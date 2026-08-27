#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 REPOSITORY TAG TARGET_SHA OUTPUT_FILE" >&2
  exit 2
}

(( $# == 4 )) || usage
repo=$1
tag=$2
target_sha=$3
output=$4

semver() {
  [[ $1 =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || return 1
  printf '%s\t%s\t%s\t%s\n' "$((10#${BASH_REMATCH[1]}))" "$((10#${BASH_REMATCH[2]}))" "$((10#${BASH_REMATCH[3]}))" "$1"
}

releases=$(gh release list --repo "$repo" --exclude-drafts --exclude-pre-releases --json tagName,publishedAt --limit 1000)
current=$(semver "$tag") || { echo "release tag is not stable semver: $tag" >&2; exit 1; }
IFS=$'\t' read -r current_major current_minor current_patch _ <<<"$current"

previous_record=$(jq -c --arg tag "$tag" --argjson major "$current_major" --argjson minor "$current_minor" --argjson patch "$current_patch" '
  [.[] | select(.tagName != $tag and .publishedAt != null and (.tagName | test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))) | . as $r |
   ($r.tagName | capture("^v(?<major>[0-9]+)\\.(?<minor>[0-9]+)\\.(?<patch>[0-9]+)$")) as $v |
   select(($v.major|tonumber) < $major or (($v.major|tonumber) == $major and ($v.minor|tonumber) < $minor) or (($v.major|tonumber) == $major and ($v.minor|tonumber) == $minor and ($v.patch|tonumber) < $patch)) |
   {tag: $r.tagName, publishedAt: $r.publishedAt, version: [($v.major|tonumber),($v.minor|tonumber),($v.patch|tonumber)]}]
  | sort_by(.version) | last // {}
' <<<"$releases")
previous_tag=$(jq -r '.tag // empty' <<<"$previous_record")
previous_date=$(jq -r '.publishedAt // empty' <<<"$previous_record")
published_date=$(jq -r --arg tag "$tag" '.[] | select(.tagName == $tag) | .publishedAt // empty' <<<"$releases")

notes_args=(--method POST "repos/$repo/releases/generate-notes" -f "tag_name=$tag" -f "target_commitish=$target_sha")
if [[ -n $previous_tag ]]; then
  notes_args+=(-f "previous_tag_name=$previous_tag")
fi
generated=$(gh api "${notes_args[@]}" --jq .body)
generated=$(printf '%s\n' "$generated" | sed '/^\*\*Full Changelog\*\*:/d')

compare_base=$previous_tag

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
compare_json="$tmp/compare.json"
if [[ -n $compare_base ]]; then
  gh api --paginate --slurp "repos/$repo/compare/$compare_base...$tag" >"$compare_json"
else
  gh api --paginate --slurp "repos/$repo/commits?sha=$tag&per_page=100" >"$compare_json"
fi

target_date=$(gh api "repos/$repo/commits/$target_sha" --jq '.commit.committer.date // .commit.author.date')
[[ -n $published_date ]] && target_date=$published_date

{
  printf '# TarLink %s\n\n' "$tag"
  printf '## Highlights\n\n'
  printf '%s\n\n' 'The categorized changes below summarize user-visible work since the previous release.'
  printf '%s\n\n' "$generated"

  direct_file="$tmp/direct.tsv"
  direct_count=0
  breaking_count=0
  while IFS=$'\t' read -r sha subject author; do
    pulls=$(gh api "repos/$repo/commits/$sha/pulls" --jq 'length')
    if [[ $pulls == 0 ]]; then
      case "$subject" in
        feat\(*\):*|feat:*) category=Added ;;
        fix\(*\):*|fix:*) category=Fixed ;;
        *BREAKING*|*breaking*) category=Breaking ;;
        refactor\(*\):*|refactor:*) category=Changed ;;
        *) continue ;;
      esac
      description=${subject#*: }
      printf '%s\t%s by @%s ([%s](https://github.com/%s/commit/%s))\n' "$category" "$description" "$author" "${sha:0:7}" "$repo" "$sha" >>"$direct_file"
      direct_count=$((direct_count + 1))
      [[ $category == Breaking ]] && breaking_count=$((breaking_count + 1))
    fi
  done < <(jq -r '[.. | objects | select((.sha? != null) and (.commit? != null))] | unique_by(.sha) | .[] | [.sha, (.commit.message | split("\n")[0]), (.author.login // .commit.author.name // "unknown")] | @tsv' "$compare_json")
  if (( direct_count == 0 )); then
    printf '_No direct-to-main changes without a pull request._\n\n'
  else
    for category in Added Changed Fixed Breaking; do
      case "$category" in
        Breaking) heading='Removed / Breaking Changes' ;;
        *) heading=$category ;;
      esac
      entries=$(awk -F '\t' -v category="$category" '$1 == category {sub("^[^\\t]*\\t", ""); print "- " $0}' "$direct_file")
      if [[ -n $entries ]]; then
        printf '## %s\n\n%s\n\n' "$heading" "$entries"
      fi
    done
  fi
  printf '## Closed Issues\n\n'
  issue_count=0
  issue_endpoint="repos/$repo/issues?state=closed&sort=closed&direction=asc&per_page=100"
  issue_file="$tmp/issues.tsv"
  issue_json="$tmp/issues.json"
  gh api --paginate --slurp "$issue_endpoint" >"$issue_json"
  jq -r --arg since "$previous_date" --arg until "$target_date" '
    .[][]? | select(.pull_request == null and .closed_at != null and
      ($since == "" or .closed_at > $since) and .closed_at <= $until) |
      [.number, .title, (.user.login // "unknown")] | @tsv' "$issue_json" >"$issue_file"
  while IFS=$'\t' read -r number title author; do
    printf '%s\n' "- [#$number](https://github.com/$repo/issues/$number) $title by @$author"
    issue_count=$((issue_count + 1))
  done <"$issue_file"
  if (( issue_count == 0 )); then
    printf '_No closed issues._\n'
  fi
  printf '\n## Upgrade notes\n\n'
  if [[ $tag == v0.13.0 ]]; then
    printf 'Update TarLink to v0.13.0 or newer before consuming the schema-v4 official registry.\n'
  elif (( breaking_count > 0 )) || [[ "$generated" == *'Removed / Breaking Changes'* || "$generated" == *'Breaking Changes'* || "$generated" == *'Upgrade Notes'* ]]; then
    printf 'Review the breaking or upgrade changes above before upgrading.\n'
  else
    printf 'No migration required.\n'
  fi
  printf '\n## Full changelog\n\n'
  if [[ -n $compare_base ]]; then
    printf '[Full changelog](https://github.com/%s/compare/%s...%s)\n' "$repo" "$compare_base" "$tag"
  else
    printf '[Full changelog](https://github.com/%s/commits/%s)\n' "$repo" "$tag"
  fi
} >"$output"
