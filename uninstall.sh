#!/bin/sh
set -eu

binary=${HOME:?HOME must be set}/.local/bin/tarlink
if [ -L "$binary" ] || [ ! -f "$binary" ] || [ ! -x "$binary" ]; then
	echo "tarlink binary not found at $binary" >&2
	exit 1
fi

"$binary" uninstall --all
rm -- "$binary"
