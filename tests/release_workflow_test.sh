#!/usr/bin/env bash

set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
release_workflow=$repo_root/.github/workflows/release.yml
recovery_workflow=$repo_root/.github/workflows/release-recovery.yml
image_workflow=$repo_root/.github/workflows/artifact-repository-image.yml

fail() {
	printf 'release workflow contract failed: %s\n' "$1" >&2
	exit 1
}

job_block() {
	local workflow=$1 job=$2
	awk -v job="$job" '
		$0 == "  " job ":" { in_job=1; next }
		in_job && /^  [a-zA-Z0-9_-]+:$/ { exit }
		in_job { print }
	' "$workflow"
}

has() {
	local text=$1 pattern=$2
	[[ $text =~ $pattern ]]
}

release=$(<"$release_workflow")
recovery=$(<"$recovery_workflow")

# Stable tag entry, global serialization, and an immutable triggering commit.
trigger_pattern=$(awk '/^  push:$/ { getline; if ($0 == "    tags:") { getline; print; exit } }' "$release_workflow")
[[ $trigger_pattern == "      - 'v[0-9]*.[0-9]*.[0-9]*'" ]] || fail 'normal release must trigger only version-shaped tags'
has "$release" 'group: tarlink-release' || fail 'normal release must use shared global concurrency'
has "$recovery" 'group: tarlink-release' || fail 'recovery must share global release concurrency'
has "$release" 'contents: read' || fail 'normal release must default to read-only contents permission'
has "$release" 'ref: \$\{\{ github\.sha \}\}' || fail 'release source must check out the exact triggering commit'
! has "$release" 'ref: \$\{\{ github\.ref \}\}' || fail 'release must not check out a mutable ref'

# Registry branch is resolved exactly once and consumers use the run-local SHA.
[[ $(awk '/^[[:space:]]+ref: main$/ { count++ } END { print count+0 }' "$release_workflow") == 1 ]] || fail 'registry main must be resolved once'
has "$release" 'needs\.registry-snapshot\.outputs\.sha' || fail 'registry consumers must use the snapshotted SHA'
has "$release" 'actual_sha=.*rev-parse HEAD' || fail 'registry snapshot must record the resolved commit'

# Stage writes the draft only after assembly, and hands verified remote bytes to
# read-only architecture checks. The write jobs never execute released code.
stage=$(job_block "$release_workflow" stage)
validate=$(job_block "$release_workflow" validate)
publish=$(job_block "$release_workflow" publish)
test_job=$(job_block "$release_workflow" test)
assemble=$(job_block "$release_workflow" assemble)
release_notes=$(job_block "$release_workflow" release-notes)
for read_job in build test assemble registry-snapshot; do
	if has "$(job_block "$release_workflow" "$read_job")" 'contents: write'; then
		fail "$read_job must not have release-write permission"
	fi
