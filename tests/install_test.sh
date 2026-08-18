#!/bin/sh

set -eu

unset XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME XDG_CACHE_HOME

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
installer=$script_dir/install.sh
test -x "$installer"
sha256sum_dir=$(dirname "$(command -v sha256sum)")
test -n "$sha256sum_dir"
real_mv=$(command -v mv)
test -n "$real_mv"
test -x "$script_dir/tests/release_artifacts_test.sh"

exact_assets=${TARLINK_TEST_ASSET_DIR:-}
exact_version=${TARLINK_TEST_RELEASE_VERSION:-}
exact_arch=${TARLINK_TEST_ARCH:-}
latest_asset=amd64
case "${FAKE_UNAME_M:-x86_64}" in
	aarch64|arm64) latest_asset=arm64 ;;
esac
if [ -n "$exact_assets" ]; then
	test -n "$exact_version" || {
		printf '%s\n' 'TARLINK_TEST_RELEASE_VERSION is required with TARLINK_TEST_ASSET_DIR' >&2
		exit 1
	}
	case "$exact_arch" in
		amd64|arm64) ;;
		*) printf '%s\n' 'TARLINK_TEST_ARCH must be amd64 or arm64' >&2; exit 1 ;;
	esac
	"$script_dir/tests/release_artifacts_test.sh" "$exact_assets" "$exact_version" "$exact_arch"
fi

fixture=$(mktemp -d "${TMPDIR:-/tmp}/tarlink-installer-test.XXXXXXXX")
fixture=$(CDPATH= cd -P -- "$fixture" && pwd -P)
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
    checksum_mode=${CHECKSUM_MODE:-}
    if [ -z "$checksum_mode" ] && [ "${BAD_CHECKSUM:-0}" = 1 ]; then
      checksum_mode=bad
    fi
    if [ -n "${TARLINK_TEST_ASSET_DIR:-}" ]; then
      case "$checksum_mode" in
        missing)
          awk -v asset="$checksum_asset" '$2 != asset' "$TARLINK_TEST_ASSET_DIR/checksums.txt" > "$output"
          ;;
        duplicate)
          cp "$TARLINK_TEST_ASSET_DIR/checksums.txt" "$output"
          awk -v asset="$checksum_asset" '$2 == asset' "$TARLINK_TEST_ASSET_DIR/checksums.txt" >> "$output"
          ;;
        malformed)
          printf '%s  %s\n' malformed-digest "$checksum_asset" > "$output"
          ;;
        uppercase)
          awk -v asset="$checksum_asset" '$2 == asset { print toupper($1), $2 }' "$TARLINK_TEST_ASSET_DIR/checksums.txt" > "$output"
          ;;
        bad)
          printf '%s  %s\n' 0000000000000000000000000000000000000000000000000000000000000000 "$checksum_asset" > "$output"
          ;;
        *)
        cp "$TARLINK_TEST_ASSET_DIR/checksums.txt" "$output"
          ;;
      esac
    else
      case "$checksum_mode" in
        missing)
          printf '%s  %s\n' 0000000000000000000000000000000000000000000000000000000000000000 tarlink-linux-other > "$output"
          ;;
        duplicate)
          printf '%s  %s\n%s  %s\n' 6c937958261f6fa4eefd3b498d9736048086c1d16a7c98e2e482c6a8c42987d0 "$checksum_asset" 6c937958261f6fa4eefd3b498d9736048086c1d16a7c98e2e482c6a8c42987d0 "$checksum_asset" > "$output"
          ;;
        malformed)
          printf '%s  %s\n' malformed-digest "$checksum_asset" > "$output"
          ;;
        uppercase)
          printf '%s  %s\n' 6C937958261F6FA4EEFD3B498D9736048086C1D16A7C98E2E482C6A8C42987D0 "$checksum_asset" > "$output"
          ;;
        bad)
          printf '%s  %s\n' 0000000000000000000000000000000000000000000000000000000000000000 "$checksum_asset" > "$output"
          ;;
        *)
          printf '%s  %s\n' 6c937958261f6fa4eefd3b498d9736048086c1d16a7c98e2e482c6a8c42987d0 "$checksum_asset" > "$output"
          ;;
      esac
    fi
    ;;
  */tarlink-linux-amd64|*/tarlink-linux-arm64)
    if [ "${FAIL_BINARY_DOWNLOAD:-0}" = 1 ]; then
      exit 22
    fi
    if [ -n "${TARLINK_TEST_ASSET_DIR:-}" ]; then
      asset=${url##*/}
      cp "$TARLINK_TEST_ASSET_DIR/$asset" "$output"
    else
      printf 'tarlink-test-binary\n' > "$output"
    fi
    ;;
  *) exit 1 ;;
