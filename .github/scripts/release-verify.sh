#!/usr/bin/env bash
#
# release-verify.sh — narrow deterministic verifier shared by release workflows.
#
# This file is sourced (not executed) by release workflow steps and unit-tested
# by tests/release_verify_test.sh. It performs no network access, no GitHub API
# calls, and never executes candidate binaries. Every check fails closed.
#
# Runtime: bash, jq, sha256sum, cmp — all present on ubuntu-24.04 runners.

# release_is_stable_tag TAG
# Succeeds only for canonical stable tags: vMAJOR.MINOR.PATCH with no leading
# zeros, no prerelease suffix, no build metadata.
release_is_stable_tag() {
  local tag=${1:-}
  [[ $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

# release_candidate_not_older_than_latest CANDIDATE LATEST
# Succeeds when CANDIDATE is a stable tag and is not older than LATEST.
# An empty LATEST (no current Latest release) succeeds. Fails closed when
# either tag is malformed.
release_candidate_not_older_than_latest() {
  local candidate=${1:-} latest=${2:-}
  local stable='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
  [[ $candidate =~ $stable ]] || {
    echo "candidate release tag is not a stable semantic version: $candidate" >&2
    return 1
  }
  local candidate_major=${BASH_REMATCH[1]}
  local candidate_minor=${BASH_REMATCH[2]}
  local candidate_patch=${BASH_REMATCH[3]}
  [[ -z $latest ]] && return 0
  [[ $latest =~ $stable ]] || {
    echo "existing GitHub Latest release has an unexpected tag: $latest" >&2
    return 1
  }
  local latest_major=${BASH_REMATCH[1]}
  local latest_minor=${BASH_REMATCH[2]}
  local latest_patch=${BASH_REMATCH[3]}
  local LC_ALL=C
  if release_decimal_less_than "$candidate_major" "$latest_major" ||
    { [[ $candidate_major == "$latest_major" ]] && release_decimal_less_than "$candidate_minor" "$latest_minor"; } ||
    { [[ $candidate_major == "$latest_major" && $candidate_minor == "$latest_minor" ]] && release_decimal_less_than "$candidate_patch" "$latest_patch"; }; then
    echo "candidate release $candidate is older than the current GitHub Latest release $latest" >&2
    return 1
  fi
}

# Decimal strings are canonical (no leading zeroes), so length then bytewise
# ordering avoids shell-integer overflow for large but syntactically valid
# semantic-version components.
release_decimal_less_than() {
  local left=${1:-} right=${2:-}
  if (( ${#left} < ${#right} )); then
    return 0
  fi
  if (( ${#left} > ${#right} )); then
    return 1
  fi
  [[ $left < $right ]]
}

# release_require_draft_tag RELEASE_JSON EXPECTED_TAG
# Succeeds only when the release record is still a draft for exactly the
# expected tag. Rejects public releases and tag mismatches.
release_require_draft_tag() {
  local release_json=${1:-} expected_tag=${2:-}
  local draft_state tag_name prerelease name
  draft_state=$(jq -r '.draft' "$release_json") || return 1
  [[ $draft_state == true ]] || {
    echo "refusing to touch a public release (expected draft for $expected_tag)" >&2
    return 1
  }
  tag_name=$(jq -r '.tag_name' "$release_json") || return 1
  [[ $tag_name == "$expected_tag" ]] || {
    echo "release tag mismatch: got $tag_name, want $expected_tag" >&2
    return 1
  }
  prerelease=$(jq -r '.prerelease' "$release_json") || return 1
  [[ $prerelease == false ]] || {
    echo "refusing to use a prerelease release for stable tag $expected_tag" >&2
    return 1
  }
  name=$(jq -r '.name' "$release_json") || return 1
  [[ $name == "TarLink $expected_tag" ]] || {
    echo "release title mismatch: got $name, want TarLink $expected_tag" >&2
    return 1
  }
}

# release_assert_exact_asset_names RELEASE_JSON
# Succeeds only when the release record carries exactly the three canonical
# asset names and nothing else.
release_assert_exact_asset_names() {
  local release_json=${1:-}
  local expected actual
  expected=$(printf '%s\n' checksums.txt tarlink-linux-amd64 tarlink-linux-arm64 | LC_ALL=C sort)
  actual=$(jq -er '.assets | map(.name) | sort | join("\n")' "$release_json") || {
    echo "release asset listing is unreadable" >&2
    return 1
  }
  [[ $actual == "$expected" ]] || {
    echo "unexpected release asset set:" >&2
    printf '%s\n' "$actual" >&2
    return 1
  }
}

# release_verify_checksums ASSET_DIR
# Verifies the SHA-256 manifest without executing any binary.
release_verify_checksums() {
  local asset_dir=${1:-}
  (cd "$asset_dir" && sha256sum --strict --check checksums.txt)
}

# release_compare_asset_bytes EXPECTED_DIR ACTUAL_DIR
# Byte-exact comparison of the three canonical assets. Used to prove a reused
# draft (or a bridge artifact) matches the assembled bytes even when an
# earlier same-run Actions artifact is unavailable (for example on rerun).
release_compare_asset_bytes() {
  local expected_dir=${1:-} actual_dir=${2:-}
  local name
  for name in checksums.txt tarlink-linux-amd64 tarlink-linux-arm64; do
    cmp -- "$expected_dir/$name" "$actual_dir/$name" || {
      echo "asset bytes differ: $name" >&2
      return 1
    }
  done
}

# release_count_matching_drafts PAGES_FILE TAG
# Prints the number of draft releases for TAG in a paginated `releases`
# listing captured with `gh api --paginate --slurp`.
release_count_matching_drafts() {
  local pages=${1:-} tag=${2:-}
  jq -r --arg tag "$tag" '[.[][] | select(.draft == true and .tag_name == $tag)] | length' "$pages"
}

# release_count_matching_any PAGES_FILE TAG
# Prints the number of releases (draft or public) for TAG. A non-zero count
# with zero drafts means a public release already owns the tag.
release_count_matching_any() {
  local pages=${1:-} tag=${2:-}
  jq -r --arg tag "$tag" '[.[][] | select(.tag_name == $tag)] | length' "$pages"
}

# release_draft_action DRAFT_COUNT TOTAL_RELEASE_COUNT
# Print create only for an absent tag and reuse only for one draft. A public
# release, a draft/public collision, multiple drafts, and malformed counts all
# fail closed rather than becoming a replace/overwrite path.
release_draft_action() {
  local drafts=${1:-} total=${2:-}
  if [[ $drafts == 0 && $total == 0 ]]; then
    printf '%s\n' create
  elif [[ $drafts == 1 && $total == 1 ]]; then
    printf '%s\n' reuse
  else
    echo "unexpected release state: drafts=$drafts releases=$total" >&2
    return 1
  fi
}

# release_parse_tag_sha LSREMOTE_FILE TAG
# Prints the commit SHA a tag ref resolves to, preferring the peeled
# `refs/tags/TAG^{}` line for annotated tags. Prints nothing when absent.
release_parse_tag_sha() {
  local lsremote=${1:-} tag=${2:-}
  awk -v tag="refs/tags/$tag" '
    $2 == tag "^{}" { print $1; found=1; exit }
    $2 == tag { direct=$1 }
    END { if (!found && direct != "") print direct }
  ' "$lsremote"
}
