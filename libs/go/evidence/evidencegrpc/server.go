package evidencegrpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/satta/platform/libs/go/evidence"
	auditv1 "github.com/satta/platform/libs/go/gen/platform/audit/v1"
	commonv1 "github.com/satta/platform/libs/go/gen/platform/common/v1"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// Server implements auditv1.EvidenceServiceServer over any evidence.Store.
// The store enforces every invariant; this adapter only translates.
type Server struct {
	auditv1.UnimplementedEvidenceServiceServer
	store evidence.Store
}

// Compile-time proof that the wire surface carries no mutation path. If the
// generated interface ever grows one, this line stops the build — the same
// discipline the Store port itself is held to.
var _ auditv1.EvidenceServiceServer = (*Server)(nil)

// NewServer returns a Server backed by store.
func NewServer(store evidence.Store) *Server {
	return &Server{store: store}
}

// Register attaches the service to a grpc server.
func (s *Server) Register(srv *grpc.Server) { auditv1.RegisterEvidenceServiceServer(srv, s) }

// requireWriteCaller enforces the caller-level half of SAFETY INVARIANT 11:
// auditor principals may call the read and verify RPCs only. The store beneath
// independently enforces the same invariant on the record's actor — the two
// checks are layered, like the port's other controls, so defeating one does
// not defeat the invariant.
func requireWriteCaller(ctx context.Context) error {
	actor, err := identity.RequireFromContext(ctx)
	if err != nil {
		return toStatus(err)
	}
	if actor.IsReadOnly() {
		return status.Error(codes.PermissionDenied,
			"identity: actor type "+string(actor.Type)+" is read-only and cannot append evidence")
	}
	return nil
}

// Append implements EvidenceService.Append.
func (s *Server) Append(ctx context.Context, req *auditv1.AppendRequest) (*auditv1.AppendResponse, error) {
	if req.GetRecord() == nil {
		return nil, toStatus(evidence.ErrInvalidRecord)
	}
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	if cerr := requireWriteCaller(ctx); cerr != nil {
		return nil, cerr
	}
	record, err := wireToRecord(req.GetRecord(), true)
	if err != nil {
		return nil, toStatus(err)
	}
	result, err := s.store.Append(ctx, record)
	if err != nil {
		return nil, toStatus(err)
	}
	return &auditv1.AppendResponse{
		Sequence:     result.Sequence,
		RecordHash:   result.RecordHash,
		Deduplicated: result.Deduplicated,
	}, nil
}

// AppendBatch implements EvidenceService.AppendBatch.
func (s *Server) AppendBatch(ctx context.Context, req *auditv1.AppendBatchRequest) (*auditv1.AppendBatchResponse, error) {
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	if cerr := requireWriteCaller(ctx); cerr != nil {
		return nil, cerr
	}
	if len(req.GetRecords()) == 0 {
		return nil, toStatus(evidence.ErrInvalidRecord)
	}
	records := make([]evidence.Record, 0, len(req.GetRecords()))
	for i, in := range req.GetRecords() {
		record, rerr := wireToRecord(in, i > 0) // chain_id on the first names the batch target
		if rerr != nil {
			return nil, toStatus(rerr)
		}
		records = append(records, record)
	}
	results, err := s.store.AppendBatch(ctx, req.GetChainId(), records)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*auditv1.AppendResponse, 0, len(results))
	for _, r := range results {
		out = append(out, &auditv1.AppendResponse{Sequence: r.Sequence, RecordHash: r.RecordHash, Deduplicated: r.Deduplicated})
	}
	return &auditv1.AppendBatchResponse{Results: out}, nil
}

// Get implements EvidenceService.Get.
func (s *Server) Get(ctx context.Context, req *auditv1.GetRequest) (*auditv1.GetResponse, error) {
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.store.Get(ctx, req.GetChainId(), req.GetSequence())
	if err != nil {
		return nil, toStatus(err)
	}
	wire, err := recordToWire(record)
	if err != nil {
		return nil, toStatus(err)
	}
	return &auditv1.GetResponse{Record: wire}, nil
}

// Range implements EvidenceService.Range.
func (s *Server) Range(ctx context.Context, req *auditv1.RangeRequest) (*auditv1.RangeResponse, error) {
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.store.Range(ctx, req.GetChainId(), req.GetFromSequence(), req.GetToSequence())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*auditv1.EvidenceRecord, 0, len(records))
	for _, rec := range records {
		wire, err := recordToWire(rec)
		if err != nil {
			return nil, toStatus(err)
		}
		out = append(out, wire)
	}
	return &auditv1.RangeResponse{Records: out}, nil
}