done
has "$test_job" './scripts/validate\.sh' || fail 'tagged source must pass canonical validation before staging'
has "$assemble" 'needs: \[build, test\]' || fail 'asset assembly must wait for builds and source validation'
has "$release_notes" 'contents: read' || fail 'release notes must use read-only permissions'
has "$release_notes" 'issues: read' || fail 'release notes require read-only issues access'
has "$release_notes" 'pull-requests: read' || fail 'release notes require read-only pull-request access'
has "$release_notes" 'generate-release-notes\.sh' || fail 'release notes must be generated outside release-write jobs'
has "$stage" 'needs: \[assemble, release-notes\]' || fail 'draft staging must consume assembled assets and release notes'
has "$stage" 'contents: write' || fail 'draft staging needs contents:write'
! has "$stage" 'generate-release-notes|scripts/release_artifacts_test|tests/install_test|registry validate' || fail 'draft staging must not execute release-note or release-test scripts'
has "$stage" 'name: release-bridge' || fail 'staging must publish verified remote bytes as an artifact'
has "$stage" 'gh api --header' || fail 'staging must retrieve draft asset bytes from GitHub'
has "$stage" 'release_compare_asset_bytes' || fail 'staging must compare remote bytes with assembled bytes'
has "$stage" 'release_verify_checksums' || fail 'staging must verify checksums'
! has "$stage" 'release_artifacts_test|install_test|registry validate|tarlink-linux-(amd64|arm64)[[:space:]]+registry' || fail 'write-scoped staging must not execute candidate code'
! has "$stage" '(^|[[:space:]])"?((dist|remote)/)?tarlink-linux-(amd64|arm64)"?[[:space:]]+(version|registry|install|uninstall)' || fail 'write-scoped staging must not invoke release binaries'
has "$validate" 'needs: \[stage, registry-snapshot\]' || fail 'architecture validation must depend on staging and registry snapshot'
has "$validate" 'contents: read' || fail 'architecture validation must be read-only'
! has "$validate" 'contents: write' || fail 'architecture validation must not write releases'
has "$validate" 'name: release-bridge' || fail 'architecture checks must consume the verified bridge artifact'
! has "$validate" 'gh api|gh release' || fail 'architecture checks must not independently discover the draft'
has "$validate" 'release_artifacts_test' || fail 'architecture checks must validate release artifact contracts'
has "$validate" 'registry validate' || fail 'architecture checks must validate the official registry'
has "$validate" 'tests/install_test\.sh' || fail 'architecture checks must exercise installer and uninstaller'
has "$publish" 'needs: \[validate\]' || fail 'publication must wait for architecture validation'
has "$publish" 'contents: write' || fail 'publication needs contents:write'
has "$publish" 'name: release-bridge' || fail 'publication must use the exact bridge bytes'
has "$publish" 'release_parse_tag_sha' || fail 'publication must recheck authoritative tag identity'
has "$publish" 'release_compare_asset_bytes' || fail 'publication must recheck exact remote bytes'
has "$publish" 'release_candidate_not_older_than_latest' || fail 'publication must guard GitHub Latest ordering'
! has "$publish" 'release_artifacts_test|install_test|registry validate' || fail 'publication must not execute candidate code'
! has "$publish" '(^|[[:space:]])"?((dist|remote|expected-assets)/)?tarlink-linux-(amd64|arm64)"?[[:space:]]+(version|registry|install|uninstall)' || fail 'publication must not invoke release binaries'

# Recovery stays explicit, does not create or replace releases, and preserves
# the same read-only architecture validation boundary.
has "$recovery" 'workflow_dispatch:' || fail 'recovery must remain manually dispatched'
recovery_validate=$(job_block "$recovery_workflow" validate)
recovery_publish=$(job_block "$recovery_workflow" publish)
recovery_prepare=$(job_block "$recovery_workflow" prepare)
has "$recovery_validate" 'contents: read' || fail 'recovery architecture validation must be read-only'
has "$recovery_prepare" 'contents: write' || fail 'recovery draft inspection must have narrow write permission'
has "$recovery_publish" 'contents: write' || fail 'recovery publication must retain narrow release-write scope'
! has "$recovery_prepare" 'release_artifacts_test|install_test|registry validate' || fail 'recovery preparation must not execute candidate code'
! has "$recovery_publish" 'release_artifacts_test|install_test|registry validate' || fail 'recovery publication must not execute candidate code'
! has "$recovery_prepare" '(^|[[:space:]])"?remote/tarlink-linux-(amd64|arm64)"?[[:space:]]+(version|registry|install|uninstall)' || fail 'recovery preparation must not invoke release binaries'
! has "$recovery_publish" '(^|[[:space:]])"?remote/tarlink-linux-(amd64|arm64)"?[[:space:]]+(version|registry|install|uninstall)' || fail 'recovery publication must not invoke release binaries'
! has "$recovery" 'gh release create|gh release upload' || fail 'recovery must not create or replace releases'

# Package-write credentials are isolated to image publication, after source
# validation succeeds. Other registry image publishing has no validation step.
image_validate=$(job_block "$image_workflow" validate)
image_publish=$(job_block "$image_workflow" publish)
has "$image_validate" 'contents: read' || fail 'image validation must have contents:read'
! has "$image_validate" 'packages: write' || fail 'image validation must not hold package-write credentials'
has "$image_publish" 'needs: validate' || fail 'image publication must wait for canonical validation'
has "$image_publish" 'packages: write' || fail 'image publication must retain package-write permission'
has "$image_publish" 'persist-credentials: false' || fail 'image publication must not persist its write token in the Docker context'
has "$(<"$repo_root/.dockerignore")" '^\.git$' || fail 'Docker build context must exclude .git'
has "$(<"$image_workflow")" '\.dockerignore' || fail 'Docker context exclusion must trigger image publication'

printf '%s\n' 'release workflow contract tests passed'