esac
EOF

chmod 0755 "$fake_bin/uname" "$fake_bin/curl"

failing_bin=$fixture/failing-bin
mkdir -p "$failing_bin"
cat > "$failing_bin/mv" <<'EOF'
#!/bin/sh
set -eu
destination=
for argument do
  destination=$argument
done
case "$destination" in
  */install.sha256) exit 1 ;;
esac
exec "$REAL_MV" "$@"
EOF
chmod 0755 "$failing_bin/mv"

write_marker() {
  home=$1
  state_home=${2:-$home/.local/state}
  mkdir -p "$state_home/tarlink"
  sha256sum "$home/.local/bin/tarlink" | awk '{print $1}' > "$state_home/tarlink/install.sha256"
}

assert_installed_binary() {
  home=$1
  test -x "$home/.local/bin/tarlink"
  if [ -n "$exact_assets" ]; then
    mkdir -p "$home/config" "$home/data" "$home/state" "$home/cache"
    output=$(HOME="$home" XDG_CONFIG_HOME="$home/config" XDG_DATA_HOME="$home/data" XDG_STATE_HOME="$home/state" XDG_CACHE_HOME="$home/cache" "$home/.local/bin/tarlink" version)
    test "$output" = "tarlink $exact_version"
  else
    test "$(cat "$home/.local/bin/tarlink")" = 'tarlink-test-binary'
  fi
}

latest_home=$fixture/latest-home
latest_log=$fixture/latest.log
latest_stdout=$fixture/latest.stdout
latest_stderr=$fixture/latest.stderr
mkdir -p "$latest_home"
HOME=$latest_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$latest_log CURL_ARGS_LOG=$fixture/curl-args.log "$installer" >"$latest_stdout" 2>"$latest_stderr"
assert_installed_binary "$latest_home"
test -f "$latest_home/.local/state/tarlink/install.sha256"
grep -F 'Installing TarLink version: latest' "$latest_stdout" >/dev/null
grep -F 'TarLink installed successfully.' "$latest_stdout" >/dev/null
grep '/releases/latest/download/checksums.txt' "$latest_log" >/dev/null
grep "/releases/latest/download/tarlink-linux-$latest_asset" "$latest_log" >/dev/null
grep -F -- '-q --fail --location --max-redirs 5 --connect-timeout 15 --max-time 600' "$fixture/curl-args.log" >/dev/null
grep -F 'Warning:' "$latest_stderr" >/dev/null
HOME=$latest_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$latest_log "$installer" >"$fixture/repeat.stdout" 2>"$fixture/repeat.stderr"
assert_installed_binary "$latest_home"

marker_failure_home=$fixture/marker-failure-home
mkdir -p "$marker_failure_home/.local/bin"
printf 'previous\n' > "$marker_failure_home/.local/bin/tarlink"
chmod 0755 "$marker_failure_home/.local/bin/tarlink"
write_marker "$marker_failure_home"
if REAL_MV=$real_mv HOME=$marker_failure_home PATH="$failing_bin:$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/marker-failure.log "$installer" >"$fixture/marker-failure.stdout" 2>"$fixture/marker-failure.stderr"; then
  printf '%s\n' 'marker publication failure unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$marker_failure_home/.local/bin/tarlink")" = previous
test "$(cat "$marker_failure_home/.local/state/tarlink/install.sha256")" = "$(sha256sum "$marker_failure_home/.local/bin/tarlink" | awk '{print $1}')"
for marker_temp in "$marker_failure_home/.local/state/tarlink/.install.sha256."*; do
  test ! -e "$marker_temp" && test ! -L "$marker_temp"
