#!/bin/sh
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/.."
if [ "${1:-}" = "--check" ]; then
	[ "$#" -eq 1 ] || { echo 'usage: ./scripts/update-cli-readme.sh [--check]' >&2; exit 2; }
	exec go run ./cmd/cli-reference --check cli/README.md
fi
[ "$#" -eq 0 ] || { echo 'usage: ./scripts/update-cli-readme.sh [--check]' >&2; exit 2; }
exec go run ./cmd/cli-reference cli/README.md
