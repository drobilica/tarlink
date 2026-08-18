#!/bin/sh
set -eu

home=${HOME:?HOME must be set}
valid_path_text() {
	path_value=$1
	LC_ALL=C TARLINK_PATH_VALUE="$path_value" awk '
		BEGIN {
			value = ENVIRON["TARLINK_PATH_VALUE"]
			for (position = 1; position <= length(value); position++) {
				if (substr(value, position, 1) ~ /[[:cntrl:]]/) {
					exit 1
				}
			}
			exit 0
		}
	'
}

if ! valid_path_text "$home"; then
	echo 'HOME must not contain control characters' >&2
	exit 1
fi
case "$home" in
	/*) ;;
	*)
		echo 'HOME must be an absolute path' >&2
		exit 1
		;;
esac
case "$home" in
	*'//'*) exit 1 ;;
	*/./*|*/.|./*|.) exit 1 ;;
	*/../*|*/..|../*|..) exit 1 ;;
esac

binary=$home/.local/bin/tarlink
data_home=${XDG_DATA_HOME:-$home/.local/share}
state_home=${XDG_STATE_HOME:-$home/.local/state}
cache_home=${XDG_CACHE_HOME:-$home/.cache}
marker=$state_home/tarlink/install.sha256

safe_layout_path() {
	path=$1
	valid_path_text "$path" || return 1
	case "$path" in
		"$home"|"$home"/*) ;;
		*) return 1 ;;
	esac
	case "$path" in
		*'//'*) return 1 ;;
		*/./*|*/.|./*|.) return 1 ;;
		*/../*|*/..|../*|..) return 1 ;;
	esac
	current=/
	remainder=$path
	while [ "$remainder" != / ]; do
		remainder=${remainder#/}
		component=${remainder%%/*}
		if [ "$component" = "$remainder" ]; then
			remainder=/
		else
			remainder=/${remainder#*/}
		fi
		[ -n "$component" ] || return 1
		if [ "$current" = / ]; then
			current=/$component
		else
			current=$current/$component
		fi
		[ ! -L "$current" ] || return 1
	done
}

for xdg_home in "$data_home" "$state_home" "$cache_home"; do
	if ! safe_layout_path "$xdg_home"; then
		echo 'XDG homes must be clean paths below HOME' >&2
		exit 1
	fi
done
if ! safe_layout_path "$binary"; then
	echo "canonical TarLink binary path contains a symlink: $binary" >&2
	exit 1
fi
if ! safe_layout_path "$marker"; then
	echo "TarLink ownership marker path contains a symlink: $marker" >&2
	exit 1
fi

fail_marker() {
	echo "TarLink ownership marker is invalid: $marker" >&2
	exit 1
}

read_marker_digest() {
	test -f "$marker" || fail_marker
	test ! -L "$marker" || fail_marker
	marker_size=$(wc -c < "$marker" | awk '{print $1}') || fail_marker
	test "$marker_size" = 65 || fail_marker
	marker_digest=$(awk '
		NR == 1 { value = $0; next }
		{ invalid = 1 }
		END {
			if (invalid || length(value) != 64 || value !~ /^[0-9a-f]+$/) exit 1
			print value
		}
	' "$marker") || fail_marker
}

command -v sha256sum >/dev/null 2>&1 || {
	echo 'sha256sum is required' >&2
	exit 1
}

verify_owned_binary() {
	test -f "$binary" || {
		echo "tarlink binary is not a regular executable: $binary" >&2
		exit 1
	}
	test ! -L "$binary" || {
		echo "tarlink binary must not be a symlink: $binary" >&2
		exit 1
	}
	test -x "$binary" || {
		echo "tarlink binary is not a regular executable: $binary" >&2
		exit 1
	}
	read_marker_digest
	binary_digest=$(sha256sum "$binary" | awk '{print $1}') || fail_marker
	test "$binary_digest" = "$marker_digest" || {
		echo "TarLink ownership marker does not match $binary" >&2
		exit 1
	}
}

fail_missing() {
	echo "tarlink binary not found at $binary; refusing to use PATH" >&2
	exit 1
}

if [ -L "$binary" ]; then
	echo "tarlink binary must not be a symlink: $binary" >&2
	exit 1
fi
if [ ! -e "$binary" ]; then
	if [ -e "$marker" ] || [ -L "$marker" ]; then
		fail_missing
	fi
	path_binary=$(PATH=${PATH-} command -v tarlink 2>/dev/null || :)
	if [ -n "$path_binary" ] && [ "$path_binary" != "$binary" ]; then
		fail_missing
	fi
	for product in "$data_home/tarlink" "$state_home/tarlink" "$cache_home/tarlink"; do
		if [ -e "$product" ] || [ -L "$product" ]; then
			fail_missing
		fi
	done
	printf 'TarLink is not installed.\n'
	exit 0
fi
verify_owned_binary

expected_digest=$binary_digest

printf 'Uninstalling TarLink...\n'
if ! "$binary" uninstall --all; then
	echo 'uninstall.sh: TarLink uninstall failed' >&2
	exit 1
fi

verify_owned_binary
test "$binary_digest" = "$expected_digest" || {
	echo "tarlink binary changed during uninstall: $binary" >&2
	exit 1
}
rm "$marker"
rm "$binary"

for product in "$data_home/tarlink" "$state_home/tarlink" "$cache_home/tarlink"; do
	safe_layout_path "$product" || continue
	rmdir "$product" 2>/dev/null || :
done

printf 'TarLink uninstalled successfully.\n'
