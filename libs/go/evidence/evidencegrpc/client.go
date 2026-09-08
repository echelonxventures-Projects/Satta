package evidencegrpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/satta/platform/libs/go/evidence"
	auditv1 "github.com/satta/platform/libs/go/gen/platform/audit/v1"
	"github.com/satta/platform/libs/go/identity"
	"github.com/satta/platform/libs/go/tenancy"
)

// Client implements evidence.Store over an EvidenceServiceClient. It exists so
// that the same contract suite which validates an in-process store also
// validates any server implementing the wire contract — including non-Go ones.
type Client struct {
	client auditv1.EvidenceServiceClient
}

var _ evidence.Store = (*Client)(nil)

// NewClient wraps a gRPC client of the evidence service.
func NewClient(cc grpc.ClientConnInterface) *Client {
	return &Client{client: auditv1.NewEvidenceServiceClient(cc)}
}

// Append implements evidence.Store.
func (c *Client) Append(ctx context.Context, record evidence.Record) (evidence.AppendResult, error) {
	wire, err := recordToWire(record)
	if err != nil {
		return evidence.AppendResult{}, err
	}
	outCtx, err := outgoingCaller(ctx)
	if err != nil {
		return evidence.AppendResult{}, err
	}
	resp, err := c.client.Append(outCtx, &auditv1.AppendRequest{Record: wire})
	if err != nil {
		return evidence.AppendResult{}, fromStatus(err)
	}
	return evidence.AppendResult{
		Sequence:     resp.GetSequence(),
		RecordHash:   resp.GetRecordHash(),
		Deduplicated: resp.GetDeduplicated(),
	}, nil
}

// AppendBatch implements evidence.Store.
func (c *Client) AppendBatch(ctx context.Context, chainID string, records []evidence.Record) ([]evidence.AppendResult, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("evidence: AppendBatch requires at least one record")
	}
	wireRecords := make([]*auditv1.EvidenceRecord, 0, len(records))
	for _, rec := range records {
		wire, err := recordToWire(rec)
		if err != nil {
			return nil, err
		}
		// The record's own chain_id is sent verbatim, even when it disagrees
		// with the batch target: the mismatch is the store's contract to
		// reject, and overwriting it here would mask exactly that failure.
		wireRecords = append(wireRecords, wire)
	}
	outCtx, err := outgoingCaller(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.AppendBatch(outCtx, &auditv1.AppendBatchRequest{ChainId: chainID, Records: wireRecords})
	if err != nil {
		return nil, fromStatus(err)
	}
	results := make([]evidence.AppendResult, 0, len(resp.GetResults()))
	for _, r := range resp.GetResults() {
		results = append(results, evidence.AppendResult{
			Sequence:     r.GetSequence(),
			RecordHash:   r.GetRecordHash(),
			Deduplicated: r.GetDeduplicated(),
		})
	}
	return results, nil
}

// Get implements evidence.Store.
func (c *Client) Get(ctx context.Context, chainID string, sequence uint64) (evidence.Record, error) {
	outCtx, err := outgoingCaller(ctx)
	if err != nil {
		return evidence.Record{}, err
	}
	resp, err := c.client.Get(outCtx, &auditv1.GetRequest{ChainId: chainID, Sequence: sequence})
	if err != nil {
		return evidence.Record{}, fromStatus(err)
	}
	return c.wireToStored(resp.GetRecord())
}

// Range implements evidence.Store.
func (c *Client) Range(ctx context.Context, chainID string, from, to uint64) ([]evidence.Record, error) {
	outCtx, err := outgoingCaller(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Range(outCtx, &auditv1.RangeRequest{ChainId: chainID, FromSequence: from, ToSequence: to})
	if err != nil {
		return nil, fromStatus(err)
	}
	out := make([]evidence.Record, 0, len(resp.GetRecords()))
	for _, wire := range resp.GetRecords() {
		rec, err := c.wireToStored(wire)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Head implements evidence.Store.
func (c *Client) Head(ctx context.Context, chainID string) (evidence.ChainHead, error) {
	outCtx, err := outgoingCaller(ctx)
	if err != nil {
		return evidence.ChainHead{}, err
	}
	resp, err := c.client.GetChainHead(outCtx, &auditv1.GetChainHeadRequest{ChainId: chainID})
	if err != nil {
		return evidence.ChainHead{}, fromStatus(err)
	}
	head := evidence.ChainHead{
		ChainID:  resp.GetChainId(),
		Sequence: resp.GetSequence(),
		HeadHash: resp.GetHeadHash(),
	}
	if resp.GetUpdatedAt() != nil {
		head.UpdatedAt = resp.GetUpdatedAt().AsTime().UTC()
	}
	if head.IsEmpty() {
		head.HeadHash = evidence.GenesisHash
	}
	return head, nil
}

// FindByIdempotencyKey implements evidence.Store.
func (c *Client) FindByIdempotencyKey(ctx context.Context, chainID, idempotencyKey string) (evidence.Record, error) {
	outCtx, err := outgoingCaller(ctx)
	if err != nil {
		return evidence.Record{}, err
	}
	resp, err := c.client.GetByIdempotencyKey(outCtx, &auditv1.GetByIdempotencyKeyRequest{ChainId: chainID, IdempotencyKey: idempotencyKey})
	if err != nil {
		return evidence.Record{}, fromStatus(err)
	}
	return c.wireToStored(resp.GetRecord())
}

// wireToStored rebuilds a full stored record, including server-assigned
// fields, from a response message. Server-assigned fields that the wire omits
// (an empty head hash on an empty chain, for instance) are restored to their
// defined zero forms.
func (c *Client) wireToStored(wire *auditv1.EvidenceRecord) (evidence.Record, error) {
	if wire == nil {
		return evidence.Record{}, evidence.ErrNotFound
	}
	rec, err := wireToRecord(wire, false)
	if err != nil {
		return evidence.Record{}, err
	}
	rec.Sequence = wire.GetSequence()
	rec.PayloadHash = wire.GetPayloadHash()
	rec.PrevHash = wire.GetPrevHash()
	if len(rec.PrevHash) == 0 && rec.Sequence <= 1 {
		rec.PrevHash = evidence.GenesisHash
	}
	rec.RecordHash = wire.GetRecordHash()
	if wire.GetRecordedAt() != nil {
		rec.RecordedAt = wire.GetRecordedAt().AsTime()
	}
	return rec, nil
}

// outgoingCaller reads the caller's scope and actor from ctx and attaches them
// as outgoing metadata — the inverse of bindCaller on the server. A context
// without scope/actor still goes out bare, and the server responds with the
// domain's own deny-by-default errors.
func outgoingCaller(ctx context.Context) (context.Context, error) {
	scope, err := tenancy.RequireFromContext(ctx)
	if err != nil {
		return nil, err
	}
	actor, err := identity.RequireFromContext(ctx)
	if err != nil {
		return nil, err
	}
	pairs := []string{
		MetaTenantID, scope.TenantID,
		MetaBrandID, scope.BrandID,
		MetaJurisdiction, scope.JurisdictionCode,
		MetaRegion, scope.RegionCode,
		MetaActorType, string(actor.Type),
		MetaActorID, actor.ID,
		MetaActorName, actor.DisplayName,
		MetaActorSession, actor.SessionID,
		MetaActorSourceIP, actor.SourceIP,
		MetaActorOnBehalf, actor.OnBehalfOf,
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...), nil
}
