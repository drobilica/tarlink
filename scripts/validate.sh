#!/usr/bin/env bash

set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

usage() {
	printf '%s\n' 'usage: ./scripts/validate.sh [--quick]' >&2
}

current_phase=setup

begin_phase() {
	current_phase=$1
	if [[ -n ${GITHUB_ACTIONS:-} ]]; then
		printf '::group::%s\n' "$1"
	else
		printf '==> %s\n' "$1"
	fi
}

end_phase() {
	if [[ -n ${GITHUB_ACTIONS:-} ]]; then
		printf '::endgroup::\n'
	fi
}

on_error() {
	local status=$?
	if [[ -n ${GITHUB_ACTIONS:-} ]]; then
		printf '::error::canonical validation failed in phase: %s (exit %d)\n' "$current_phase" "$status"
	fi
	printf '%s\n' "validation failed in phase: $current_phase (exit $status)" >&2
	exit "$status"
}

phase_format_go() {
	begin_phase 'Formatting (gofmt)'
	local unformatted
	unformatted=$(gofmt -l .)
	if [[ -n $unformatted ]]; then
		printf 'unformatted Go files:\n%s\n' "$unformatted" >&2
		return 1
	fi
	end_phase
}

phase_vet() {
	begin_phase 'Vet (go vet)'
	go vet "$@"
	end_phase
}

phase_go_tests() {
	begin_phase 'Tests (go test)'
	go test "$@"
	end_phase
}

phase_script() {
	begin_phase "$1"
	shift
	"$@"
	end_phase
}

phase_race_tests() {
	begin_phase 'Race tests (go test -race)'
	go test -race ./...
	end_phase
}

phase_build() {
	begin_phase 'Build (go build)'
	CGO_ENABLED=0 go build ./...
	end_phase
}

go_version_from_mod() {
	while read -r directive value _; do
		if [ "$directive" = go ]; then
			printf '%s\n' "$value"
			return 0
		fi
	done < go.mod
	return 1
}

dependency_fingerprint() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum build/validation/Containerfile go.mod go.sum | sha256sum | cut -c1-16
	else
		shasum -a 256 build/validation/Containerfile go.mod go.sum | shasum -a 256 | cut -c1-16
	fi
}

validation_image() {
	local go_version=${1:-$(go_version_from_mod)}
	printf '%s\n' "ghcr.io/drobilica/tarlink-validation:go${go_version}-$(dependency_fingerprint)"
}

run_quick() {
	phase_format_go
	phase_go_tests ./...
}

run_checks() {
	if command -v desktop-file-validate >/dev/null 2>&1; then
		:
	else
		printf '%s\n' 'warning: desktop-file-validate is unavailable; integration tests may skip desktop validation' >&2
	fi
	phase_format_go
	phase_vet ./...
	phase_go_tests ./...
	phase_script 'Release notes contract' ./tests/release_notes_test.sh
	phase_script 'Release workflow contract' ./tests/release_workflow_test.sh
	phase_script 'Validation script self-tests' ./tests/validate_test.sh
	phase_script 'Installer tests' ./tests/install_test.sh
	phase_script 'Uninstaller tests' ./tests/uninstall_test.sh
	phase_race_tests
	phase_build
}

run_host_checks() {
	phase_format_go
	phase_vet ./internal/checksum ./internal/manifest ./docs
	phase_go_tests ./internal/checksum ./internal/manifest ./docs
	phase_script 'Release notes contract' ./tests/release_notes_test.sh
	phase_script 'Release workflow contract' ./tests/release_workflow_test.sh
	phase_script 'Validation script self-tests' ./tests/validate_test.sh
}

build_validation_image() {
	local image=$1
	printf '%s\n' "Validation image unavailable locally and in GHCR; building $image." >&2
	podman build --tag "$image" --file "$repo_root/build/validation/Containerfile" "$repo_root"
}

select_validation_image() {
	local image=$1
	if podman image exists "$image" >/dev/null 2>&1; then
		printf '%s\n' "Reusing local validation image $image." >&2
	elif ! podman pull "$image" >/dev/null; then
		build_validation_image "$image"
	fi
}

run_podman_checks() {
	local mode=$1
	local go_version
	local image
	go_version=$(go_version_from_mod)
	image=${TARLINK_VALIDATE_IMAGE:-$(validation_image "$go_version")}
	select_validation_image "$image"
	printf '%s\n' "macOS detected: running Linux validation in $image via Podman."
	podman run --rm \
		--mount "type=volume,source=tarlink-validation-gocache,target=/root/.cache/go-build" \
		-v "$repo_root:/src" \
		-w /src \
		-e HOME=/root \
		-e GOCACHE=/root/.cache/go-build \
		-e XDG_CONFIG_HOME=/tmp/tarlink-home/config \
		-e XDG_DATA_HOME=/tmp/tarlink-home/data \
		-e XDG_STATE_HOME=/tmp/tarlink-home/state \
		-e XDG_CACHE_HOME=/tmp/tarlink-home/cache \
		"$image" \
		bash -c 'mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME" "$XDG_CACHE_HOME" && if [ "$1" = quick ]; then /src/scripts/validate.sh --quick; else /src/scripts/validate.sh; fi' bash "$mode"
}

main() {
	local mode=full
	case "${1:-}" in
		'') ;;
		--quick) mode=quick; shift; (($# == 0)) || { usage; exit 2; } ;;
		*) usage; exit 2 ;;
	esac
	cd "$repo_root"

	case "$(uname -s)" in
		Linux)
			if [ "$mode" = quick ]; then run_quick; else run_checks; fi
			;;
		Darwin)
			if command -v podman >/dev/null 2>&1; then
				if ! podman info >/dev/null 2>&1; then
					printf '%s\n' 'Podman is installed but not running; starting the existing Podman machine.'
					podman machine start || printf '%s\n' 'warning: unable to start the existing Podman machine.' >&2
				fi
				if podman info >/dev/null 2>&1; then
					phase_script 'Linux validation via Podman' run_podman_checks "$mode"
					return
				fi
			fi
			printf '%s\n' 'Podman is unavailable; running host-compatible macOS checks. Linux-specific validation will be covered by Ubuntu GitHub Actions.' >&2
			command -v go >/dev/null 2>&1 || { printf '%s\n' 'Go is required for host-compatible validation.' >&2; exit 1; }
			if [ "$mode" = quick ]; then run_quick; else run_host_checks; fi
			;;
		*)
			printf 'unsupported host OS: %s\n' "$(uname -s)" >&2
			exit 1
			;;
	esac
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	set -E
	trap on_error ERR
	main "$@"
fi