done
grep -F 'could not install the ownership marker' "$fixture/marker-failure.stderr" >/dev/null

first_marker_failure_home=$fixture/first-marker-failure-home
mkdir -p "$first_marker_failure_home/.local/bin"
if REAL_MV=$real_mv HOME=$first_marker_failure_home PATH="$failing_bin:$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/first-marker-failure.log "$installer" >"$fixture/first-marker-failure.stdout" 2>"$fixture/first-marker-failure.stderr"; then
  printf '%s\n' 'first-install marker publication failure unexpectedly succeeded' >&2
  exit 1
fi
test ! -e "$first_marker_failure_home/.local/bin/tarlink"
test ! -e "$first_marker_failure_home/.local/state/tarlink/install.sha256"
for marker_temp in "$first_marker_failure_home/.local/state/tarlink/.install.sha256."*; do
  test ! -e "$marker_temp" && test ! -L "$marker_temp"
done
grep -F 'could not install the ownership marker' "$fixture/first-marker-failure.stderr" >/dev/null

occupied_home=$fixture/occupied-home
mkdir -p "$occupied_home/.local/bin"
printf '%s\n' 'user-owned' > "$occupied_home/.local/bin/tarlink"
chmod 0755 "$occupied_home/.local/bin/tarlink"
if HOME=$occupied_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/occupied.log "$installer" >"$fixture/occupied.stdout" 2>"$fixture/occupied.stderr"; then
  printf '%s\n' 'unmarked canonical file unexpectedly replaced' >&2
  exit 1
fi
test "$(cat "$occupied_home/.local/bin/tarlink")" = 'user-owned'
test ! -e "$occupied_home/.local/state/tarlink/install.sha256"
grep -F 'ownership marker' "$fixture/occupied.stderr" >/dev/null

occupied_file_home=$fixture/occupied-file-home
mkdir -p "$occupied_file_home/.local/bin"
printf '%s\n' 'user-owned-file' > "$occupied_file_home/.local/bin/tarlink"
if HOME=$occupied_file_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/occupied-file.log "$installer" >"$fixture/occupied-file.stdout" 2>"$fixture/occupied-file.stderr"; then
  printf '%s\n' 'unmarked canonical file unexpectedly replaced' >&2
  exit 1
fi
test "$(cat "$occupied_file_home/.local/bin/tarlink")" = 'user-owned-file'
test ! -e "$occupied_file_home/.local/state/tarlink/install.sha256"

explicit_home=$fixture/explicit-home
explicit_log=$fixture/explicit.log
explicit_stdout=$fixture/explicit.stdout
explicit_stderr=$fixture/explicit.stderr
mkdir -p "$explicit_home"
explicit_machine=aarch64
explicit_asset=arm64
if [ -n "$exact_assets" ]; then
  case "$exact_arch" in
    amd64) explicit_machine=x86_64; explicit_asset=amd64 ;;
    arm64) explicit_machine=aarch64 ;;
  esac
fi
FAKE_UNAME_M=$explicit_machine HOME=$explicit_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$explicit_log "$installer" v9.9.9 >"$explicit_stdout" 2>"$explicit_stderr"
assert_installed_binary "$explicit_home"
grep -F 'Installing TarLink version: v9.9.9' "$explicit_stdout" >/dev/null
grep -F 'TarLink installed successfully.' "$explicit_stdout" >/dev/null
grep '/releases/download/v9.9.9/checksums.txt' "$explicit_log" >/dev/null
grep "/releases/download/v9.9.9/tarlink-linux-$explicit_asset" "$explicit_log" >/dev/null

failed_home=$fixture/failed-home
mkdir -p "$failed_home/.local/bin"
printf 'previous\n' > "$failed_home/.local/bin/tarlink"
chmod 0755 "$failed_home/.local/bin/tarlink"
write_marker "$failed_home"
if BAD_CHECKSUM=1 HOME=$failed_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/failed.log "$installer" >"$fixture/failed.stdout" 2>"$fixture/failed.stderr"; then
  printf '%s\n' 'checksum mismatch unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$failed_home/.local/bin/tarlink")" = previous
