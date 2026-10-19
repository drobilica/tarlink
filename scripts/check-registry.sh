#!/usr/bin/env bash
#
# Run the registry checker against a registry tree.
#
# On macOS the checker runs inside Podman. The container's /tmp is a small
# tmpfs, so a realistic multi-application audit can exhaust it. The macOS
# branch therefore stages on the host disk: it creates a host temporary
# directory, bind-mounts it at /check-tmp, and points TMPDIR there so the
# checker's isolated home and artifact cache live on the host filesystem.
# Set TARLINK_CHECK_TMPDIR to a caller-owned directory to stage there instead of
# a fresh temporary directory; it is used as-is and never deleted. Each run
# still uses its own isolated session home, so verified artifacts are reused
# within a run but not across runs.

set -euo pipefail

usage() {
	printf '%s\n' 'usage: ./scripts/check-registry.sh <registry> [--app <id> [--release <version> ...] | --changed-from <git-sha> | --all-artifacts]' >&2
}

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

if (($# < 1)); then usage; exit 2; fi
registry_root=$(CDPATH= cd -- "$1" && pwd)
shift

if [ "${TARLINK_CHECK_IN_CONTAINER:-}" != 1 ] && [ "$(uname -s)" = Darwin ]; then
	command -v podman >/dev/null 2>&1 || { printf '%s\n' 'Podman is required for registry checking on macOS.' >&2; exit 1; }
	registry_root=$(CDPATH= cd -- "$registry_root" && pwd)
	go_version=$(go env GOVERSION)
	go_version=${go_version#go}
	if [ -n "${TARLINK_CHECK_TMPDIR:-}" ]; then
		mkdir -p -- "$TARLINK_CHECK_TMPDIR"
		check_tmpdir=$(CDPATH= cd -- "$TARLINK_CHECK_TMPDIR" && pwd)
	else
		check_tmpdir=$(mktemp -d)
		trap 'rm -rf -- "$check_tmpdir"' EXIT
	fi
	podman run --rm \
		-v "$repo_root:/src" \
		-v "$registry_root:/registry" \
		-v "$check_tmpdir:/check-tmp" \
		-w /src \
		-e TARLINK_CHECK_IN_CONTAINER=1 \
		-e TMPDIR=/check-tmp \
		"golang:${go_version}-bookworm" \
		bash -c '/src/scripts/check-registry.sh /registry "$@"' bash "$@"
	exit $?
fi

mode=()
case "${1:-}" in
	'') ;;
	--app)
		(($# >= 2)) || { usage; exit 2; }
		mode=(--app "$2")
		shift 2
		while (($# > 0)); do
			if [ "$1" != --release ] || (($# < 2)); then
				usage
				exit 2
			fi
			mode+=(--release "$2")
			shift 2
		done
		;;
	--all-artifacts)
		(($# == 1)) || { usage; exit 2; }
		mode=(--all-artifacts)
		;;
	--changed-from)
		(($# == 2)) || { usage; exit 2; }
		old_root=$(mktemp -d)
		trap 'rm -rf -- "$old_root"' EXIT
		baseline=$(git -C "$registry_root" rev-parse --verify --end-of-options "${2}^{commit}")
		git -C "$registry_root" archive "$baseline" apps runtimes | tar -x -C "$old_root"
		mode=(--old-root "$old_root")
		;;
	*) usage; exit 2;;
esac

cd "$repo_root"
if ((${#mode[@]})); then
	go run ./cmd/tarlink-registry-check "${mode[@]}" "$registry_root"
else
	go run ./cmd/tarlink-registry-check "$registry_root"
fi
