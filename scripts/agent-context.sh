#!/usr/bin/env bash

# Read-only, dependency-light context summary for a fresh maintainer session.
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
ledger="$repo_root/registry-research/candidates.yaml"
json=0
if [ "${1:-}" = "--json" ]; then json=1; shift; fi
if [ "$#" -ne 0 ]; then
  printf '%s\n' 'usage: ./scripts/agent-context.sh [--json]' >&2
  exit 2
fi

git_value() { local root=$1; shift; git -C "$root" "$@" 2>/dev/null || printf '%s' 'unavailable'; }
git_state() {
  if [ -n "$(git -C "$1" status --porcelain 2>/dev/null)" ]; then printf dirty; else printf clean; fi
}
registry_root=''
sibling="$repo_root/../tarlink-registry"
if [ -d "$sibling/.git" ]; then registry_root=$(CDPATH= cd -- "$sibling" && pwd); fi

tarlink_bin=${TARLINK_BIN:-}
run_tarlink() {
  if [ -n "$tarlink_bin" ]; then
    "$tarlink_bin" "$@"
  elif command -v go >/dev/null 2>&1; then
    (cd "$repo_root" && go run ./cmd/tarlink "$@")
  else
    return 127
  fi
}
ledger_total=0; ledger_blocked=0; ledger_deferred=0; ledger_rejected=0
if [ -f "$ledger" ]; then
  ledger_total=$(awk '/^id:[[:space:]]*|^-[[:space:]]+id:[[:space:]]*/ {n++} END {print n+0}' "$ledger")
  ledger_blocked=$(awk '$1=="status:" && $2=="blocked" {n++} END {print n+0}' "$ledger")
  ledger_deferred=$(awk '$1=="status:" && $2=="deferred" {n++} END {print n+0}' "$ledger")
  ledger_rejected=$(awk '$1=="status:" && $2=="rejected" {n++} END {print n+0}' "$ledger")
fi
changed_json=''; blockers_json=''; changed_summary='unavailable'
if [ -n "$tarlink_bin" ] || command -v go >/dev/null 2>&1; then
  changed_json=$(run_tarlink registry candidates --changed --json 2>/dev/null || true)
  blockers_json=$(run_tarlink registry blockers --json 2>/dev/null || true)
  changed_summary=$(printf '%s' "$changed_json" | awk 'BEGIN{RS="}"} /"decision"[[:space:]]*:[[:space:]]*"RECHECK"/ {r++} /"decision"[[:space:]]*:[[:space:]]*"UNCHANGED"/ {u++} /"decision"[[:space:]]*:[[:space:]]*"ERROR"/ {e++} END {printf "RECHECK %d UNCHANGED %d ERROR %d",r+0,u+0,e+0}')
fi
latest=$(git -C "$repo_root" describe --tags --abbrev=0 2>/dev/null || printf unavailable)
schema=$(awk '/^schema:[[:space:]]*/ {print $2; exit}' "$repo_root/schema/manifest-v5.example.yaml" 2>/dev/null || printf unavailable)

if [ "$json" -eq 1 ]; then
  printf '{"tarlink":{"head":"%s","origin_main":"%s","branch":"%s","state":"%s","latest_release":"%s","manifest_schema":"%s"},' \
    "$(git_value "$repo_root" rev-parse HEAD)" "$(git_value "$repo_root" rev-parse origin/main)" "$(git_value "$repo_root" branch --show-current)" "$(git_state "$repo_root")" "$latest" "$schema"
  if [ -n "$registry_root" ]; then
    printf '"registry":{"head":"%s","origin_main":"%s","branch":"%s","state":"%s"},' \
      "$(git_value "$registry_root" rev-parse HEAD)" "$(git_value "$registry_root" rev-parse origin/main)" "$(git_value "$registry_root" branch --show-current)" "$(git_state "$registry_root")"
  else printf '"registry":{"state":"unavailable"},'; fi
  printf '"ledger":{"path":"%s","total":%s,"blocked":%s,"deferred":%s,"rejected":%s},"changed_summary":"%s"' \
    "$ledger" "$ledger_total" "$ledger_blocked" "$ledger_deferred" "$ledger_rejected" "$changed_summary"
  [ -n "$changed_json" ] && printf ',"changed_candidates":%s' "$changed_json"
  [ -n "$blockers_json" ] && printf ',"blockers":%s' "$blockers_json"
  printf '}\n'
  exit 0
fi
printf 'TarLink\n  HEAD: %s\n  origin/main: %s\n  branch: %s\n  working tree: %s\n  latest release/tag: %s\n  manifest schema: %s\n' \
  "$(git_value "$repo_root" rev-parse HEAD)" "$(git_value "$repo_root" rev-parse origin/main)" "$(git_value "$repo_root" branch --show-current)" "$(git_state "$repo_root")" "$latest" "$schema"
if [ -n "$registry_root" ]; then
  printf 'Registry\n  HEAD: %s\n  origin/main: %s\n  branch: %s\n  working tree: %s\n' \
    "$(git_value "$registry_root" rev-parse HEAD)" "$(git_value "$registry_root" rev-parse origin/main)" "$(git_value "$registry_root" branch --show-current)" "$(git_state "$registry_root")"
else printf 'Registry\n  checkout: unavailable\n'; fi
printf 'Candidate ledger\n  path: %s\n  total: %s\n  blocked: %s\n  deferred: %s\n  rejected: %s\nChanged candidates\n  %s\n' "$ledger" "$ledger_total" "$ledger_blocked" "$ledger_deferred" "$ledger_rejected" "$changed_summary"
if [ -n "$blockers_json" ]; then
  printf 'Top blockers\n'
  printf '%s' "$blockers_json" | awk 'match($0,/"blocker"[[:space:]]*:[[:space:]]*"[^"]+"[[:space:]]*,[[:space:]]*"count"[[:space:]]*:[[:space:]]*[0-9]+/) {x=substr($0,RSTART,RLENGTH); gsub(/.*"blocker"[[:space:]]*:[[:space:]]*"|"[[:space:]]*,.*/,"",x); printf "  %s\n",x}'
fi