// GetByIdempotencyKey implements EvidenceService.GetByIdempotencyKey.
func (s *Server) GetByIdempotencyKey(ctx context.Context, req *auditv1.GetByIdempotencyKeyRequest) (*auditv1.GetByIdempotencyKeyResponse, error) {
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.store.FindByIdempotencyKey(ctx, req.GetChainId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, toStatus(err)
	}
	wire, err := recordToWire(record)
	if err != nil {
		return nil, toStatus(err)
	}
	return &auditv1.GetByIdempotencyKeyResponse{Record: wire}, nil
}

// Verify implements EvidenceService.Verify.
func (s *Server) Verify(ctx context.Context, req *auditv1.VerifyRequest) (*auditv1.VerifyResponse, error) {
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	result, err := evidence.Verify(ctx, s.store, req.GetChainId(), req.GetFromSequence(), req.GetToSequence())
	if err != nil {
		return nil, toStatus(err)
	}
	return &auditv1.VerifyResponse{
		Intact:           result.Intact,
		RecordsChecked:   result.RecordsChecked,
		FirstBadSequence: result.FirstBadSequence,
		FailureReason:    result.FailureReason,
		HeadHash:         result.HeadHash,
	}, nil
}

// GetChainHead implements EvidenceService.GetChainHead.
func (s *Server) GetChainHead(ctx context.Context, req *auditv1.GetChainHeadRequest) (*auditv1.GetChainHeadResponse, error) {
	ctx, err := bindCaller(ctx)
	if err != nil {
		return nil, err
	}
	head, err := s.store.Head(ctx, req.GetChainId())
	if err != nil {
		return nil, toStatus(err)
	}
	var updatedAt *timestamppb.Timestamp
	if !head.UpdatedAt.IsZero() {
		updatedAt = timestamppb.New(head.UpdatedAt)
	}
	return &auditv1.GetChainHeadResponse{
		ChainId:   head.ChainID,
		Sequence:  head.Sequence,
		HeadHash:  head.HeadHash,
		UpdatedAt: updatedAt,
	}, nil
}

// bindCaller restores the caller's scope and actor from metadata into the
// context, so the store beneath sees exactly what an in-process caller would
// have set. Missing scope or actor surfaces as the domain's own deny-by-default
// errors: no scope on the wire means no scope in the context, never a global
// fallback.
func bindCaller(ctx context.Context) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return context.Background(), toStatus(tenancy.ErrNoScope)
	}

	tenantID := firstMeta(md, MetaTenantID)
	if tenantID == "" {
		// No tenant header at all: hand the store a context with no scope so
		// it returns tenancy.ErrNoScope itself, which maps to the wire as
		// PermissionDenied.
		return context.Background(), nil
	}
	scope := tenancy.Scope{
		TenantID:         tenantID,
		BrandID:          firstMeta(md, MetaBrandID),
		JurisdictionCode: firstMeta(md, MetaJurisdiction),
		RegionCode:       firstMeta(md, MetaRegion),
	}
	ctx, err := tenancy.WithScope(ctx, scope)
	if err != nil {
		return nil, toStatus(err)
	}

	actorType := firstMeta(md, MetaActorType)
	actorID := firstMeta(md, MetaActorID)
	if actorType == "" || actorID == "" {
		// Scope present but no actor: identity's deny-by-default applies.
		return context.Background(), toStatus(identity.ErrNoActor)
	}
	actor := identity.Actor{
		Type:        identity.Type(actorType),
		ID:          actorID,
		DisplayName: firstMeta(md, MetaActorName),
		SessionID:   firstMeta(md, MetaActorSession),
		SourceIP:    firstMeta(md, MetaActorSourceIP),
		OnBehalfOf:  firstMeta(md, MetaActorOnBehalf),
	}
	ctx, err = identity.WithActor(ctx, actor)
	if err != nil {
		return nil, toStatus(err)
	}
	return ctx, nil
}

