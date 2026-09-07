#!/usr/bin/env bash
#
# Static checks for the safety invariants that can be checked statically.
#
# This is not a substitute for the tests, and it cannot prove an invariant holds. What it
# does is catch the specific regressions that are cheap to introduce, invisible in review,
# and expensive once shipped — a float creeping into money code, a direct wall-clock read, a
# deletion path appearing on an append-only table.
#
# Every check below exists because its absence would breach a numbered invariant. If a check
# starts failing legitimately, the fix is to change the code, not the check. If the check
# itself is wrong, change it here and say why in the commit message.
set -uo pipefail

# Repo root, regardless of where this was invoked from.
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

failures=0
checks=0

red=$'\033[31m'; green=$'\033[32m'; yellow=$'\033[33m'; dim=$'\033[2m'; reset=$'\033[0m'
if [[ ! -t 1 ]]; then red=""; green=""; yellow=""; dim=""; reset=""; fi

fail() {
    printf '%sFAIL%s  %s\n' "$red" "$reset" "$1"
    shift
    for line in "$@"; do printf '        %s%s%s\n' "$dim" "$line" "$reset"; done
    failures=$((failures + 1))
}

pass() {
    checks=$((checks + 1))
    printf '%s ok %s  %s\n' "$green" "$reset" "$1"
}

warn() { printf '%swarn%s  %s\n' "$yellow" "$reset" "$1"; }

# Go sources, excluding generated code. Generated protobuf carries float accessors and
# vendored patterns that these checks would flag meaninglessly.
go_sources() {
    find libs cmd services apps packages -name '*.go' -type f 2>/dev/null \
        | grep -v '/gen/' \
        | grep -v '\.pb\.go$' \
        | sort
}

go_sources_excluding_tests() {
    go_sources | grep -v '_test\.go$' | grep -v '/[a-z]*test/'
}

# ---------------------------------------------------------------------------------------
# INVARIANT 1 — integer-only money arithmetic. No floating point for money, ever.
# ---------------------------------------------------------------------------------------

