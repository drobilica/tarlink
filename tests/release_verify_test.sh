#!/usr/bin/env bash
#
# Fixture tests for .github/scripts/release-verify.sh: release-state cases
# (absent/create decision, exact draft, incomplete, wrong set, digest and byte
# mismatch, multiple, public), tag exact/wrong, stable canonical/malformed,
# and older-version decisions. No network access.

set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# shellcheck source=../.github/scripts/release-verify.sh
source "$script_dir/.github/scripts/release-verify.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

failures=0
check() {
  local description=$1
  shift
  if "$@" >/dev/null 2>"$tmp/stderr"; then
    printf 'ok: %s\n' "$description"
  else
    printf 'FAIL: %s\n' "$description"
    cat "$tmp/stderr" >&2
    failures=$((failures + 1))
  fi
}
check_fails() {
  local description=$1
  shift
  if "$@" >/dev/null 2>"$tmp/stderr"; then
    printf 'FAIL (expected rejection): %s\n' "$description"
    failures=$((failures + 1))
  else
    printf 'ok (rejected): %s\n' "$description"
  fi
}

# Stable canonical tags pass; malformed tags fail.
for good_tag in v0.18.2 v0.0.0 v1.2.3 v10.20.30; do
  check "stable tag accepted: $good_tag" release_is_stable_tag "$good_tag"
done
for bad_tag in v01.2.3 v1.02.3 v1.2.03 v1.2.3-rc1 v1.2.3+build v1.2 1.2.3 latest v V1.2.3 '' 'v1.2.3 '; do
  check_fails "malformed tag rejected: [$bad_tag]" release_is_stable_tag "$bad_tag"
done

# Tag resolution fixtures: annotated tags prefer the peeled line.
printf 'abc123\treps/tags/bogus\n' >"$tmp/lsremote-noise.txt"
printf '%s\n' '1111111111111111111111111111111111111111	refs/tags/v0.18.2' >"$tmp/lsremote-light.txt"
check "lightweight tag resolves exactly" test "$(release_parse_tag_sha "$tmp/lsremote-light.txt" v0.18.2)" = 1111111111111111111111111111111111111111
printf '%s\n' \
  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa	refs/tags/v0.18.2' \
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb	refs/tags/v0.18.2^{}' >"$tmp/lsremote-annotated.txt"
check "annotated tag prefers peeled commit" test "$(release_parse_tag_sha "$tmp/lsremote-annotated.txt" v0.18.2)" = bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
check "wrong tag resolves to nothing" test -z "$(release_parse_tag_sha "$tmp/lsremote-annotated.txt" v0.18.3)"

# Release-listing fixtures shaped like `gh api --paginate --slurp`.
exact_release() {
  jq -n --arg tag "${1:-v0.18.2}" --argjson draft "${2:-true}" '{
    id: 1, tag_name: $tag, name: ("TarLink " + $tag), draft: $draft,
    prerelease: false,
    assets: [
      {name: "checksums.txt", url: "https://api.example/a"},
      {name: "tarlink-linux-amd64", url: "https://api.example/b"},
      {name: "tarlink-linux-arm64", url: "https://api.example/c"}
    ]
  }'
}
exact_release >"$tmp/release-exact.json"
# `gh api --paginate --slurp` yields an array of pages, each page an array of
# releases, hence the double-nested fixture shape.
pages_of() {
  jq -s '[.]' "$@"
}
exact_release | pages_of >"$tmp/pages-exact.json"
jq -n '[[]]' >"$tmp/pages-empty.json"

# No draft: create decision (zero drafts, zero total).
check "absent tag counts zero drafts" test "$(release_count_matching_drafts "$tmp/pages-empty.json" v0.18.2)" = 0
check "absent tag counts zero total" test "$(release_count_matching_any "$tmp/pages-empty.json" v0.18.2)" = 0
check "absent tag selects create" test "$(release_draft_action 0 0)" = create

# Exact draft: accepted.
check "exact draft counts one" test "$(release_count_matching_drafts "$tmp/pages-exact.json" v0.18.2)" = 1
check "exact draft selects reuse" test "$(release_draft_action 1 1)" = reuse
check "exact draft tag accepted" release_require_draft_tag "$tmp/release-exact.json" v0.18.2
check "exact draft asset set accepted" release_assert_exact_asset_names "$tmp/release-exact.json"

# Wrong tag on the release record is rejected.
check_fails "wrong release tag rejected" release_require_draft_tag "$tmp/release-exact.json" v0.18.3
check "unrelated tag counts zero drafts" test "$(release_count_matching_drafts "$tmp/pages-exact.json" v0.18.3)" = 0
jq '.name = "Unexpected title"' "$tmp/release-exact.json" >"$tmp/release-bad-title.json"
check_fails "unexpected title metadata rejected" release_require_draft_tag "$tmp/release-bad-title.json" v0.18.2
jq '.prerelease = true' "$tmp/release-exact.json" >"$tmp/release-prerelease.json"
check_fails "prerelease metadata rejected" release_require_draft_tag "$tmp/release-prerelease.json" v0.18.2

