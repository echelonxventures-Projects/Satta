// The typed API client, generated from proto/ — the same contract source as
// the Go services. Never re-type these messages by hand (ADR-0005).

export * as evidence from "./gen/platform/audit/v1/evidence_pb.ts"
export * as common from "./gen/platform/common/v1/tenant_pb.ts"
export * as moneyMsg from "./gen/platform/common/v1/money_pb.ts"
export * as metering from "./gen/platform/metering/v1/usage_pb.ts"

export { EvidenceService } from "./gen/platform/audit/v1/evidence_pb.ts"
export { MeteringIngestService } from "./gen/platform/metering/v1/usage_pb.ts"

export { createClient } from "@connectrpc/connect"
export { createGrpcTransport } from "@connectrpc/connect-node"
