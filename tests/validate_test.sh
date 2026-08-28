#!/usr/bin/env bash

set -euo pipefail

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cd "$repo_dir"
# shellcheck source=../scripts/validate.sh
source "$repo_dir/scripts/validate.sh"

first_image=$(validation_image)
second_image=$(validation_image)
test "$first_image" = "$second_image"
case "$first_image" in
	ghcr.io/drobilica/tarlink-validation:go1.26.0-????????????????) ;;
	*) printf 'unexpected validation image: %s\n' "$first_image" >&2; exit 1 ;;
esac

fake_bin=$tmp/bin
mkdir -p "$fake_bin"
cat >"$fake_bin/uname" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' Darwin
EOF
cat >"$fake_bin/podman" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$PODMAN_LOG"
case "$1" in
  info) exit 0 ;;
  image) exit 0 ;;
  pull) printf '%s\n' 'pull must not run for an exact local image' >&2; exit 99 ;;
  run) exit 0 ;;
  *) exit 1 ;;
esac
EOF
chmod 0755 "$fake_bin/uname" "$fake_bin/podman"

PODMAN_LOG=$tmp/reuse.log PATH="$fake_bin:$PATH" TARLINK_VALIDATE_IMAGE=test/image:exact \
"$repo_dir/scripts/validate.sh" --quick
! grep -F 'pull test/image:exact' "$tmp/reuse.log" >/dev/null
grep -F -- '--mount type=volume,source=tarlink-validation-gocache,target=/root/.cache/go-build' "$tmp/reuse.log" >/dev/null
grep -F -- '-e GOCACHE=/root/.cache/go-build' "$tmp/reuse.log" >/dev/null
grep -F -- 'test/image:exact bash -c' "$tmp/reuse.log" >/dev/null

cat >"$fake_bin/podman" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$PODMAN_LOG"
case "$1" in
  info) exit 0 ;;
  image) exit 1 ;;
  pull) exit 1 ;;
  build) test "$2" = --tag; exit 0 ;;
  run) exit 0 ;;
  *) exit 1 ;;
esac
EOF
chmod 0755 "$fake_bin/podman"
PODMAN_LOG=$tmp/fallback.log PATH="$fake_bin:$PATH" TARLINK_VALIDATE_IMAGE=test/image:fallback \
	"$repo_dir/scripts/validate.sh" --quick
grep -F 'pull test/image:fallback' "$tmp/fallback.log" >/dev/null
grep -F 'build --tag test/image:fallback --file ' "$tmp/fallback.log" >/dev/null

if "$repo_dir/scripts/validate.sh" --quick extra 2>"$tmp/usage"; then
	printf '%s\n' 'extra argument unexpectedly accepted' >&2
	exit 1
fi
grep -F 'usage: ./scripts/validate.sh [--quick]' "$tmp/usage" >/dev/null

cat >"$fake_bin/gofmt" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' 'cmd/tarlink/bad.go'
EOF
cat >"$fake_bin/podman" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
	info | machine) exit 1 ;;
	*) exit 1 ;;
esac
EOF
chmod 0755 "$fake_bin/gofmt" "$fake_bin/podman"

if env -u GITHUB_ACTIONS PATH="$fake_bin:$PATH" \
	"$repo_dir/scripts/validate.sh" --quick >"$tmp/local.out" 2>"$tmp/local.err"; then
	printf '%s\n' 'unformatted sources unexpectedly passed validation' >&2
	exit 1
fi
grep -F '==> Formatting (gofmt)' "$tmp/local.out" >/dev/null
grep -F 'unformatted Go files:' "$tmp/local.err" >/dev/null
grep -F 'cmd/tarlink/bad.go' "$tmp/local.err" >/dev/null
grep -F 'validation failed in phase: Formatting (gofmt) (exit 1)' "$tmp/local.err" >/dev/null
if grep -q '^::' "$tmp/local.out"; then
	printf '%s\n' 'local validation must not emit GitHub Actions workflow commands' >&2
	exit 1
fi

if GITHUB_ACTIONS=true PATH="$fake_bin:$PATH" \
	"$repo_dir/scripts/validate.sh" --quick >"$tmp/gha.out" 2>"$tmp/gha.err"; then
	printf '%s\n' 'unformatted sources unexpectedly passed validation' >&2
	exit 1
fi
grep -F '::group::Formatting (gofmt)' "$tmp/gha.out" >/dev/null
grep -F '::error::canonical validation failed in phase: Formatting (gofmt) (exit 1)' "$tmp/gha.out" >/dev/null
grep -F 'validation failed in phase: Formatting (gofmt) (exit 1)' "$tmp/gha.err" >/dev/null

cat >"$fake_bin/gofmt" <<'EOF'
#!/usr/bin/env bash
exit 2
EOF
chmod 0755 "$fake_bin/gofmt"

status=0
env -u GITHUB_ACTIONS PATH="$fake_bin:$PATH" \
	"$repo_dir/scripts/validate.sh" --quick >"$tmp/crash.out" 2>"$tmp/crash.err" || status=$?
test "$status" = 2
grep -F 'validation failed in phase: Formatting (gofmt) (exit 2)' "$tmp/crash.err" >/dev/null

cat >"$fake_bin/gofmt" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$fake_bin/go" <<'EOF'
#!/usr/bin/env bash
case "$1" in
	vet) exit 0 ;;
	test) exit 3 ;;
	*) exit 0 ;;
esac
EOF
chmod 0755 "$fake_bin/gofmt" "$fake_bin/go"

status=0
env -u GITHUB_ACTIONS PATH="$fake_bin:$PATH" \
	"$repo_dir/scripts/validate.sh" --quick >"$tmp/status.out" 2>"$tmp/status.err" || status=$?
test "$status" = 3
grep -F 'validation failed in phase: Tests (go test) (exit 3)' "$tmp/status.err" >/dev/null

cat >"$fake_bin/podman" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
	info) exit 0 ;;
	image) exit 0 ;;
	run) exit 7 ;;
	*) exit 1 ;;
esac
EOF
chmod 0755 "$fake_bin/podman"

status=0
env -u GITHUB_ACTIONS PATH="$fake_bin:$PATH" TARLINK_VALIDATE_IMAGE=test/image:phases \
	"$repo_dir/scripts/validate.sh" --quick >"$tmp/podman.out" 2>"$tmp/podman.err" || status=$?
test "$status" = 7
grep -F 'validation failed in phase: Linux validation via Podman (exit 7)' "$tmp/podman.err" >/dev/null

printf '%s\n' 'validation script tests passed'
