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
if [[ $1 == release && $2 == list ]]; then
  if [[ ${RELEASE_NOTES_FIRST:-} == 1 ]]; then
    printf '[{"tagName":"v0.1.0","publishedAt":"2026-01-01T00:00:00Z"}]\n'
  else
    printf '[{"tagName":"v0.1.0","publishedAt":"2026-01-01T00:00:00Z"},{"tagName":"v0.2.0","publishedAt":"2026-02-01T00:00:00Z"}]\n'
  fi
  exit
fi
case " $* " in
  *'/releases/generate-notes '*)
    [[ ${RELEASE_NOTES_FIRST:-} == 1 ]] || [[ $* == *'previous_tag_name=v0.2.0'* ]]
     printf '{"body":"## Added\\n\\n- Add feature (#1)"}\n'
    ;;
  *'/compare/'*)
     printf '{"commits":[{"sha":"abcdef1234567","commit":{"message":"feat: Direct change\\n\\nDetails","author":{"name":"Author"}},"author":null}]}\n'
    ;;
  *'/commits/'*'/pulls '*) printf '0\n' ;;
  *'/issues?state=closed'*) printf '[]\n' ;;
  *'/commits?sha='*) printf '[{"sha":"abcdef1234567","commit":{"message":"feat: Direct change","author":{"name":"Author"}},"author":null}]\n' ;;
  *) printf '{}\n' ;;
esac
GH
chmod 0755 "$tmp/gh"

run_case() {
  local first=$1
  local output="$tmp/notes-$first.md"
  RELEASE_NOTES_FIRST=$first PATH="$tmp:$PATH" bash "$script_dir/.github/scripts/generate-release-notes.sh" \
    drobilica/tarlink "v0.$((first == 1 ? 1 : 3)).0" targetsha "$output"
  grep -Fq '## Added' "$output"
  grep -Fq 'Direct change by @Author' "$output"
  grep -Fq '## Full changelog' "$output"
}

run_case 1
run_case 0
printf 'release notes tests passed\n'
