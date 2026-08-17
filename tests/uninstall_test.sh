#!/bin/sh

set -eu

unset XDG_DATA_HOME XDG_STATE_HOME XDG_CACHE_HOME
sha256sum_dir=$(dirname "$(command -v sha256sum)")
test -n "$sha256sum_dir"

write_marker() {
	home=$1
	state_home=${2:-$home/.local/state}
	mkdir -p "$state_home/tarlink"
	sha256sum "$home/.local/bin/tarlink" | awk '{print $1}' > "$state_home/tarlink/install.sha256"
}

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
uninstaller=$script_dir/uninstall.sh
test -x "$uninstaller"

fixture=$(mktemp -d "${TMPDIR:-/tmp}/tarlink-uninstall-test.XXXXXXXX")
fixture=$(CDPATH= cd -- "$fixture" && pwd -P)
cleanup() {
	status=$?
	rm -rf "$fixture"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fake_home=$fixture/home
mkdir -p "$fake_home/.local/bin" "$fake_home/.local/share/tarlink" "$fake_home/.cache/tarlink"
cat > "$fake_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" > "${UNINSTALL_LOG:?}"
exit "${UNINSTALL_STATUS:-0}"
EOF
chmod 0755 "$fake_home/.local/bin/tarlink"
write_marker "$fake_home"

UNINSTALL_LOG=$fixture/failure.log UNINSTALL_STATUS=1 HOME=$fake_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" && exit 1 || :
test -x "$fake_home/.local/bin/tarlink"
test "$(cat "$fixture/failure.log")" = 'uninstall --all'

UNINSTALL_LOG=$fixture/success.log HOME=$fake_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller"
test ! -e "$fake_home/.local/bin/tarlink"
test ! -e "$fake_home/.local/state/tarlink/install.sha256"
test ! -e "$fake_home/.local/share/tarlink"
test ! -e "$fake_home/.cache/tarlink"
test -d "$fake_home/.local/bin"
test -d "$fake_home/.local/state"
test -d "$fake_home/.local/share"
test -d "$fake_home/.cache"
test "$(cat "$fixture/success.log")" = 'uninstall --all'

already_home=$fixture/already-home
mkdir -p "$already_home"
HOME=$already_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller"

control_home="$fixture/control-home$(printf '\t')"
if HOME="$control_home" PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/control-home.stdout" 2>"$fixture/control-home.stderr"; then
	printf '%s\n' 'control-character HOME unexpectedly succeeded' >&2
	exit 1
fi
grep -F 'HOME must not contain control characters' "$fixture/control-home.stderr" >/dev/null

control_xdg_home=$fixture/control-xdg-home
control_data="$control_xdg_home/data$(printf '\n'; printf x)"
if HOME="$control_xdg_home" XDG_DATA_HOME="$control_data" PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/control-data.stdout" 2>"$fixture/control-data.stderr"; then
	printf '%s\n' 'control-character XDG_DATA_HOME unexpectedly succeeded' >&2
	exit 1
fi
grep -F 'XDG homes must be clean paths below HOME' "$fixture/control-data.stderr" >/dev/null

unicode_home="$fixture/unicode-ž"
mkdir -p "$unicode_home/.local/bin"
cat > "$unicode_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$unicode_home/.local/bin/tarlink"
write_marker "$unicode_home"
HOME="$unicode_home" PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller"
test ! -e "$unicode_home/.local/bin/tarlink"

partial_home=$fixture/partial-home
mkdir -p "$partial_home/.local/bin" "$partial_home/.local/share/tarlink"
cat > "$partial_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$partial_home/.local/bin/tarlink"
write_marker "$partial_home"
if HOME=$partial_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/partial.stdout" 2>"$fixture/partial.stderr"; then
	printf '%s\n' 'partial uninstall unexpectedly succeeded' >&2
	exit 1
fi
test -x "$partial_home/.local/bin/tarlink"
test -d "$partial_home/.local/share/tarlink"

cat > "$fake_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$fake_home/.local/bin/tarlink"
write_marker "$fake_home"
UNINSTALL_LOG=$fixture/piped.log HOME=$fake_home PATH="$sha256sum_dir:/usr/bin:/bin" sh -c 'cat "$1" | sh' sh "$uninstaller"
test ! -e "$fake_home/.local/bin/tarlink"

unmarked_home=$fixture/unmarked-home
mkdir -p "$unmarked_home/.local/bin"
cat > "$unmarked_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
printf executed > "${UNMARKED_MARKER:?}"
EOF
chmod 0755 "$unmarked_home/.local/bin/tarlink"
if UNMARKED_MARKER=$fixture/unmarked-marker HOME=$unmarked_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/unmarked.stdout" 2>"$fixture/unmarked.stderr"; then
	printf '%s\n' 'unmarked canonical binary unexpectedly accepted' >&2
	exit 1
fi
test -x "$unmarked_home/.local/bin/tarlink"
test ! -e "$fixture/unmarked-marker"

malformed_home=$fixture/malformed-marker-home
mkdir -p "$malformed_home/.local/bin" "$malformed_home/.local/state/tarlink"
cat > "$malformed_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
printf executed > "${MALFORMED_MARKER:?}"
EOF
chmod 0755 "$malformed_home/.local/bin/tarlink"
printf '%s\n' malformed > "$malformed_home/.local/state/tarlink/install.sha256"
if MALFORMED_MARKER=$fixture/malformed-marker HOME=$malformed_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/malformed.stdout" 2>"$fixture/malformed.stderr"; then
	printf '%s\n' 'malformed ownership marker unexpectedly accepted' >&2
	exit 1
fi
test -x "$malformed_home/.local/bin/tarlink"
test ! -e "$fixture/malformed-marker"

mismatched_home=$fixture/mismatched-marker-home
mkdir -p "$mismatched_home/.local/bin" "$mismatched_home/.local/state/tarlink"
cat > "$mismatched_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
printf executed > "${MISMATCHED_MARKER:?}"
EOF
chmod 0755 "$mismatched_home/.local/bin/tarlink"
printf '%064d\n' 0 > "$mismatched_home/.local/state/tarlink/install.sha256"
if MISMATCHED_MARKER=$fixture/mismatched-marker HOME=$mismatched_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/mismatched.stdout" 2>"$fixture/mismatched.stderr"; then
	printf '%s\n' 'mismatched ownership marker unexpectedly accepted' >&2
	exit 1
fi
test -x "$mismatched_home/.local/bin/tarlink"
test ! -e "$fixture/mismatched-marker"

symlink_marker_home=$fixture/symlink-marker-home
symlink_marker_target=$fixture/symlink-marker-target
mkdir -p "$symlink_marker_home/.local/bin" "$symlink_marker_home/.local/state/tarlink"
cat > "$symlink_marker_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
printf executed > "${SYMLINK_MARKER_FILE:?}"
EOF
chmod 0755 "$symlink_marker_home/.local/bin/tarlink"
sha256sum "$symlink_marker_home/.local/bin/tarlink" | awk '{print $1}' > "$symlink_marker_target"
ln -s "$symlink_marker_target" "$symlink_marker_home/.local/state/tarlink/install.sha256"
if SYMLINK_MARKER_FILE=$fixture/symlink-marker-file HOME=$symlink_marker_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/symlink-marker.stdout" 2>"$fixture/symlink-marker.stderr"; then
	printf '%s\n' 'symlinked ownership marker unexpectedly accepted' >&2
	exit 1
fi
test -x "$symlink_marker_home/.local/bin/tarlink"
test ! -e "$fixture/symlink-marker-file"

unrelated_home=$fixture/unrelated-home
unrelated_bin=$fixture/unrelated-bin
mkdir -p "$unrelated_home" "$unrelated_bin"
cat > "$unrelated_bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$unrelated_bin/tarlink"
if HOME=$unrelated_home PATH="$unrelated_bin:$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/unrelated.stdout" 2>"$fixture/unrelated.stderr"; then
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
if SYMLINK_MARKER=$fixture/symlink-marker HOME=$symlink_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/symlink.stdout" 2>"$fixture/symlink.stderr"; then
	printf '%s\n' 'uninstaller accepted a symlinked binary' >&2
	exit 1
fi
test -L "$symlink_home/.local/bin/tarlink"
test ! -e "$fixture/symlink-marker"

symlink_parent_home=$fixture/symlink-parent-home
symlink_parent_target=$fixture/symlink-parent-target
mkdir -p "$symlink_parent_home" "$symlink_parent_target/bin"
cat > "$symlink_parent_target/bin/tarlink" <<'EOF'
#!/bin/sh
printf executed > "${SYMLINK_PARENT_MARKER:?}"
EOF
chmod 0755 "$symlink_parent_target/bin/tarlink"
mkdir -p "$symlink_parent_home/.local"
ln -s "$symlink_parent_target" "$symlink_parent_home/.local/bin"
if SYMLINK_PARENT_MARKER=$fixture/symlink-parent-marker HOME=$symlink_parent_home PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/symlink-parent.stdout" 2>"$fixture/symlink-parent.stderr"; then
	printf '%s\n' 'uninstaller accepted a symlinked binary parent' >&2
	exit 1
fi
test ! -e "$fixture/symlink-parent-marker"

owned_home=$fixture/owned-home
mkdir -p "$owned_home/.local/bin" "$owned_home/data/tarlink" "$owned_home/state/tarlink" "$owned_home/cache/tarlink"
cat > "$owned_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$owned_home/.local/bin/tarlink"
printf '%s\n' 'user-owned' > "$owned_home/data/tarlink/keep.txt"
write_marker "$owned_home" "$owned_home/state"
HOME=$owned_home XDG_DATA_HOME=$owned_home/data XDG_STATE_HOME=$owned_home/state XDG_CACHE_HOME=$owned_home/cache PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller"
test ! -e "$owned_home/.local/bin/tarlink"
test -f "$owned_home/data/tarlink/keep.txt"
test -d "$owned_home/data/tarlink"
test ! -e "$owned_home/state/tarlink"
test ! -e "$owned_home/cache/tarlink"

missing_product_home=$fixture/missing-product-home
mkdir -p "$missing_product_home/data/tarlink"
if HOME=$missing_product_home XDG_DATA_HOME=$missing_product_home/data PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/missing-product.stdout" 2>"$fixture/missing-product.stderr"; then
	printf '%s\n' 'missing-binary cleanup unexpectedly succeeded' >&2
	exit 1
fi
test -d "$missing_product_home/data/tarlink"

outside_home=$fixture/outside-home
outside_data=$fixture/outside-data
mkdir -p "$outside_home/.local/bin" "$outside_data/tarlink"
cat > "$outside_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$outside_home/.local/bin/tarlink"
if HOME=$outside_home XDG_DATA_HOME=$outside_data PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/outside.stdout" 2>"$fixture/outside.stderr"; then
	printf '%s\n' 'outside XDG home unexpectedly accepted' >&2
	exit 1
fi
test -x "$outside_home/.local/bin/tarlink"
test -d "$outside_data/tarlink"

relative_home=$fixture/relative-home
relative_data=$fixture/relative-data
mkdir -p "$relative_home/.local/bin" "$relative_data/tarlink"
cat > "$relative_home/.local/bin/tarlink" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0755 "$relative_home/.local/bin/tarlink"
if (cd "$relative_home" && HOME="$relative_home" XDG_DATA_HOME=../relative-data PATH="$sha256sum_dir:/usr/bin:/bin" "$uninstaller" >"$fixture/relative.stdout" 2>"$fixture/relative.stderr"); then
	printf '%s\n' 'relative XDG home unexpectedly accepted' >&2
	exit 1
fi
test -x "$relative_home/.local/bin/tarlink"
test -d "$relative_data/tarlink"

printf '%s\n' 'uninstall tests passed'
