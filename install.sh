#!/bin/sh

set -eu

repository='https://github.com/drobilica/tarlink'
bin_dir=${HOME:?HOME must be set}/.local/bin

fail() {
	printf 'install.sh: %s\n' "$1" >&2
	exit 1
}

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

mkdir -p "$bin_dir"
tmp_dir=$(mktemp -d "$bin_dir/.tarlink-install.XXXXXXXX") || fail 'could not create a temporary install directory'

cleanup() {
	status=$?
	rm -rf "$tmp_dir"
	exit "$status"
}

trap cleanup EXIT
trap 'exit 1' HUP INT TERM

download() {
	curl -q --fail --location --max-redirs 5 --connect-timeout 15 --max-time 600 --silent --show-error --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$2" "$1"
}

checksums="$tmp_dir/checksums.txt"
binary="$tmp_dir/$asset"
download "$base_url/checksums.txt" "$checksums" || fail 'could not download checksums.txt'
download "$base_url/$asset" "$binary" || fail "could not download $asset"
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
target="$bin_dir/tarlink"
test ! -d "$target" || fail "$target is a directory"
mv -f "$binary" "$target" || fail "could not install $target"

printf 'Installed TarLink at %s\n' "$target"
case ":${PATH:-}:" in
	*":$bin_dir:"*) ;;
	*) printf 'Warning: %s is not in PATH; add it to your shell configuration to run tarlink directly.\n' "$bin_dir" >&2 ;;
esac
