#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
installer=$script_dir/install.sh
test -x "$installer"
release_workflow=$script_dir/.github/workflows/release.yml
grep -F 'GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o dist/tarlink-linux-amd64' "$release_workflow" >/dev/null
grep -F 'GOARCH=arm64 go build -trimpath -ldflags "$LDFLAGS" -o dist/tarlink-linux-arm64' "$release_workflow" >/dev/null
grep -F 'github.com/drobilica/tarlink/internal/version.Current=$RELEASE_VERSION' "$release_workflow" >/dev/null
grep -F 'sha256sum tarlink-linux-amd64 tarlink-linux-arm64 > checksums.txt' "$release_workflow" >/dev/null
grep -F 'gh release upload "$RELEASE_TAG" dist/tarlink-linux-amd64 dist/tarlink-linux-arm64 dist/checksums.txt --clobber' "$release_workflow" >/dev/null
if grep -E 'gh release upload.*(install\.sh|uninstall\.sh|\.tar\.gz)' "$release_workflow" >/dev/null; then
	printf '%s\n' 'release workflow publishes a forbidden packaged or shell asset' >&2
	exit 1
fi

fixture=$(mktemp -d "${TMPDIR:-/tmp}/tarlink-installer-test.XXXXXXXX")
cleanup() {
	status=$?
	rm -rf "$fixture"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fake_bin=$fixture/bin
mkdir -p "$fake_bin"

cat > "$fake_bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;;
  -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;;
  *) exit 1 ;;
esac
EOF

cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
[ -z "${CURL_ARGS_LOG:-}" ] || printf '%s\n' "$*" >> "$CURL_ARGS_LOG"
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    --max-redirs|--connect-timeout|--max-time|--proto|--proto-redir) shift 2 ;;
    -q|--fail|--location|--silent|--show-error|--tlsv1.2) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >> "$CURL_LOG"
if [ "${FAIL_DOWNLOAD:-0}" = 1 ]; then
  exit 22
fi
case "$url" in
  */checksums.txt)
    checksum_asset=tarlink-linux-amd64
    case "${FAKE_UNAME_M:-x86_64}" in
      aarch64|arm64) checksum_asset=tarlink-linux-arm64 ;;
    esac
    if [ "${BAD_CHECKSUM:-0}" = 1 ]; then
      printf '%s  %s\n' 0000000000000000000000000000000000000000000000000000000000000000 "$checksum_asset" > "$output"
    else
      printf '%s  %s\n' 6c937958261f6fa4eefd3b498d9736048086c1d16a7c98e2e482c6a8c42987d0 "$checksum_asset" > "$output"
    fi
    ;;
  */tarlink-linux-amd64|*/tarlink-linux-arm64) printf 'tarlink-test-binary\n' > "$output" ;;
  *) exit 1 ;;
esac
EOF

chmod 0755 "$fake_bin/uname" "$fake_bin/curl"

latest_home=$fixture/latest-home
latest_log=$fixture/latest.log
latest_stdout=$fixture/latest.stdout
latest_stderr=$fixture/latest.stderr
mkdir -p "$latest_home"
HOME=$latest_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$latest_log CURL_ARGS_LOG=$fixture/curl-args.log "$installer" >"$latest_stdout" 2>"$latest_stderr"
test "$(cat "$latest_home/.local/bin/tarlink")" = 'tarlink-test-binary'
test -x "$latest_home/.local/bin/tarlink"
grep '/releases/latest/download/checksums.txt' "$latest_log" >/dev/null
grep '/releases/latest/download/tarlink-linux-amd64' "$latest_log" >/dev/null
grep -F -- '-q --fail --location --max-redirs 5 --connect-timeout 15 --max-time 600' "$fixture/curl-args.log" >/dev/null
grep -F 'Warning:' "$latest_stderr" >/dev/null
printf 'previous\n' > "$latest_home/.local/bin/tarlink"
HOME=$latest_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$latest_log "$installer" >"$fixture/repeat.stdout" 2>"$fixture/repeat.stderr"
test "$(cat "$latest_home/.local/bin/tarlink")" = 'tarlink-test-binary'

explicit_home=$fixture/explicit-home
explicit_log=$fixture/explicit.log
explicit_stdout=$fixture/explicit.stdout
explicit_stderr=$fixture/explicit.stderr
mkdir -p "$explicit_home"
FAKE_UNAME_M=aarch64 HOME=$explicit_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$explicit_log "$installer" v9.9.9 >"$explicit_stdout" 2>"$explicit_stderr"
grep '/releases/download/v9.9.9/checksums.txt' "$explicit_log" >/dev/null
grep '/releases/download/v9.9.9/tarlink-linux-arm64' "$explicit_log" >/dev/null

failed_home=$fixture/failed-home
mkdir -p "$failed_home/.local/bin"
printf 'previous\n' > "$failed_home/.local/bin/tarlink"
if BAD_CHECKSUM=1 HOME=$failed_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/failed.log "$installer" >"$fixture/failed.stdout" 2>"$fixture/failed.stderr"; then
  printf '%s\n' 'checksum mismatch unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$failed_home/.local/bin/tarlink")" = previous
grep -F 'SHA-256 verification failed' "$fixture/failed.stderr" >/dev/null

unavailable_home=$fixture/unavailable-home
mkdir -p "$unavailable_home/.local/bin"
printf 'previous\n' > "$unavailable_home/.local/bin/tarlink"
if FAIL_DOWNLOAD=1 HOME=$unavailable_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/unavailable.log "$installer" >"$fixture/unavailable.stdout" 2>"$fixture/unavailable.stderr"; then
  printf '%s\n' 'unavailable release unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$unavailable_home/.local/bin/tarlink")" = previous
grep -F 'could not download checksums.txt' "$fixture/unavailable.stderr" >/dev/null

unsupported_arch_home=$fixture/unsupported-arch-home
if FAKE_UNAME_M=ppc64le HOME=$unsupported_arch_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/unsupported-arch.log "$installer" >"$fixture/unsupported-arch.stdout" 2>"$fixture/unsupported-arch.stderr"; then
  printf '%s\n' 'unsupported architecture unexpectedly succeeded' >&2
  exit 1
fi
test ! -e "$unsupported_arch_home/.local/bin/tarlink"
grep -F 'unsupported architecture' "$fixture/unsupported-arch.stderr" >/dev/null

present_home=$fixture/present-home
present_bin=$present_home/.local/bin
present_log=$fixture/present.log
present_stderr=$fixture/present.stderr
mkdir -p "$present_bin"
HOME=$present_home PATH="$present_bin:$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$present_log "$installer" >"$fixture/present.stdout" 2>"$present_stderr"
if grep -F 'Warning:' "$present_stderr" >/dev/null; then
  printf '%s\n' 'unexpected PATH warning when bin directory is present' >&2
  exit 1
fi
test -x "$present_bin/tarlink"

if FAKE_UNAME_S=Darwin HOME=$fixture/unsupported-home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/unsupported.log "$installer" >"$fixture/unsupported.stdout" 2>"$fixture/unsupported.stderr"; then
  printf '%s\n' 'unsupported operating system unexpectedly succeeded' >&2
  exit 1
fi

printf '%s\n' 'installer tests passed'
