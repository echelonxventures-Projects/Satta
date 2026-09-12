// Integer minor-unit money, mirroring libs/go/money. SAFETY INVARIANT 1 in the
// browser: amounts are bigint counts of minor units; floats are not permitted
// anywhere in this package, and parsing a float-formatted string is an error,
// not a silent truncation.
//
// The display layer formats server-computed amounts and performs no money
// arithmetic. This package's Allocate exists so the remainder-tie-break rule
// is testable against the shared golden corpus on the same footing as the Go
// implementation — UI code that recomputes a split must get the server's
// exact answer, not "close enough".

export type Currency = {
  /** ISO 4217 for fiat, registry key for non-fiat (crypto, bonus, sweeps). */
  code: string
  /** Minor-unit exponent: JPY 0, USD/EUR 2, KWD 3, BTC 8. */
  scale: number
}

export class MoneyError extends Error {}

export function validateCurrency(cur: Currency): void {
  if (!/^[A-Z0-9]{3}$/.test(cur.code)) {
    throw new MoneyError(`currency code ${JSON.stringify(cur.code)} must be 3 uppercase alphanumerics`)
  }
  if (!Number.isInteger(cur.scale) || cur.scale < 0 || cur.scale > 18) {
    throw new MoneyError(`currency ${cur.code} has invalid scale ${cur.scale}`)
  }
}

/**
 * Parses a decimal string into minor units. Accepts "12", "12.34", "-0.05",
 * "+7.1", ".5"; rejects anything finer than the currency scale (never a
 * silent rounding) and anything that is not plain decimal text.
 */
export function parse(raw: string, cur: Currency): bigint {
  validateCurrency(cur)
  const s = raw.trim()
  if (s === "") {
    throw new MoneyError(`cannot parse empty string as ${cur.code}`)
  }
  const m = /^([+-]?)(\d*)(?:\.(\d*))?$/.exec(s)
  if (!m) {
    throw new MoneyError(`cannot parse ${JSON.stringify(raw)} as ${cur.code}: not a decimal number`)
  }
  const [, sign, intPart = "", fracPart = ""] = m
  if (intPart === "" && fracPart === "") {
    throw new MoneyError(`cannot parse ${JSON.stringify(raw)} as ${cur.code}: no digits`)
  }
  if (fracPart.length > cur.scale) {
    throw new MoneyError(
      `cannot parse ${JSON.stringify(raw)} as ${cur.code}: ${fracPart.length} decimal places exceed the currency scale ${cur.scale}`,
    )
  }
  const padded = fracPart.padEnd(cur.scale, "0")
  const minor = BigInt((intPart === "" ? "0" : intPart) + padded)
  return sign === "-" ? -minor : minor
}

/** Renders minor units as a plain decimal string (no locale; see @satta/formatting). */
export function decimal(minor: bigint, cur: Currency): string {
  validateCurrency(cur)
  const neg = minor < 0n
  const digits = (neg ? -minor : minor).toString().padStart(cur.scale + 1, "0")
  const cut = digits.length - cur.scale
  const intPart = digits.slice(0, cut)
  const out = cur.scale === 0 ? intPart : `${intPart}.${digits.slice(cut)}`
  return neg ? `-${out}` : out
}

/**
 * Waterfall allocation: integer parts, remainder distributed one minor unit
 * each to the first N buckets in order, weights non-negative with at least
 * one positive. Identical tie-break rule to the Go implementation; the golden
 * corpus is the arbiter of any drift.
 */
export function allocate(amount: bigint, weights: readonly bigint[]): bigint[] {
  if (weights.length === 0) {
    throw new MoneyError("Allocate requires at least one weight")
  }
  if (weights.some((w) => w < 0n)) {
    throw new MoneyError("allocation weights must be non-negative")
  }
  const total = weights.reduce((a, b) => a + b, 0n)
  if (total === 0n) {
    throw new MoneyError("allocation weights must not all be zero")
  }
  const parts: bigint[] = []
  let remainder = amount
  for (const w of weights) {
    const part = (amount * w) / total
    parts.push(part)
    remainder -= part
  }
  // Integer division truncated toward zero; walk the remainder in bucket
  // order, adding/subtracting one unit until the parts sum exactly.
  for (let i = 0; remainder !== 0n; i = (i + 1) % parts.length) {
    if (remainder > 0n) {
      parts[i] = (parts[i] ?? 0n) + 1n
      remainder -= 1n
    } else {
      parts[i] = (parts[i] ?? 0n) - 1n
      remainder += 1n
    }
  }
  return parts
}
