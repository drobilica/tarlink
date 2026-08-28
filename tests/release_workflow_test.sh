#!/usr/bin/env bash

set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
release_workflow=$script_dir/.github/workflows/release.yml
registry_commit=$script_dir/.github/registry-commit
test -f "$release_workflow"
test ! -e "$registry_commit"

grep -E '^  push:$' "$release_workflow" >/dev/null
grep -F 'tags:' "$release_workflow" >/dev/null
grep -F 'gh release create "$RELEASE_TAG"' "$release_workflow" >/dev/null
grep -F -- '--draft --verify-tag' "$release_workflow" >/dev/null
grep -F 'gh release upload "$RELEASE_TAG"' "$release_workflow" >/dev/null
grep -F 'gh release edit "$RELEASE_TAG"' "$release_workflow" >/dev/null
grep -F -- '--draft=false --latest' "$release_workflow" >/dev/null
grep -F 'group: tarlink-release' "$release_workflow" >/dev/null
if grep -F 'group: release-${{ github.ref }}' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflows must share one global concurrency group' >&2
	exit 1
fi
grep -F 'gh release list --repo "$GITHUB_REPOSITORY" --exclude-drafts --exclude-pre-releases --json isLatest,tagName' "$release_workflow" >/dev/null
grep -F 'expected exactly one GitHub Latest release' "$release_workflow" >/dev/null
grep -F 'candidate release $RELEASE_TAG is older than the current GitHub Latest release $latest_tag' "$release_workflow" >/dev/null
grep -F 'existing GitHub Latest release has an unexpected tag' "$release_workflow" >/dev/null
grep -F 'ref: ${{ github.sha }}' "$release_workflow" >/dev/null
if grep -F 'ref: ${{ github.ref }}' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflow checks out a mutable ref' >&2
	exit 1
fi
if [ "$(grep -Ec '^[[:space:]]+ref: main$' "$release_workflow")" -ne 1 ]; then
	printf '%s\n' 'release workflow must resolve registry main exactly once' >&2
	exit 1
fi
grep -F 'actual_sha=$(git -C registry rev-parse HEAD)' "$release_workflow" >/dev/null
grep -F '[[ ! "$actual_sha" =~ ^[0-9a-f]{40}$ ]]' "$release_workflow" >/dev/null
grep -F 'echo "sha=$actual_sha" >> "$GITHUB_OUTPUT"' "$release_workflow" >/dev/null
test "$(grep -Fc 'ref: ${{ needs.registry-snapshot.outputs.sha }}' "$release_workflow")" -ge 2
grep -F 'stable vMAJOR.MINOR.PATCH' "$release_workflow" >/dev/null
if grep -F 'PRERELEASE' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflow permits prerelease tags' >&2
	exit 1
fi
grep -F 'chmod 0755 dist/tarlink-linux-amd64 dist/tarlink-linux-arm64' "$release_workflow" >/dev/null
grep -F 'chmod 0755 release-assets/tarlink-linux-amd64 release-assets/tarlink-linux-arm64' "$release_workflow" >/dev/null
grep -F 'chmod 0755 remote/tarlink-linux-amd64 remote/tarlink-linux-arm64' "$release_workflow" >/dev/null
test "$(grep -Fc 'git ls-remote origin' "$release_workflow")" -ge 2
grep -F 'EXPECTED_SHA: ${{ github.sha }}' "$release_workflow" >/dev/null
test "$(grep -Fc 'cmp -- "release-assets/$name" "remote/$name"' "$release_workflow")" -ge 2
test "$(grep -Fc 'gh api --paginate --slurp "repos/$GITHUB_REPOSITORY/releases?per_page=100"' "$release_workflow")" -ge 2
test "$(grep -Fc "gh api --header 'Accept: application/octet-stream' \"\$asset_url\"" "$release_workflow")" -ge 2
remote_verify_job=$(awk '
  /^  remote-verify:$/ { in_job=1 }
  in_job && /^  publish:$/ { exit }
  in_job { print }
' "$release_workflow")
printf '%s\n' "$remote_verify_job" | grep -E '^    permissions:$' >/dev/null
printf '%s\n' "$remote_verify_job" | grep -E '^      contents: write$' >/dev/null
if grep -F 'gh release download "$RELEASE_TAG"' "$release_workflow" >/dev/null; then
	printf '%s\n' 'draft assets must be downloaded through the authenticated asset API' >&2
	exit 1
fi
if grep -E '^  release:|types:[[:space:]]*\[published\]' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflow must not run from a published-release event' >&2
	exit 1
fi
if grep -F -- '--clobber' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflow must not overwrite existing release assets' >&2
	exit 1
fi
if grep -E 'gh release (upload|create).*(install\.sh|uninstall\.sh|\.tar\.(gz|xz))' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflow publishes a forbidden packaged or shell asset' >&2
	exit 1
fi

printf '%s\n' 'release workflow tests passed'
