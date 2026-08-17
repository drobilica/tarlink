#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
uninstaller=$script_dir/uninstall.sh
test -x "$uninstaller"

fixture=$(mktemp -d "${TMPDIR:-/tmp}/tarlink-uninstall-test.XXXXXXXX")
cleanup() {
	status=$?
	rm -rf "$fixture"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fake_home=$fixture/home
mkdir -p "$fake_home/.local/bin"
cat > "$fake_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" > "${UNINSTALL_LOG:?}"
exit "${UNINSTALL_STATUS:-0}"
EOF
chmod 0755 "$fake_home/.local/bin/tarlink"

UNINSTALL_LOG=$fixture/failure.log UNINSTALL_STATUS=1 HOME=$fake_home PATH=/usr/bin:/bin "$uninstaller" && exit 1 || :
test -x "$fake_home/.local/bin/tarlink"
test "$(cat "$fixture/failure.log")" = 'uninstall --all'

UNINSTALL_LOG=$fixture/success.log HOME=$fake_home PATH=/usr/bin:/bin "$uninstaller"
test ! -e "$fake_home/.local/bin/tarlink"
test "$(cat "$fixture/success.log")" = 'uninstall --all'

cat > "$fake_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$fake_home/.local/bin/tarlink"
UNINSTALL_LOG=$fixture/piped.log HOME=$fake_home PATH=/usr/bin:/bin sh -c 'cat "$1" | sh' sh "$uninstaller"
test ! -e "$fake_home/.local/bin/tarlink"

unrelated_home=$fixture/unrelated-home
unrelated_bin=$fixture/unrelated-bin
mkdir -p "$unrelated_home" "$unrelated_bin"
cat > "$unrelated_bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$unrelated_bin/tarlink"
if HOME=$unrelated_home PATH="$unrelated_bin:/usr/bin:/bin" "$uninstaller" >"$fixture/unrelated.stdout" 2>"$fixture/unrelated.stderr"; then
	printf '%s\n' 'uninstaller used a noncanonical PATH binary' >&2
	exit 1
fi
test -x "$unrelated_bin/tarlink"
grep -F "$unrelated_home/.local/bin/tarlink" "$fixture/unrelated.stderr" >/dev/null

symlink_home=$fixture/symlink-home
mkdir -p "$symlink_home/.local/bin"
cat > "$fixture/symlink-target" <<'EOF'
#!/bin/sh
printf executed > "${SYMLINK_MARKER:?}"
EOF
chmod 0755 "$fixture/symlink-target"
ln -s "$fixture/symlink-target" "$symlink_home/.local/bin/tarlink"
if SYMLINK_MARKER=$fixture/symlink-marker HOME=$symlink_home PATH=/usr/bin:/bin "$uninstaller" >"$fixture/symlink.stdout" 2>"$fixture/symlink.stderr"; then
	printf '%s\n' 'uninstaller accepted a symlinked binary' >&2
	exit 1
fi
test -L "$symlink_home/.local/bin/tarlink"
test ! -e "$fixture/symlink-marker"

printf '%s\n' 'uninstall tests passed'