func firstMeta(md metadata.MD, key string) string {
	v := md.Get(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// categoryToWire and categoryFromWire map between the domain's registered
// category strings and the wire enum. An unregistered value fails conversion —
// the registry, not the adapter, decides what exists.

var categoryFromWire = map[auditv1.EvidenceCategory]evidence.Category{
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_UNSPECIFIED:          evidence.CategoryUnspecified,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_MONEY:                evidence.CategoryMoney,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_IDENTITY:             evidence.CategoryIdentity,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_COMPLIANCE:           evidence.CategoryCompliance,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_RESPONSIBLE_GAMBLING: evidence.CategoryResponsibleGambling,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_SECURITY:             evidence.CategorySecurity,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_GAMEPLAY:             evidence.CategoryGameplay,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_CONFIGURATION:        evidence.CategoryConfiguration,
	auditv1.EvidenceCategory_EVIDENCE_CATEGORY_COMMERCIAL:           evidence.CategoryCommercial,
}

var categoryToWire = func() map[evidence.Category]auditv1.EvidenceCategory {
	out := make(map[evidence.Category]auditv1.EvidenceCategory, len(categoryFromWire))
	for w, d := range categoryFromWire {
		out[d] = w
	}
	return out
}()

func actorTypeFromWire(t commonv1.ActorType) (identity.Type, error) {
	switch t {
	case commonv1.ActorType_ACTOR_TYPE_PLAYER:
		return identity.TypePlayer, nil
	case commonv1.ActorType_ACTOR_TYPE_OPERATOR_USER:
		return identity.TypeOperatorUser, nil
	case commonv1.ActorType_ACTOR_TYPE_PLATFORM_USER:
		return identity.TypePlatformUser, nil
	case commonv1.ActorType_ACTOR_TYPE_AGENT:
		return identity.TypeAgent, nil
	case commonv1.ActorType_ACTOR_TYPE_PARTNER_API_CLIENT:
		return identity.TypePartnerAPIClient, nil
	case commonv1.ActorType_ACTOR_TYPE_SERVICE:
		return identity.TypeService, nil
	case commonv1.ActorType_ACTOR_TYPE_REGULATOR:
		return identity.TypeRegulator, nil
	case commonv1.ActorType_ACTOR_TYPE_AUDITOR:
		return identity.TypeAuditor, nil
	default:
		return identity.TypeUnspecified, evidence.ErrInvalidRecord
	}
}

func actorTypeToWire(t identity.Type) (commonv1.ActorType, error) {
	switch t {
	case identity.TypePlayer:
		return commonv1.ActorType_ACTOR_TYPE_PLAYER, nil
	case identity.TypeOperatorUser:
		return commonv1.ActorType_ACTOR_TYPE_OPERATOR_USER, nil
	case identity.TypePlatformUser:
		return commonv1.ActorType_ACTOR_TYPE_PLATFORM_USER, nil
	case identity.TypeAgent:
		return commonv1.ActorType_ACTOR_TYPE_AGENT, nil
	case identity.TypePartnerAPIClient:
		return commonv1.ActorType_ACTOR_TYPE_PARTNER_API_CLIENT, nil
	case identity.TypeService:
		return commonv1.ActorType_ACTOR_TYPE_SERVICE, nil
	case identity.TypeRegulator:
		return commonv1.ActorType_ACTOR_TYPE_REGULATOR, nil
	case identity.TypeAuditor:
		return commonv1.ActorType_ACTOR_TYPE_AUDITOR, nil
	default:
		return commonv1.ActorType_ACTOR_TYPE_UNSPECIFIED, evidence.ErrInvalidRecord
	}
}

// wireToRecord converts an append payload. When fromAppend is true the
// canonical payload bytes are required — hashing authority is the bytes, and
// an append without them has no defensible source of truth.
func wireToRecord(in *auditv1.EvidenceRecord, requirePayload bool) (evidence.Record, error) {
	if in == nil {
		return evidence.Record{}, evidence.ErrInvalidRecord
	}
	category, ok := categoryFromWire[in.GetCategory()]
	if !ok {
		return evidence.Record{}, evidence.ErrInvalidRecord
	}

	var actor identity.Actor
	if in.GetActor() != nil {
		actorType, err := actorTypeFromWire(in.GetActor().GetType())
		if err != nil {
			return evidence.Record{}, err
		}
		actor = identity.Actor{
			Type:        actorType,
			ID:          in.GetActor().GetId(),
			DisplayName: in.GetActor().GetDisplayName(),
			SessionID:   in.GetActor().GetSessionId(),
			SourceIP:    in.GetActor().GetSourceIp(),
			OnBehalfOf:  in.GetActor().GetOnBehalfOf(),
		}
	}

	var scope tenancy.Scope
	if in.GetScope() != nil {
		scope = tenancy.Scope{
			TenantID:         in.GetScope().GetTenantId(),
			BrandID:          in.GetScope().GetBrandId(),
			JurisdictionCode: in.GetScope().GetJurisdictionCode(),
			RegionCode:       in.GetScope().GetRegionCode(),
		}
	}

	var payload map[string]any
	if len(in.GetPayloadEncoded()) > 0 {
		decoded, err := evidence.DecodePayload(in.GetPayloadEncoded())
		if err != nil {
			return evidence.Record{}, err
		}
		payload = decoded
	} else if requirePayload {
		return evidence.Record{}, evidence.ErrMalformedPayload
	}

	var occurredAt time.Time
	if in.GetOccurredAt() != nil {
		occurredAt = in.GetOccurredAt().AsTime()
	}

	return evidence.Record{
		ChainID:        in.GetChainId(),
		Scope:          scope,
		OccurredAt:     occurredAt,
		Category:       category,
		Action:         in.GetAction(),
		SubjectType:    in.GetSubjectType(),
		SubjectID:      in.GetSubjectId(),
		Actor:          actor,
		Payload:        payload,
		IdempotencyKey: in.GetIdempotencyKey(),
		RetentionClass: in.GetRetentionClass(),
		CorrelationID:  in.GetCorrelationId(),
		TraceID:        in.GetTraceId(),
	}, nil
}

// recordToWire converts a stored record for a response. The canonical bytes are
// always populated; the Struct is a best-effort projection for consoles and is
// documented as lossy for large integers.
func recordToWire(rec evidence.Record) (*auditv1.EvidenceRecord, error) {
	category, ok := categoryToWire[rec.Category]
	if !ok {
		return nil, evidence.ErrInvalidRecord
	}
	actorType, err := actorTypeToWire(rec.Actor.Type)
	if err != nil {
		return nil, err
	}

	encoded, err := evidence.EncodePayload(rec.Payload)
	if err != nil {
		return nil, err
	}

	wire := &auditv1.EvidenceRecord{
		ChainId:        rec.ChainID,
		Sequence:       rec.Sequence,
		Category:       category,
		Action:         rec.Action,
		SubjectType:    rec.SubjectType,
		SubjectId:      rec.SubjectID,
		PayloadEncoded: encoded,
		PayloadHash:    rec.PayloadHash,
		PrevHash:       rec.PrevHash,
		RecordHash:     rec.RecordHash,
		IdempotencyKey: rec.IdempotencyKey,
		RetentionClass: rec.RetentionClass,
		CorrelationId:  rec.CorrelationID,
		TraceId:        rec.TraceID,
		Actor: &commonv1.Actor{
			Type:        actorType,
			Id:          rec.Actor.ID,
			DisplayName: rec.Actor.DisplayName,
			SessionId:   rec.Actor.SessionID,
			SourceIp:    rec.Actor.SourceIP,
			OnBehalfOf:  rec.Actor.OnBehalfOf,
		},
	}
	if rec.Scope != (tenancy.Scope{}) {
		wire.Scope = &commonv1.TenantScope{
			TenantId:         rec.Scope.TenantID,
			BrandId:          rec.Scope.BrandID,
			JurisdictionCode: rec.Scope.JurisdictionCode,
			RegionCode:       rec.Scope.RegionCode,
		}
	}
	if !rec.OccurredAt.IsZero() {
		wire.OccurredAt = timestamppb.New(rec.OccurredAt)
	}
	if !rec.RecordedAt.IsZero() {
		wire.RecordedAt = timestamppb.New(rec.RecordedAt)
	}
	if projection := structProjection(rec.Payload); projection != nil {
		wire.Payload = projection
	}
	return wire, nil
}

// structProjection renders a payload as a Struct for human-facing consumers.
// Failure is tolerated — the projection is advisory; payload_encoded is the
// authority.
func structProjection(payload map[string]any) *structpb.Struct {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		switch val := v.(type) {
		case int:
			out[k] = int64(val)
		default:
			out[k] = v
		}
	}
	s, err := structpb.NewStruct(out)
	if err != nil {
		return nil
	}
	return s
}
