#!/usr/bin/env bash
#
# Warns when implementation-state.md looks stale relative to the code.
#
# This warns and never fails. A commit hook that blocks on documentation hygiene gets
# bypassed with --no-verify, and a hook that is routinely bypassed enforces nothing at all —
# including the checks in it that do matter.
#
# The reason it exists: this project's steering explicitly relies on the state file being
# accurate so that a new session knows where things stand without asking. That contract broke
# once already — the file read "Pre-Implementation" while a complete shared-library layer sat
# in libs/go. A nudge at commit time is cheap insurance against a repeat.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 0

STATE_FILE=".kiro/steering/implementation-state.md"

yellow=$'\033[33m'; dim=$'\033[2m'; reset=$'\033[0m'
if [[ ! -t 1 ]]; then yellow=""; dim=""; reset=""; fi

[[ -f $STATE_FILE ]] || {
    printf '%swarn%s  %s is missing\n' "$yellow" "$reset" "$STATE_FILE"
    exit 0
}

# Portable mtime: BSD stat on macOS, GNU stat elsewhere.
mtime() {
    stat -f %m "$1" 2>/dev/null || stat -c %Y "$1" 2>/dev/null || echo 0
}

state_mtime=$(mtime "$STATE_FILE")
newest_file=""
newest_mtime=0

# Only source that represents implementation progress. Config and lockfiles churn for
# reasons that have nothing to do with where a task stands.
while IFS= read -r file; do
    [[ -f $file ]] || continue
    file_mtime=$(mtime "$file")
    if (( file_mtime > newest_mtime )); then
        newest_mtime=$file_mtime
        newest_file=$file
    fi
done < <(find libs cmd services apps packages proto -type f \
             \( -name '*.go' -o -name '*.rs' -o -name '*.ts' -o -name '*.tsx' \
                -o -name '*.sql' -o -name '*.proto' -o -name '*.ex' \) \
             -not -path '*/gen/*' -not -name '*.pb.go' 2>/dev/null)

if [[ -z $newest_file ]]; then
    exit 0
fi

if (( newest_mtime > state_mtime )); then
    age_hours=$(( (newest_mtime - state_mtime) / 3600 ))
    printf '%swarn%s  %s is older than %s' "$yellow" "$reset" "$STATE_FILE" "$newest_file"
    if (( age_hours > 0 )); then
        printf ' (by ~%dh)' "$age_hours"
    fi
    printf '\n'
    printf '        %sCode has moved since the state file was updated. If a task changed\n' "$dim"
    printf '        status, record it — the next session reads that file to orient.%s\n' "$reset"
fi

# Structural sanity: the file's own rules say Completed Tasks is append-only and that
# decisions belong in docs/adr. A missing section usually means the file was rewritten by
# hand and a section was dropped.
for section in "## Completed Tasks" "## In Progress" "## Blockers" "## Next Up"; do
    if ! grep -qF "$section" "$STATE_FILE"; then
        printf '%swarn%s  %s has no "%s" section\n' "$yellow" "$reset" "$STATE_FILE" "$section"
    fi
done

exit 0
