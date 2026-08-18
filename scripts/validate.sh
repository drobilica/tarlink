#!/usr/bin/env bash

set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repo_root"

run_checks() {
	if command -v desktop-file-validate >/dev/null 2>&1; then
		:
	else
		printf '%s\n' 'warning: desktop-file-validate is unavailable; integration tests may skip desktop validation' >&2
	fi
	test -z "$(gofmt -l .)"
	go vet ./...
	go test ./...
	./tests/release_notes_test.sh
	./tests/release_workflow_test.sh
	./tests/install_test.sh
	./tests/uninstall_test.sh
	go test -race ./...
	CGO_ENABLED=0 go build ./...
}

run_host_checks() {
	test -z "$(gofmt -l .)"
	go vet ./internal/checksum ./internal/manifest ./docs
	go test ./internal/checksum ./internal/manifest ./docs
	./tests/release_notes_test.sh
	./tests/release_workflow_test.sh
}

case "$(uname -s)" in
	Linux)
		run_checks
		;;
	Darwin)
		printf '%s\n' 'macOS detected: running host-compatible Go validation.'
		test -n "${TARLINK_VALIDATE_IN_CONTAINER:-}" || {
			test -n "$(command -v go || true)" || {
				printf '%s\n' 'Go is required for host-compatible validation.' >&2
				exit 1
			}
			run_host_checks
		}

		if ! command -v podman >/dev/null 2>&1; then
			printf '%s\n' 'Podman is unavailable; Linux-specific validation will be covered by Ubuntu GitHub Actions.' >&2
			exit 0
		fi

		if ! podman info >/dev/null 2>&1; then
			printf '%s\n' 'Podman is installed but not running; starting the existing Podman machine.'
			podman machine start
		fi

		go_version=$(awk '$1 == "go" { print $2; exit }' go.mod)
		test -n "$go_version"
		printf '%s\n' "macOS detected: running Linux validation in golang:${go_version}-bookworm via Podman."
		podman run --rm \
			-v "$repo_root:/src" \
			-w /src \
			-e HOME=/tmp/tarlink-home \
			-e XDG_CONFIG_HOME=/tmp/tarlink-home/config \
			-e XDG_DATA_HOME=/tmp/tarlink-home/data \
			-e XDG_STATE_HOME=/tmp/tarlink-home/state \
			-e XDG_CACHE_HOME=/tmp/tarlink-home/cache \
			-e TARLINK_VALIDATE_IN_CONTAINER=1 \
			"golang:${go_version}-bookworm" \
			bash -c 'apt-get update >/dev/null && apt-get install --no-install-recommends -y desktop-file-utils jq >/dev/null && mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" "$XDG_CACHE_HOME" && /src/scripts/validate.sh'
		;;
	*)
		printf 'unsupported host OS: %s\n' "$(uname -s)" >&2
		exit 1
		;;
esac