check_no_float_in_money() {
    # A float in a type-switch `case` is inspection, not arithmetic — that is how the
    # evidence payload encoder *rejects* floats. Flag declarations and expressions only.
    local hits
    hits=$(go_sources_excluding_tests \
        | xargs grep -nE '\b(float32|float64)\b' 2>/dev/null \
        | grep -vE ':[0-9]+:\s*case .*float' \
        | grep -vE ':[0-9]+:\s*//' \
        || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 1: floating-point types found in non-test Go source" \
             "Money is integer minor units; rates are numerator/denominator pairs." \
             "$hits"
    else
        pass "INVARIANT 1: no floating-point arithmetic types in non-test Go source"
    fi
}

check_no_parsefloat() {
    local hits
    hits=$(go_sources_excluding_tests | xargs grep -n 'ParseFloat' 2>/dev/null || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 1: strconv.ParseFloat used outside tests" \
             "Parse into money.Amount or money.Ratio instead." \
             "$hits"
    else
        pass "INVARIANT 1: no strconv.ParseFloat outside tests"
    fi
}

check_no_float_money_columns() {
    # The comment filter must run against the matched line, not the "file:line:" prefix
    # that grep prepends — hence the ':N:' anchor.
    local hits
    hits=$(find . -name '*.sql' -type f -not -path './.git/*' 2>/dev/null \
        | xargs grep -inE '\b(real|double precision|float[48]?)\b' 2>/dev/null \
        | grep -vE ':[0-9]+:\s*--' \
        || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 1: floating-point column type in a migration" \
             "Monetary and rate columns are bigint minor units or integer pairs." \
             "$hits"
    else
        pass "INVARIANT 1: no floating-point columns in migrations"
    fi
}

# ---------------------------------------------------------------------------------------
# INVARIANT 12 — single time authority. UTC storage, no local-time arithmetic.
# ---------------------------------------------------------------------------------------

check_no_direct_wall_clock() {
    # libs/go/clock is the one package permitted to read the host clock; that is its purpose.
    local hits
    hits=$(go_sources_excluding_tests \
        | grep -v '^libs/go/clock/' \
        | xargs grep -nE '\btime\.(Now|Since)\(' 2>/dev/null || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 12: direct wall-clock read outside libs/go/clock" \
             "Inject clock.Clock. Settlement windows, bonus expiry and self-exclusion" \
             "boundaries are untestable if code reads the wall clock." \
             "$hits"
    else
        pass "INVARIANT 12: no direct wall-clock reads outside libs/go/clock"
    fi
}

check_no_local_timezone_use() {
    local hits
    hits=$(go_sources_excluding_tests \
        | xargs grep -nE 'time\.Local|\.In\(time\.Local\)' 2>/dev/null || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 12: time.Local referenced" \
             "Display timezone is a presentation-layer parameter and is never stored." \
             "$hits"
    else
        pass "INVARIANT 12: no time.Local references"
    fi
}

# ---------------------------------------------------------------------------------------
# INVARIANT 2 — RNG generation path is structurally enforced.
# ---------------------------------------------------------------------------------------

check_no_math_rand() {
    local hits
    hits=$(go_sources_excluding_tests \
        | xargs grep -nE '"math/rand(/v2)?"' 2>/dev/null || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 2: math/rand imported in non-test source" \
             "Game outcomes go through the certified RNG service. Identifiers and tokens" \
             "use crypto/rand." \
             "$hits"
    else
        pass "INVARIANT 2: no math/rand in non-test source"
    fi
}

# ---------------------------------------------------------------------------------------
# INVARIANT 4 — evidence store is append-only, tamper-evident, no deletion path.
# ---------------------------------------------------------------------------------------

check_evidence_port_has_no_mutation() {
    local store=libs/go/evidence/store.go
    [[ -f $store ]] || { warn "INVARIANT 4: $store not found, skipping port check"; return; }

    # Method declarations inside the Store interface. A mutation verb appearing here means
    # the port itself can express deletion, which is the thing it must not do.
    local hits
    hits=$(awk '/^type Store interface/,/^}/' "$store" \
        | grep -nE '^\s+(Update|Delete|Purge|Truncate|Remove|Erase|Redact)\(' || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 4: evidence.Store exposes a mutation method" \
             "The port must not be able to express deletion or mutation." \
             "$hits"
    else
        pass "INVARIANT 4: evidence.Store exposes no mutation method"
    fi
}

check_append_only_enforcement_present() {
    local checked=0
    for migration in libs/go/evidence/migrations/*.sql libs/go/metering/migrations/*.sql; do
        [[ -f $migration ]] || continue
        checked=1
        if ! grep -qiE 'BEFORE (UPDATE|DELETE|TRUNCATE)' "$migration"; then
            continue  # Not every migration adds enforcement; only the ones creating tables.
        fi
        if ! grep -qiE 'RAISE EXCEPTION' "$migration"; then
            fail "INVARIANT 4/7: $migration has a mutation trigger that does not raise" \
                 "A trigger that does not raise is decoration."
        fi
    done
    if [[ $checked -eq 1 ]]; then
        pass "INVARIANT 4/7: append-only triggers raise where present"
    else
        warn "INVARIANT 4/7: no migrations found to check"
    fi
}

check_no_trigger_removal() {
    local hits
    hits=$(find . -name '*.sql' -type f -not -path './.git/*' 2>/dev/null \
        | xargs grep -inE 'DROP (TRIGGER|FUNCTION|POLICY)|DISABLE (TRIGGER|ROW LEVEL SECURITY)' 2>/dev/null \
        | grep -vE ':[0-9]+:\s*--' || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 4/7: a migration drops or disables an enforcement object" \
             "Removing append-only or isolation enforcement is an invariant breach." \
             "$hits"
    else
        pass "INVARIANT 4/7: no migration drops or disables enforcement"
    fi
}

check_forward_only_migrations() {
    local hits
    hits=$(find . -type f \( -name '*.down.sql' -o -name 'down_*.sql' -o -name '*_down.sql' \) \
        -not -path './.git/*' 2>/dev/null || true)
    if [[ -n "$hits" ]]; then
        fail "Migrations are forward-only; a down migration was found" \
             "A down migration cannot restore deleted rows, and on a table holding" \
             "regulated records, running one would itself breach INVARIANT 4." \
             "$hits"
    else
        pass "Migrations are forward-only (no down files)"
    fi
}

# ---------------------------------------------------------------------------------------
# INVARIANT 3 — deny-by-default authorisation. INVARIANT 11 — auditor read-only.
# ---------------------------------------------------------------------------------------

check_tenant_isolation_policies() {
    local missing=()
    for migration in libs/go/*/migrations/*.sql; do
        [[ -f $migration ]] || continue
        # A migration that creates a table carrying tenant_id must also enable RLS on it.
        if grep -qiE '^\s*tenant_id\s' "$migration" && ! grep -qi 'ENABLE ROW LEVEL SECURITY' "$migration"; then
            missing+=("$migration")
        fi
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        fail "INVARIANT 3: tenant-scoped table without row-level security" \
             "Tenant isolation is enforced at the database layer as well as the application." \
             "${missing[@]}"
    else
        pass "INVARIANT 3: every tenant-scoped migration enables row-level security"
    fi
}

check_auditor_is_read_only() {
    local actor=libs/go/identity/actor.go
    [[ -f $actor ]] || { warn "INVARIANT 11: $actor not found, skipping"; return; }
    if grep -q 'func (a Actor) IsReadOnly()' "$actor" \
       && grep -q 'TypeAuditor' "$actor" \
       && grep -q 'func (a Actor) RequireWriteCapable()' "$actor"; then
        pass "INVARIANT 11: auditor read-only enforcement present in identity package"
    else
        fail "INVARIANT 11: auditor read-only enforcement missing or renamed" \
             "Actor.IsReadOnly and Actor.RequireWriteCapable are what write paths call."
    fi
}

# ---------------------------------------------------------------------------------------
# INVARIANT 7 — metered usage carries no money and no ledger reference.
# ---------------------------------------------------------------------------------------

check_usage_carries_no_money() {
    local event=libs/go/metering/event.go
    [[ -f $event ]] || { warn "INVARIANT 7: $event not found, skipping"; return; }
    local hits
    hits=$(awk '/^type UsageEvent struct/,/^}/' "$event" \
        | grep -nE '\b(Amount|Price|Cost|Currency|LedgerAccount|AccountID)\b' || true)
    if [[ -n "$hits" ]]; then
        fail "INVARIANT 7: UsageEvent carries a monetary or ledger field" \
             "Usage carries quantity only. Pricing happens in the rating engine, which" \
             "posts to billing accounts and never to the gaming ledger." \
             "$hits"
    else
        pass "INVARIANT 7: UsageEvent carries no monetary or ledger field"
    fi
}

# ---------------------------------------------------------------------------------------
# DoD gate 1 — no literal business-policy values in code.
# ---------------------------------------------------------------------------------------

check_no_resolve_with_default() {
    local hits
    hits=$(go_sources_excluding_tests \
        | xargs grep -nE 'func .*Resolve.*[Dd]efault|ResolveOrDefault|GetOrDefault' 2>/dev/null || true)
    if [[ -n "$hits" ]]; then
        fail "DoD gate 1: a resolve-with-default helper was added to config" \
             "A default at the call site is a hardcoded policy value in disguise, and is how" \
             "one jurisdiction silently runs another's rules. A missing key must be an error." \
             "$hits"
    else
        pass "DoD gate 1: config has no resolve-with-default path"
    fi
}

# ---------------------------------------------------------------------------------------

printf '\n%sSafety invariant checks%s\n\n' "$dim" "$reset"

check_no_float_in_money
check_no_parsefloat
check_no_float_money_columns
check_no_direct_wall_clock
check_no_local_timezone_use
check_no_math_rand
check_evidence_port_has_no_mutation
check_append_only_enforcement_present
check_no_trigger_removal
check_forward_only_migrations
check_tenant_isolation_policies
check_auditor_is_read_only
check_usage_carries_no_money
check_no_resolve_with_default

printf '\n'
if [[ $failures -gt 0 ]]; then
    printf '%s%d check(s) failed%s — see .kiro/steering/safety-invariants.md\n\n' \
        "$red" "$failures" "$reset"
    exit 1
fi
printf '%sall %d invariant checks passed%s\n\n' "$green" "$checks" "$reset"
