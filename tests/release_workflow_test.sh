#!/usr/bin/env bash

set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
release_workflow=$script_dir/.github/workflows/release.yml
recovery_workflow=$script_dir/.github/workflows/release-recovery.yml
registry_commit=$script_dir/.github/registry-commit
test -f "$release_workflow"
test -f "$recovery_workflow"
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
test "$(grep -Fc 'for attempt in $(seq 1 15)' "$release_workflow")" -ge 2
test "$(grep -Fc 'sleep 2' "$release_workflow")" -ge 2
test "$(grep -Fc "gh api --header 'Accept: application/octet-stream' \"\$asset_url\"" "$release_workflow")" -ge 2
test "$(grep -Fc './tests/release_artifacts_test.sh' "$release_workflow")" -ge 3
test "$(grep -Fc 'registry validate "$PWD/registry"' "$release_workflow")" -ge 2
test "$(grep -Fc './tests/install_test.sh' "$release_workflow")" -ge 2
remote_verify_job=$(awk '
  /^  remote-verify:$/ { in_job=1 }
  in_job && /^  publish:$/ { exit }
  in_job { print }
' "$release_workflow")
printf '%s\n' "$remote_verify_job" | grep -E '^    permissions:$' >/dev/null
printf '%s\n' "$remote_verify_job" | grep -E '^      contents: read$' >/dev/null
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

grep -E '^  workflow_dispatch:$' "$recovery_workflow" >/dev/null
grep -F 'release_tag:' "$recovery_workflow" >/dev/null
grep -F 'expected_sha:' "$recovery_workflow" >/dev/null
grep -F 'source_run_id:' "$recovery_workflow" >/dev/null
grep -F 'ref: main' "$recovery_workflow" >/dev/null
grep -F 'git ls-remote origin refs/heads/main' "$recovery_workflow" >/dev/null
grep -F 'git ls-remote origin "refs/tags/$RELEASE_TAG"' "$recovery_workflow" >/dev/null
grep -F 'gh api --header '\''Accept: application/octet-stream'\'' "$asset_url"' "$recovery_workflow" >/dev/null
test "$(grep -Fc 'gh api --paginate --slurp "repos/$GITHUB_REPOSITORY/releases?per_page=100"' "$recovery_workflow")" -eq 2
if grep -F 'releases/tags/$RELEASE_TAG' "$recovery_workflow" >/dev/null; then
	printf '%s\n' 'draft recovery must not use the public tag-release endpoint' >&2
	exit 1
fi
grep -F 'sha256sum --strict --check checksums.txt' "$recovery_workflow" >/dev/null
grep -F 'gh run download "$SOURCE_RUN_ID"' "$recovery_workflow" >/dev/null
grep -F 'actions/runs/$SOURCE_RUN_ID/jobs' "$recovery_workflow" >/dev/null
jobs_selector='[.[].jobs[] | select(.name == $name and .conclusion == "success")] | length >= 1'
grep -F "$jobs_selector" "$recovery_workflow" >/dev/null
jobs_fixture='[{"total_count":2,"jobs":[{"name":"Build amd64","conclusion":"success"},{"name":"Build arm64","conclusion":"failure"}]},{"total_count":2,"jobs":[{"name":"Build arm64","conclusion":"success"}]}]'
printf '%s\n' "$jobs_fixture" | jq -e --arg name 'Build amd64' "$jobs_selector" >/dev/null
printf '%s\n' "$jobs_fixture" | jq -e --arg name 'Build arm64' "$jobs_selector" >/dev/null
if printf '%s\n' "$jobs_fixture" | jq -e --arg name 'Test tagged source' "$jobs_selector" >/dev/null; then
	printf '%s\n' 'source-run job selector accepted a missing successful job' >&2
	exit 1
fi
grep -F 'head_branch' "$recovery_workflow" >/dev/null
grep -F 'cmp -- "source-assets/$name" "remote/$name"' "$recovery_workflow" >/dev/null
grep -F 'ubuntu-24.04-arm' "$recovery_workflow" >/dev/null
grep -F 'needs.prepare.outputs.registry_sha' "$recovery_workflow" >/dev/null
grep -F 'release_artifacts_test.sh remote' "$recovery_workflow" >/dev/null
grep -F './tests/install_test.sh' "$recovery_workflow" >/dev/null
grep -F 'gh release edit "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" --draft=false --latest' "$recovery_workflow" >/dev/null
grep -F '[[ "$latest_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]' "$recovery_workflow" >/dev/null
grep -F '[[ "$RELEASE_TAG" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]' "$recovery_workflow" >/dev/null
stable_tag_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
[[ v0.18.2 =~ $stable_tag_pattern ]]
for release_test_tag in v01.2.3 v1.02.3 v1.2.03 v1.2.3-rc1; do
	if [[ $release_test_tag =~ $stable_tag_pattern ]]; then
		printf '%s\n' "recovery accepted noncanonical tag $release_test_tag" >&2
		exit 1
	fi
done
grep -F 'contents: read' "$recovery_workflow" >/dev/null
grep -F 'contents: write' "$recovery_workflow" >/dev/null
grep -F 'group: tarlink-release' "$recovery_workflow" >/dev/null
recovery_prepare=$(awk '/^  prepare:$/ { in_job=1 } in_job && /^  validate:$/ { exit } in_job { print }' "$recovery_workflow")
recovery_validate=$(awk '/^  validate:$/ { in_job=1 } in_job && /^  publish:$/ { exit } in_job { print }' "$recovery_workflow")
recovery_publish=$(awk '/^  publish:$/ { in_job=1 } in_job { print }' "$recovery_workflow")
printf '%s\n' "$recovery_prepare" | grep -F 'contents: write' >/dev/null
printf '%s\n' "$recovery_validate" | grep -F 'contents: read' >/dev/null
printf '%s\n' "$recovery_publish" | grep -F 'contents: write' >/dev/null
if printf '%s\n%s\n' "$recovery_prepare" "$recovery_publish" | grep -E 'release_artifacts_test\.sh|tests/install_test\.sh|registry validate' >/dev/null; then
	printf '%s\n' 'release-write jobs must not execute release binaries or installer tests' >&2
	exit 1
fi
if grep -F 'gh release create' "$recovery_workflow" >/dev/null || grep -F 'gh release upload' "$recovery_workflow" >/dev/null; then
	printf '%s\n' 'recovery workflow must not create or replace a release' >&2
	exit 1
fi

printf '%s\n' 'release workflow tests passed'
