#!/bin/sh

set -eu

repository='https://github.com/drobilica/tarlink'

fail() {
	printf 'install.sh: %s\n' "$1" >&2
	exit 1
}

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

home=${HOME:?HOME must be set}
valid_path_text "$home" || fail 'HOME must not contain control characters'
case "$home" in
	/*) ;;
	*) fail 'HOME must be an absolute path' ;;
esac
case "$home" in
	/) ;;
	*'//'*) fail 'HOME must be an absolute, clean path' ;;
	*/) fail 'HOME must be an absolute, clean path' ;;
	*/./*|*/.|./*|.) fail 'HOME must be an absolute, clean path' ;;
	*/../*|*/..|../*|..) fail 'HOME must be an absolute, clean path' ;;
esac

safe_path() {
	path=$1
	case "$path" in
		/*) ;;
		*) return 1 ;;
	esac
	case "$path" in
		*'//'*) return 1 ;;
		*/) [ "$path" = / ] || return 1 ;;
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

bin_dir=$home/.local/bin
target=$bin_dir/tarlink
state_home=${XDG_STATE_HOME:-$home/.local/state}
valid_path_text "$state_home" || fail 'XDG_STATE_HOME must not contain control characters'
case "$state_home" in
	"$home"|"$home"/*) ;;
	*) fail 'XDG_STATE_HOME must be an absolute, clean path below HOME' ;;
esac
safe_path "$bin_dir" || fail "canonical TarLink binary path contains a symlink: $bin_dir"
safe_path "$state_home" || fail "XDG_STATE_HOME contains a symlink: $state_home"
marker_dir=$state_home/tarlink
marker=$marker_dir/install.sha256
safe_path "$marker" || fail "TarLink ownership marker path contains a symlink: $marker"

command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'

read_marker_digest() {
	marker_path=$1
	test -f "$marker_path" || fail "TarLink ownership marker is not a regular file: $marker_path"
	test ! -L "$marker_path" || fail "TarLink ownership marker must not be a symlink: $marker_path"
	marker_size=$(wc -c < "$marker_path" | awk '{print $1}') || fail "could not read TarLink ownership marker: $marker_path"
	test "$marker_size" = 65 || fail "TarLink ownership marker is malformed: $marker_path"
	marker_digest=$(awk '
		NR == 1 { value = $0; next }
		{ invalid = 1 }
		END {
			if (invalid || length(value) != 64 || value !~ /^[0-9a-f]+$/) exit 1
			print value
		}
	' "$marker_path") || fail "TarLink ownership marker is malformed: $marker_path"
}

verify_owned_target() {
	target_path=$1
	marker_path=$2
	test ! -L "$target_path" || fail "canonical TarLink binary must not be a symlink: $target_path"
	test -f "$target_path" || fail "canonical TarLink binary is not a regular file: $target_path"
	test -x "$target_path" || fail "canonical TarLink binary is not executable: $target_path"
	read_marker_digest "$marker_path"
	target_digest=$(sha256sum "$target_path" | awk '{print $1}') || fail "could not hash canonical TarLink binary: $target_path"
	test "$target_digest" = "$marker_digest" || fail "TarLink ownership marker does not match $target_path"
}

if [ -e "$target" ] || [ -L "$target" ]; then
	verify_owned_target "$target" "$marker"
elif [ -e "$marker" ] || [ -L "$marker" ]; then
	fail "TarLink ownership marker exists without its canonical binary: $marker"
fi

usage() {
	printf 'Usage: %s [RELEASE]\n' "$0" >&2
	exit 2
}

if [ "$#" -gt 1 ]; then
	usage
fi

release=${TARLINK_VERSION:-latest}
if [ "$#" -eq 1 ]; then
	release=$1
fi

case "$release" in
	'')
		fail 'release must not be empty'
		;;
	*[!A-Za-z0-9._-]*)
		fail 'release may contain only letters, numbers, dots, underscores, and hyphens'
		;;
esac

case "$(uname -s)" in
	Linux) ;;
	*) fail 'Linux is the only supported operating system' ;;
esac

case "$(uname -m)" in
	x86_64|amd64) architecture=amd64 ;;
	aarch64|arm64) architecture=arm64 ;;
	*) fail 'unsupported architecture (expected amd64 or arm64)' ;;
esac

asset="tarlink-linux-$architecture"
if [ "$release" = latest ]; then
	release_path='latest/download'
else
	release_path="download/$release"
fi
base_url="$repository/releases/$release_path"

printf 'Installing TarLink version: %s\n' "$release"

mkdir -p "$bin_dir"
safe_path "$bin_dir" || fail "canonical TarLink binary path contains a symlink: $bin_dir"
tmp_dir=$(mktemp -d "$bin_dir/.tarlink-install.XXXXXXXX") || fail 'could not create a temporary install directory'
marker_tmp=
previous_binary=
publish_started=0
previous_target_moved=0
new_binary_published=0
marker_published=0
preserve_tmp=0
actual_hash=

remove_new_binary() {
	if [ ! -e "$target" ] && [ ! -L "$target" ]; then
		return 0
	fi
	if [ -L "$target" ] || [ ! -f "$target" ]; then
		return 1
	fi
	current_hash=$(sha256sum "$target" | awk '{print $1}') || return 1
	if [ "$current_hash" != "$actual_hash" ]; then
		return 1
	fi
	rm "$target"
}

rollback_publish() {
	rollback_ok=1
	if [ "$new_binary_published" -eq 1 ]; then
		if ! remove_new_binary; then
			rollback_ok=0
		fi
	fi
	if [ "$previous_target_moved" -eq 1 ]; then
		if [ "$rollback_ok" -eq 1 ] && [ -e "$previous_binary" ] && [ ! -L "$previous_binary" ] && [ ! -e "$target" ] && [ ! -L "$target" ]; then
			if ! mv "$previous_binary" "$target"; then
				rollback_ok=0
			fi
		else
			rollback_ok=0
		fi
	fi
	[ "$rollback_ok" -eq 1 ]
}

remove_marker_tmp() {
	[ -n "$marker_tmp" ] || return 0
	if ! safe_path "$marker_tmp" || [ -L "$marker_tmp" ]; then
		printf 'install.sh: refusing to remove unsafe temporary ownership marker: %s\n' "$marker_tmp" >&2
		return 1
	fi
	if [ -e "$marker_tmp" ]; then
		if [ ! -f "$marker_tmp" ] || ! rm "$marker_tmp"; then
			printf 'install.sh: could not remove temporary ownership marker: %s\n' "$marker_tmp" >&2
			return 1
		fi
	fi
	marker_tmp=
}

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		if ! remove_marker_tmp; then
			status=1
		fi
		if [ "$publish_started" -eq 1 ] && [ "$marker_published" -eq 0 ]; then
			if ! rollback_publish; then
				preserve_tmp=1
				printf 'install.sh: could not restore the previous installation; temporary files retained at %s\n' "$tmp_dir" >&2
			fi
		fi
	fi
	if [ "$preserve_tmp" -eq 0 ]; then
		if ! rm -rf "$tmp_dir"; then
			status=1
		fi
	else
		printf 'install.sh: restore manually if needed: %s\n' "$tmp_dir" >&2
	fi
	exit "$status"
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

download() {
	progress_option=--silent
	if [ "${3:-}" = binary ] && [ -t 2 ]; then
		progress_option=--progress-bar
	fi
	curl -q --fail --location --max-redirs 5 --connect-timeout 15 --max-time 600 "$progress_option" --show-error --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$2" "$1"
}

checksums="$tmp_dir/checksums.txt"
binary="$tmp_dir/$asset"
download "$base_url/checksums.txt" "$checksums" || fail 'could not download checksums.txt'
download "$base_url/$asset" "$binary" binary || fail "could not download $asset"
test -s "$binary" || fail "downloaded $asset is empty"

expected_hash=$(awk -v asset="$asset" '
	NF == 2 && $2 == asset && length($1) == 64 && $1 !~ /[^0-9a-f]/ {
		if (found) duplicate=1
		found=1
		value=$1
	}
	END {
		if (!found || duplicate) exit 1
		print value
	}
' "$checksums") || fail "checksums.txt has no unique lowercase SHA-256 for $asset"

command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'
actual_hash=$(sha256sum "$binary" | awk '{print $1}')
test "$actual_hash" = "$expected_hash" || fail "SHA-256 verification failed for $asset"

chmod 0755 "$binary"
if [ -e "$target" ] || [ -L "$target" ]; then
	verify_owned_target "$target" "$marker"
elif [ -e "$marker" ] || [ -L "$marker" ]; then
	fail "TarLink ownership marker exists without its canonical binary: $marker"
fi

mkdir -p -m 0700 "$marker_dir" || fail "could not create TarLink state directory: $marker_dir"
safe_path "$marker" || fail "TarLink ownership marker path contains a symlink: $marker"
marker_tmp=$(mktemp "$marker_dir/.install.sha256.XXXXXXXX") || fail 'could not create a temporary ownership marker'
printf '%s\n' "$actual_hash" > "$marker_tmp" || fail 'could not write the ownership marker'
chmod 0600 "$marker_tmp" || fail 'could not secure the ownership marker'
publish_started=1
previous_binary="$tmp_dir/previous-$asset"
if [ -e "$target" ] || [ -L "$target" ]; then
	verify_owned_target "$target" "$marker"
	mv "$target" "$previous_binary" || fail "could not stage the previous $target"
	previous_target_moved=1
fi
mv -f "$binary" "$target" || fail "could not install $target"
new_binary_published=1
mv -f "$marker_tmp" "$marker" || fail "could not install the ownership marker"
marker_published=1
marker_tmp=

printf 'TarLink installed successfully.\n'
printf 'Installed TarLink at %s\n' "$target"
case ":${PATH:-}:" in
	*":$bin_dir:"*) ;;
	*) printf 'Warning: %s is not in PATH; add it to your shell configuration to run tarlink directly.\n' "$bin_dir" >&2 ;;
esac
