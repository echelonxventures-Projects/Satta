// Contract-integrity tests: the generated client must speak the same wire
// format the Go side proved (S8). If codegen or the proto drift, serialize +
// reparse of a pinned record diverges.

import { test } from "node:test"
import assert from "node:assert/strict"
import { create, fromBinary, toBinary } from "@bufbuild/protobuf"

import { EvidenceRecordSchema, EvidenceCategory } from "../src/gen/platform/audit/v1/evidence_pb.ts"
import { TenantScopeSchema, ActorType } from "../src/gen/platform/common/v1/tenant_pb.ts"

test("EvidenceRecord round-trips through the binary wire format", () => {
  const record = create(EvidenceRecordSchema, {
    chainId: "tenant-a-money",
    sequence: 7n,
    scope: { tenantId: "tenant-a", brandId: "", jurisdictionCode: "mt", regionCode: "eu-west" },
    category: EvidenceCategory.MONEY,
    action: "ledger.transfer.posted",
    subjectType: "wallet",
    subjectId: "w-1",
    actor: { type: ActorType.SERVICE, id: "ledger", displayName: "", sessionId: "", sourceIp: "", onBehalfOf: "" },
    payloadEncoded: new Uint8Array([0x6d, 0x02, 0x73, 0x01, 0x61, 0x69, 0x05]),
    payloadHash: new Uint8Array(32).fill(3),
    prevHash: new Uint8Array(32).fill(1),
    recordHash: new Uint8Array(32).fill(9),
    idempotencyKey: "key-7",
    retentionClass: "financial-7y",
    correlationId: "corr-7",
    traceId: "",
  })
  const bytes = toBinary(EvidenceRecordSchema, record)
  const back = fromBinary(EvidenceRecordSchema, bytes)
  assert.equal(back.chainId, "tenant-a-money")
  assert.equal(back.sequence, 7n)
  assert.equal(back.scope?.tenantId, "tenant-a")
  assert.equal(back.category, EvidenceCategory.MONEY)
  assert.deepEqual(back.payloadEncoded, record.payloadEncoded)
  assert.equal(back.idempotencyKey, "key-7")
})

test("scope and actor descriptors match the published contract", () => {
  // These fields were the S8 wire gaps (on_behalf_of absent; canonical
  // payload absent). Pinning them here means a regeneration that loses a
  // field fails loudly in this package, not silently in a future non-Go
  // client.
  const scope = fromBinary(
    TenantScopeSchema,
    toBinary(TenantScopeSchema, create(TenantScopeSchema, { tenantId: "t", brandId: "b", jurisdictionCode: "mt", regionCode: "r" })),
  )
  assert.equal(scope.tenantId, "t")
  assert.equal(scope.jurisdictionCode, "mt")

  const rec = fromBinary(
    EvidenceRecordSchema,
    toBinary(
      EvidenceRecordSchema,
      create(EvidenceRecordSchema, { actor: { type: ActorType.SERVICE, id: "s", onBehalfOf: "operator_user:op-7" } }),
    ),
  )
  assert.equal(rec.actor?.onBehalfOf, "operator_user:op-7")
})
