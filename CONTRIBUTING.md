# Contributing

Read [AGENTS.md](AGENTS.md), the [architecture](docs/architecture.md), and the
[security model](docs/security-model.md) before changing TarLink. Keep changes
scoped and preserve its rootless trust boundaries. Pre-1.0 changes should remove
obsolete designs rather than add compatibility layers.

## Development and validation

Use Go 1.26 within the 1.26 release line. Production builds remain pure Go and
compatible with `CGO_ENABLED=0`.

```sh
go build ./...
./scripts/validate.sh --quick  # During iteration
./scripts/validate.sh          # Before opening a pull request
```

Quick validation checks formatting, CLI reference freshness, ordinary Go tests,
release asset/workflow contracts, and action pins. Full validation adds vet,
release notes, installer/uninstaller and validation-script tests, the race
detector, and a static build. Regenerate command documentation with
`./scripts/update-cli-readme.sh` when the command tree changes.

Validate natively on Linux. On macOS, the script uses ephemeral Linux Podman
when available and otherwise runs host-compatible checks. Podman reuses a
versioned validation image and Go build-cache volume. The image is pulled from
`ghcr.io/drobilica/tarlink-validation` or built locally if unavailable; a
maintainer must make that GHCR package public for anonymous pulls.

The `CI` workflow runs full validation on Ubuntu 24.04. The exact change must
pass `Canonical validation` before merge through a pull request.

## Registry changes

Use one strict v5 manifest at `apps/<id>/manifest.yaml`. Select official upstream
portable Linux artifacts over HTTPS and record their exact lowercase SHA-256 or
SHA-512 digests. Maintainers may calculate digests locally; upstream checksum
publication is optional. `verification.source` is informational upstream-origin
metadata, not independent authentication.

Declare only supported platform artifact keys. The client resolves its exact
`GOOS`/`GOARCH` pair and refuses an absent key. Single-channel releases omit
channel plumbing. TarLink derives package fingerprints; there is no
manifest-authored revision. See the [manifest contract](docs/manifest-v5.md).

Run `tarlink registry icons . --fix` to discover missing icons and review
unresolved candidates manually. Icons must be archive-contained or verified
HTTPS PNG/SVG resources with exact lowercase SHA-256 digests. Then run
`tarlink registry validate .` and the changed-artifact lifecycle check described
in the [registry workflow](docs/registry-research.md) before review.

## Review

Describe security impact and failure/rollback behavior in the pull request.
Check containment, symlinks, resource bounds, cancellation, atomicity, ownership,
and deterministic output. Changes to parsing, extraction, trust, deletion,
locking, state, download verification, or activation require focused success,
hostile-input, and failure-path regression tests. Trust-boundary changes require
an explicit design proposal before implementation.

Minor releases receive a source dead-code audit; routine patches do not require
one. Dependency changes require the review described in
[Dependency review](docs/dependencies.md).
