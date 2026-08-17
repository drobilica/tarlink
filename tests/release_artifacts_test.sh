#!/bin/sh

set -eu

fail() {
	printf 'release_artifacts_test.sh: %s\n' "$1" >&2
	exit 1
}

if [ "$#" -ne 3 ]; then
	fail 'usage: release_artifacts_test.sh ARTIFACT_DIR RELEASE_VERSION TARGET_ARCH'
fi

artifact_dir=$1
release_version=$2
target_arch=$3

test -d "$artifact_dir" || fail "artifact directory does not exist: $artifact_dir"
test ! -L "$artifact_dir" || fail 'artifact directory must not be a symlink'
case "$target_arch" in
	amd64) binary_name=tarlink-linux-amd64 ;;
	arm64) binary_name=tarlink-linux-arm64 ;;
	*) fail "unsupported target architecture: $target_arch" ;;
esac

expected_names=$(printf '%s\n' checksums.txt tarlink-linux-amd64 tarlink-linux-arm64 | LC_ALL=C sort)
actual_names=$(
	for path in "$artifact_dir"/* "$artifact_dir"/.[!.]* "$artifact_dir"/..?*; do
		if [ -e "$path" ] || [ -L "$path" ]; then
			basename "$path"
		fi
	done | LC_ALL=C sort
)
test "$actual_names" = "$expected_names" || fail 'artifact directory contains unexpected or missing files'

for name in checksums.txt tarlink-linux-amd64 tarlink-linux-arm64; do
	path=$artifact_dir/$name
	test -f "$path" || fail "$name is not a regular file"
	test ! -L "$path" || fail "$name must not be a symlink"
	test -s "$path" || fail "$name is empty"
done

awk '
function known(name) {
	return name == "tarlink-linux-amd64" || name == "tarlink-linux-arm64"
}
NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-f]+$/ || !known($2) || seen[$2]++ {
	bad = 1
	next
}
{ count++ }
END { exit bad || count != 2 }
' "$artifact_dir/checksums.txt" || fail 'checksums.txt is not the exact two-entry lowercase SHA-256 manifest'

(cd "$artifact_dir" && sha256sum --strict --check checksums.txt) || fail 'checksums.txt does not verify both binaries'

for name in tarlink-linux-amd64 tarlink-linux-arm64; do
	description=$(file -b "$artifact_dir/$name")
	case "$name" in
		tarlink-linux-amd64)
			case "$description" in
				*'ELF 64-bit LSB'*'x86-64'*) ;;
				*) fail "$name is not an x86-64 Linux ELF binary: $description" ;;
			esac
			;;
		tarlink-linux-arm64)
			case "$description" in
				*'ELF 64-bit LSB'*'ARM aarch64'*) ;;
				*) fail "$name is not an ARM64 Linux ELF binary: $description" ;;
			esac
			;;
	esac
	test -x "$artifact_dir/$name" || fail "$name is not executable"
	mode=$(stat -c '%a' "$artifact_dir/$name" 2>/dev/null || stat -f '%Lp' "$artifact_dir/$name" 2>/dev/null) || fail "$name mode could not be inspected"
	test "$mode" = 755 || fail "$name must have mode 0755 (got $mode)"
	case "$description" in
		*'statically linked'*) ;;
		*) fail "$name is not statically linked: $description" ;;
	esac
done

target="$artifact_dir/$binary_name"
test_home=$(mktemp -d "${TMPDIR:-/tmp}/tarlink-artifact-test.XXXXXXXX") || fail 'could not create isolated test home'
cleanup() {
	status=$?
	rm -rf "$test_home"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

mkdir -p "$test_home/config" "$test_home/data" "$test_home/state" "$test_home/cache"
version_output=$(HOME="$test_home" XDG_CONFIG_HOME="$test_home/config" XDG_DATA_HOME="$test_home/data" XDG_STATE_HOME="$test_home/state" XDG_CACHE_HOME="$test_home/cache" "$target" version) || fail "$binary_name did not start on the target runner"
test "$version_output" = "tarlink $release_version" || fail "$binary_name reported version $version_output, want tarlink $release_version"
