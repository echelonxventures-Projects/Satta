// Golden corpus replay: the same file the Go implementation generated and
// CI checks (libs/go/golden/testdata/corpus.json). ADR-0005 rule 2 says
// packages/money must reproduce these vectors — this test is the proof that
// Invariant 1 survives into the browser instead of "holding on the server and
// silently breaking in the browser".

import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import path from "node:path"
import { parse, decimal, allocate, MoneyError } from "../index.ts"

const here = path.dirname(fileURLToPath(import.meta.url))
const corpusPath = path.resolve(here, "../../../libs/go/golden/testdata/corpus.json")
const corpus = JSON.parse(readFileSync(corpusPath, "utf8")) as {
  version: number
  money: Array<{ id: string; op: string; inputs: unknown[]; result?: unknown; error?: string; note?: string }>
  allocation: Array<{ id: string; amount: string; weights: string[]; parts?: string[]; error?: string; note?: string }>
}

if (corpus.version !== 1) {
  throw new Error(`unsupported corpus version ${corpus.version}`)
}

// The currency table mirrors the vectors' scale inputs.
const ccy = (code: string, scale: number) => ({ code, scale })

function inputMinor(v: unknown): bigint {
  if (typeof v === "number") return BigInt(v)
  if (typeof v === "string") {
    if (v === "max_int64") return 9223372036854775807n
    if (v === "min_int64") return -9223372036854775808n
    throw new Error(`unknown named input ${v}`)
  }
  throw new Error(`unsupported input type ${typeof v}`)
}

test("money vectors: parse", () => {
  for (const v of corpus.money.filter((x) => x.op === "parse")) {
    const cur = ccy(v.inputs[1] as string, Number(v.inputs[2]))
    if (v.error) {
      assert.throws(() => parse(String(v.inputs[0]), cur), MoneyError, v.id)
      continue
    }
    assert.equal(parse(String(v.inputs[0]), cur), inputMinor(v.result), v.id)
  }
})

test("money vectors: decimal", () => {
  for (const v of corpus.money.filter((x) => x.op === "decimal")) {
    const cur = ccy(v.inputs[1] as string, Number(v.inputs[2]))
    assert.equal(decimal(inputMinor(v.inputs[0]), cur), v.result, v.id)
  }
})

test("allocation vectors: waterfall + value preservation", () => {
  for (const v of corpus.allocation) {
    const amount = BigInt(v.amount)
    const weights = v.weights.map((w) => BigInt(w))
    if (v.error) {
      assert.throws(() => allocate(amount, weights), MoneyError, v.id)
      continue
    }
    const got = allocate(amount, weights)
    assert.deepEqual(
      got,
      (v.parts ?? []).map((p) => BigInt(p)),
      v.id,
    )
    const sum = got.reduce((a, b) => a + b, 0n)
    assert.equal(sum, amount, `${v.id}: allocation must be value-preserving`)
  }
})
