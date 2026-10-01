#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
if [[ $1 == api && " $* " == *' --repo '* ]]; then
  echo 'gh api must use repository-qualified endpoints' >&2
  exit 99
fi
if [[ " $* " == *' --method POST '* ]]; then
  echo 'release notes must not use write APIs' >&2
  exit 98
fi
if [[ $1 == release && $2 == list ]]; then
  if [[ ${RELEASE_NOTES_FIRST:-} == 1 ]]; then
    printf '[{"tagName":"v0.1.0","publishedAt":"2026-01-01T00:00:00Z"}]\n'
  else
    printf '[{"tagName":"v0.1.0","publishedAt":"2026-01-01T00:00:00Z"},{"tagName":"v0.2.0","publishedAt":"2026-02-01T00:00:00Z"}]\n'
  fi
  exit
fi
case " $* " in
  *'/pulls?state=closed'*)
    printf '[[{"number":7,"title":"Old pull","user":{"login":"Ada"},"html_url":"https://github.com/drobilica/tarlink/pull/7","merged_at":"2025-12-15T00:00:00Z"},{"number":8,"title":"New pull","user":{"login":"Grace"},"html_url":"https://github.com/drobilica/tarlink/pull/8","merged_at":"2026-02-15T00:00:00Z"}]]\n'
    ;;
  *'/compare/'*)
     printf '{"commits":[{"sha":"abcdef1234567","commit":{"message":"feat: Direct change\\n\\nDetails","author":{"name":"Author"}},"author":null}]}\n'
    ;;
  *'/commits/'*'/pulls '*) printf '0\n' ;;
  *'/issues?state=closed'*) printf '[]\n' ;;
  *'/commits?sha='*) printf '[{"sha":"abcdef1234567","commit":{"message":"feat: Direct change","author":{"name":"Author"}},"author":null}]\n' ;;
  *'/commits/targetsha'*) printf '{"commit":{"committer":{"date":"2026-03-01T00:00:00Z"}}}\n' ;;
  *) printf '{}\n' ;;
esac
GH
chmod 0755 "$tmp/gh"

run_case() {
  local first=$1
  local output="$tmp/notes-$first.md"
  RELEASE_NOTES_FIRST=$first PATH="$tmp:$PATH" bash "$script_dir/.github/scripts/generate-release-notes.sh" \
    drobilica/tarlink "v0.$((first == 1 ? 1 : 3)).0" targetsha "$output"
  if [[ $first == 1 ]]; then
    grep -Fq '[#7](https://github.com/drobilica/tarlink/pull/7) Old pull by @Ada' "$output"
    if grep -Fq '[#8](' "$output"; then
      echo 'first release must not include later pull requests' >&2
      exit 1
    fi
  else
    grep -Fq '[#8](https://github.com/drobilica/tarlink/pull/8) New pull by @Grace' "$output"
    if grep -Fq '[#7](' "$output"; then
      echo 'later release must not repeat earlier pull requests' >&2
      exit 1
    fi
  fi
  grep -Fq 'Direct change by @Author' "$output"
  grep -Fq '## Full changelog' "$output"
}

run_case 1
run_case 0
cutover_notes="$tmp/notes-v0.13.0.md"
PATH="$tmp:$PATH" bash "$script_dir/.github/scripts/generate-release-notes.sh" \
  drobilica/tarlink v0.13.0 targetsha "$cutover_notes"
grep -Fq 'Update TarLink to v0.13.0 or newer before consuming the schema-v4 official registry.' "$cutover_notes"
printf 'release notes tests passed\n'