grep -F 'SHA-256 verification failed' "$fixture/failed.stderr" >/dev/null

run_checksum_failure() {
  case_name=$1
  checksum_mode=$2
  case_home=$fixture/checksum-$case_name-home
  mkdir -p "$case_home/.local/bin"
  printf 'previous\n' > "$case_home/.local/bin/tarlink"
  chmod 0755 "$case_home/.local/bin/tarlink"
  write_marker "$case_home"
  if CHECKSUM_MODE=$checksum_mode HOME=$case_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG="$fixture/checksum-$case_name.log" "$installer" >"$fixture/checksum-$case_name.stdout" 2>"$fixture/checksum-$case_name.stderr"; then
    printf '%s checksum unexpectedly succeeded\n' "$case_name" >&2
    exit 1
  fi
  test "$(cat "$case_home/.local/bin/tarlink")" = previous
  grep -F 'checksums.txt has no unique lowercase SHA-256' "$fixture/checksum-$case_name.stderr" >/dev/null
}

run_checksum_failure missing missing
run_checksum_failure duplicate duplicate
run_checksum_failure malformed malformed
run_checksum_failure uppercase uppercase

binary_failed_home=$fixture/binary-failed-home
mkdir -p "$binary_failed_home/.local/bin"
printf 'previous\n' > "$binary_failed_home/.local/bin/tarlink"
chmod 0755 "$binary_failed_home/.local/bin/tarlink"
write_marker "$binary_failed_home"
if FAIL_BINARY_DOWNLOAD=1 HOME=$binary_failed_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/binary-failed.log "$installer" >"$fixture/binary-failed.stdout" 2>"$fixture/binary-failed.stderr"; then
  printf '%s\n' 'binary download failure unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$binary_failed_home/.local/bin/tarlink")" = previous
grep -F 'could not download tarlink-linux-' "$fixture/binary-failed.stderr" >/dev/null

relative_home=$fixture/relative-home
if (cd "$fixture" && HOME=relative-home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/relative.log "$installer" >"$fixture/relative.stdout" 2>"$fixture/relative.stderr"); then
  printf '%s\n' 'relative HOME unexpectedly succeeded' >&2
  exit 1
fi
test ! -e "$relative_home/.local"
test ! -e "$fixture/relative.log"
grep -F 'HOME must be an absolute path' "$fixture/relative.stderr" >/dev/null

unclean_home=$fixture/unclean-home
mkdir -p "$unclean_home/.local/bin"
printf 'previous\n' > "$unclean_home/.local/bin/tarlink"
if HOME="$fixture/unclean-parent/../unclean-home" PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/unclean.log "$installer" >"$fixture/unclean.stdout" 2>"$fixture/unclean.stderr"; then
  printf '%s\n' 'unclean HOME unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$unclean_home/.local/bin/tarlink")" = previous
test ! -e "$fixture/unclean.log"
grep -F 'HOME must be an absolute, clean path' "$fixture/unclean.stderr" >/dev/null

control_home="$fixture/control-home$(printf '\t')"
if HOME="$control_home" PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG="$fixture/control-home.log" "$installer" >"$fixture/control-home.stdout" 2>"$fixture/control-home.stderr"; then
  printf '%s\n' 'control-character HOME unexpectedly succeeded' >&2
  exit 1
fi
test ! -e "$fixture/control-home.log"
grep -F 'HOME must not contain control characters' "$fixture/control-home.stderr" >/dev/null

control_xdg_home=$fixture/control-xdg-home
control_state="$control_xdg_home/state$(printf '\n'; printf x)"
if HOME="$control_xdg_home" XDG_STATE_HOME="$control_state" PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG="$fixture/control-state.log" "$installer" >"$fixture/control-state.stdout" 2>"$fixture/control-state.stderr"; then
  printf '%s\n' 'control-character XDG_STATE_HOME unexpectedly succeeded' >&2
  exit 1
fi
test ! -e "$fixture/control-state.log"
grep -F 'XDG_STATE_HOME must not contain control characters' "$fixture/control-state.stderr" >/dev/null

unicode_home="$fixture/unicode-ž"
mkdir -p "$unicode_home"
HOME="$unicode_home" PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG="$fixture/unicode.log" "$installer" >"$fixture/unicode.stdout" 2>"$fixture/unicode.stderr"
assert_installed_binary "$unicode_home"