# Public release owning the tag is rejected, and visible via the any-count.
exact_release v0.18.2 false >"$tmp/release-public.json"
exact_release v0.18.2 false | pages_of >"$tmp/pages-public.json"
check_fails "public release rejected" release_require_draft_tag "$tmp/release-public.json" v0.18.2
check "public tag counts zero drafts" test "$(release_count_matching_drafts "$tmp/pages-public.json" v0.18.2)" = 0
check "public tag counts one total" test "$(release_count_matching_any "$tmp/pages-public.json" v0.18.2)" = 1
check_fails "public release never selects create or reuse" release_draft_action 0 1

# Multiple drafts for one tag fail closed.
jq -n --slurpfile release "$tmp/release-exact.json" '[[$release[0], $release[0]]]' >"$tmp/pages-multiple.json"
check "multiple drafts counted" test "$(release_count_matching_drafts "$tmp/pages-multiple.json" v0.18.2)" = 2
check "multiple releases counted" test "$(release_count_matching_any "$tmp/pages-multiple.json" v0.18.2)" = 2
check_fails "multiple drafts fail closed" release_draft_action 2 2
check_fails "draft/public collision fails closed" release_draft_action 1 2

# Incomplete asset set (missing checksums) is rejected.
jq '.assets |= map(select(.name != "checksums.txt"))' "$tmp/release-exact.json" >"$tmp/release-incomplete.json"
check_fails "incomplete asset set rejected" release_assert_exact_asset_names "$tmp/release-incomplete.json"

# Wrong asset set (unexpected extra name) is rejected.
jq '.assets += [{name: "install.sh", url: "https://api.example/d"}]' "$tmp/release-exact.json" >"$tmp/release-wrongset.json"
check_fails "wrong asset set rejected" release_assert_exact_asset_names "$tmp/release-wrongset.json"

# Digest mismatch: tampered bytes fail the manifest check.
mkdir -p "$tmp/assembled" "$tmp/remote-good" "$tmp/remote-tampered"
printf 'binary-amd64-bytes' >"$tmp/assembled/tarlink-linux-amd64"
printf 'binary-arm64-bytes' >"$tmp/assembled/tarlink-linux-arm64"
(cd "$tmp/assembled" && sha256sum tarlink-linux-amd64 tarlink-linux-arm64 >checksums.txt)
cp "$tmp/assembled/tarlink-linux-amd64" "$tmp/assembled/tarlink-linux-arm64" "$tmp/assembled/checksums.txt" "$tmp/remote-good/"
cp "$tmp/assembled/tarlink-linux-amd64" "$tmp/assembled/tarlink-linux-arm64" "$tmp/assembled/checksums.txt" "$tmp/remote-tampered/"
printf 'tampered' >"$tmp/remote-tampered/tarlink-linux-amd64"
check "exact bytes accepted" release_compare_asset_bytes "$tmp/assembled" "$tmp/remote-good"
check "exact digests accepted" release_verify_checksums "$tmp/remote-good"
check_fails "digest mismatch rejected" release_verify_checksums "$tmp/remote-tampered"
check_fails "byte mismatch rejected" release_compare_asset_bytes "$tmp/assembled" "$tmp/remote-tampered"

# Version ordering: equal and newer pass, older and malformed fail.
check "equal version accepted" release_candidate_not_older_than_latest v0.18.2 v0.18.2
check "newer patch accepted" release_candidate_not_older_than_latest v0.18.3 v0.18.2
check "newer minor accepted" release_candidate_not_older_than_latest v0.19.0 v0.18.9
check "newer major accepted" release_candidate_not_older_than_latest v1.0.0 v0.99.99
check "empty latest accepted" release_candidate_not_older_than_latest v0.18.2 ''
check_fails "numeric order rejects older minor despite lexical order" release_candidate_not_older_than_latest v0.9.0 v0.18.2
check "large semver components compare without overflow" release_candidate_not_older_than_latest v999999999999999999999999.0.0 v999999999999999999999998.99.99
check_fails "older patch rejected" release_candidate_not_older_than_latest v0.18.1 v0.18.2
check_fails "older minor rejected" release_candidate_not_older_than_latest v0.17.9 v0.18.0
check_fails "older major rejected" release_candidate_not_older_than_latest v0.99.99 v1.0.0
check_fails "malformed candidate rejected" release_candidate_not_older_than_latest v1.2.3-rc1 v1.2.2
check_fails "malformed latest rejected" release_candidate_not_older_than_latest v1.2.3 latest

if ((failures > 0)); then
  printf 'release verifier tests failed: %d\n' "$failures" >&2
  exit 1
fi
printf 'release verifier tests passed\n'
