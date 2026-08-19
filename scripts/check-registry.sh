#!/usr/bin/env bash

set -euo pipefail

usage() {
	printf '%s\n' 'usage: ./scripts/check-registry.sh <registry> [--app <id> | --changed-from <git-sha> | --all-artifacts]' >&2
}

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

if (($# < 1)); then usage; exit 2; fi
registry_root=$1
shift

if [ "${TARLINK_CHECK_IN_CONTAINER:-}" != 1 ] && [ "$(uname -s)" = Darwin ]; then
	command -v podman >/dev/null 2>&1 || { printf '%s\n' 'Podman is required for registry checking on macOS.' >&2; exit 1; }
	registry_root=$(CDPATH= cd -- "$registry_root" && pwd)
	go_version=$(go env GOVERSION)
	go_version=${go_version#go}
	podman run --rm \
		-v "$repo_root:/src" \
		-v "$registry_root:/registry" \
		-w /src \
		-e TARLINK_CHECK_IN_CONTAINER=1 \
		"golang:${go_version}-bookworm" \
		bash -c '/src/scripts/check-registry.sh /registry "$@"' bash "$@"
	exit $?
fi

mode=()
case "${1:-}" in
	'') ;;
	--app)
		(($# == 2)) || { usage; exit 2; }
		mode=(--app "$2")
		;;
	--all-artifacts)
		(($# == 1)) || { usage; exit 2; }
		mode=(--all-artifacts)
		;;
	--changed-from)
		(($# == 2)) || { usage; exit 2; }
		old_root=$(mktemp -d)
		trap 'rm -rf -- "$old_root"' EXIT
		git -C "$registry_root" archive "$2" apps | tar -x -C "$old_root"
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
