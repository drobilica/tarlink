#!/usr/bin/env bash

set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

uses_line_valid() {
	local line=$1 target reference version
	[[ $line =~ ^[[:space:]]*uses:[[:space:]]*([^[:space:]#]+)([[:space:]]+#(.*))?$ ]] || return 1
	target=${BASH_REMATCH[1]}
	version=${BASH_REMATCH[3]:-}
	[[ $target == ./* ]] && return 0
	[[ $target == *@* ]] || return 1
	reference=${target##*@}
	[[ $reference =~ ^[0-9a-f]{40}$ ]] || return 1
	[[ $version =~ ^[[:space:]]*v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?[[:space:]]*$ ]]
}

failures=0
check_line() {
	local label=$1 expected=$2 line=$3
	if [[ $expected == valid ]]; then
		if ! uses_line_valid "$line"; then
			printf 'FAIL: rejected %s: %s\n' "$label" "$line" >&2
			failures=$((failures + 1))
		fi
	elif uses_line_valid "$line"; then
		printf 'FAIL: accepted %s: %s\n' "$label" "$line" >&2
		failures=$((failures + 1))
	fi
}

sha=0123456789abcdef0123456789abcdef01234567
check_line 'full SHA with version comment' valid "uses: actions/checkout@$sha # v7.0.1"
check_line 'repository-local action' valid 'uses: ./.github/actions/local'
check_line 'mutable major tag' invalid 'uses: actions/checkout@v7 # v7.0.1'
check_line 'mutable exact-version tag' invalid 'uses: actions/checkout@v7.0.1 # v7.0.1'
check_line 'short SHA' invalid 'uses: actions/checkout@0123456789abcdef0123456789abcdef0123456 # v7.0.1'
check_line 'malformed SHA' invalid 'uses: actions/checkout@0123456789abcdef0123456789abcdef0123456g # v7.0.1'
check_line 'missing version comment' invalid "uses: actions/checkout@$sha"
check_line 'unreadable version comment' invalid "uses: actions/checkout@$sha # checkout release"

shopt -s nullglob
workflows=("$repo_root"/.github/workflows/*.yml)
workflow_count=${#workflows[@]}
for workflow in "${workflows[@]}"; do
	line_number=0
	while IFS= read -r line || [[ -n $line ]]; do
		line_number=$((line_number + 1))
		if [[ $line =~ ^[[:space:]]*uses: ]]; then
			if ! uses_line_valid "$line"; then
				printf '%s:%d: external action must use a full 40-character SHA and same-line version comment: %s\n' \
					"${workflow#"$repo_root/"}" "$line_number" "$line" >&2
				failures=$((failures + 1))
			fi
		fi
	done <"$workflow"
done

dependabot=$repo_root/.github/dependabot.yml
[[ -f $dependabot ]] || { printf '%s\n' 'missing .github/dependabot.yml' >&2; exit 1; }
dependabot_text=$(<"$dependabot")
[[ $dependabot_text =~ package-ecosystem:[[:space:]]*github-actions ]] || { printf '%s\n' 'Dependabot must update GitHub Actions' >&2; failures=$((failures + 1)); }
[[ $dependabot_text =~ directory:[[:space:]]*/ ]] || { printf '%s\n' 'Dependabot GitHub Actions directory must be /' >&2; failures=$((failures + 1)); }
[[ $dependabot_text =~ interval:[[:space:]]*weekly ]] || { printf '%s\n' 'Dependabot GitHub Actions updates must be weekly' >&2; failures=$((failures + 1)); }
[[ ! $dependabot_text =~ package-ecosystem:[[:space:]]*(docker|gomod) ]] || { printf '%s\n' 'unrequested Dependabot ecosystem configured' >&2; failures=$((failures + 1)); }

if (( failures > 0 )); then
	printf 'Actions pin tests failed: %d issue(s)\n' "$failures" >&2
	exit 1
fi
printf 'Actions pin tests passed (%d workflow files)\n' "$workflow_count"