outside_local=$fixture/outside-local
symlink_local_home=$fixture/symlink-local-home
mkdir -p "$outside_local/bin" "$symlink_local_home"
printf 'outside\n' > "$outside_local/bin/tarlink"
ln -s "$outside_local" "$symlink_local_home/.local"
if HOME=$symlink_local_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/symlink-local.log "$installer" >"$fixture/symlink-local.stdout" 2>"$fixture/symlink-local.stderr"; then
  printf '%s\n' 'symlinked .local unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$outside_local/bin/tarlink")" = outside
test -L "$symlink_local_home/.local"
test ! -e "$fixture/symlink-local.log"
grep -F 'canonical TarLink binary path contains a symlink' "$fixture/symlink-local.stderr" >/dev/null

outside_bin=$fixture/outside-bin
symlink_bin_home=$fixture/symlink-bin-home
mkdir -p "$outside_bin" "$symlink_bin_home/.local"
printf 'outside\n' > "$outside_bin/tarlink"
ln -s "$outside_bin" "$symlink_bin_home/.local/bin"
if HOME=$symlink_bin_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/symlink-bin.log "$installer" >"$fixture/symlink-bin.stdout" 2>"$fixture/symlink-bin.stderr"; then
  printf '%s\n' 'symlinked bin unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$outside_bin/tarlink")" = outside
test -L "$symlink_bin_home/.local/bin"
test ! -e "$fixture/symlink-bin.log"
grep -F 'canonical TarLink binary path contains a symlink' "$fixture/symlink-bin.stderr" >/dev/null

outside_target=$fixture/outside-target
symlink_target_home=$fixture/symlink-target-home
mkdir -p "$outside_target" "$symlink_target_home/.local/bin"
printf 'outside\n' > "$outside_target/tarlink"
ln -s "$outside_target/tarlink" "$symlink_target_home/.local/bin/tarlink"
if HOME=$symlink_target_home PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/symlink-target.log "$installer" >"$fixture/symlink-target.stdout" 2>"$fixture/symlink-target.stderr"; then
  printf '%s\n' 'symlinked target unexpectedly succeeded' >&2
  exit 1
fi
test "$(cat "$outside_target/tarlink")" = outside
test -L "$symlink_target_home/.local/bin/tarlink"
grep -F 'must not be a symlink' "$fixture/symlink-target.stderr" >/dev/null

unavailable_home=$fixture/unavailable-home
mkdir -p "$unavailable_home/.local/bin"
printf 'previous\n' > "$unavailable_home/.local/bin/tarlink"
chmod 0755 "$unavailable_home/.local/bin/tarlink"
write_marker "$unavailable_home"
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

if [ -n "$exact_assets" ]; then
  exact_uninstall_home=$fixture/exact-uninstall-home
  mkdir -p "$exact_uninstall_home"
  FAKE_UNAME_M=$explicit_machine HOME=$exact_uninstall_home XDG_STATE_HOME=$exact_uninstall_home/state PATH="$fake_bin:/sbin:/usr/bin:/bin" CURL_LOG=$fixture/exact-uninstall.log "$installer" >"$fixture/exact-uninstall-install.stdout" 2>"$fixture/exact-uninstall-install.stderr"
  assert_installed_binary "$exact_uninstall_home"
  HOME=$exact_uninstall_home XDG_CONFIG_HOME=$exact_uninstall_home/config XDG_DATA_HOME=$exact_uninstall_home/data XDG_STATE_HOME=$exact_uninstall_home/state XDG_CACHE_HOME=$exact_uninstall_home/cache PATH="$exact_uninstall_home/.local/bin:$sha256sum_dir:/sbin:/usr/bin:/bin" "$script_dir/uninstall.sh" >"$fixture/exact-uninstall.stdout" 2>"$fixture/exact-uninstall.stderr"
  test ! -e "$exact_uninstall_home/.local/bin/tarlink"
  test ! -e "$exact_uninstall_home/state/tarlink/install.sha256"
fi

printf '%s\n' 'installer tests passed'
