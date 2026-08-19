# Contributing

Contributions are welcome when they preserve TarLink's narrow, rootless design.

Development requires Go 1.26 or newer within the 1.26 release line. Core, CLI, TUI, tests, and developer utilities are Go; production builds must remain compatible with `CGO_ENABLED=0`.

## Development workflow

After the first checkout, or when starting work from an existing checkout, pull
the current source and make sure it builds before iterating:

```sh
git pull --ff-only
go build ./...
```

Reuse that checkout for subsequent changes. On the first macOS validation,
Podman pulls or builds the versioned Linux validation image and creates a
persistent Go build-cache volume. Subsequent runs reuse both; there is no need
to recreate them for each iteration.

During iteration, use the quick validation path:

```sh
./scripts/validate.sh --quick
```

On macOS, when Podman is available and functioning, this runs the quick checks
once in the Linux validation container rather than duplicating them on the
host. If Podman is unavailable, it runs useful host-compatible checks and the
Linux-specific checks are left to Ubuntu CI.

The quick path is intentionally limited to formatting and ordinary Go tests
for fast feedback. Run the full command before completion; it includes vet,
release and installer checks, the race detector, and the static build.

## Before opening a change

1. Read [AGENTS.md](AGENTS.md), [docs/architecture.md](docs/architecture.md), and [docs/security-model.md](docs/security-model.md).
2. Keep changes scoped. Do not add hooks, arbitrary command execution, custom destinations, telemetry, plugins, a daemon, CGO, or a system dependency. Self-upgrade changes must preserve canonical install ownership, strict stable release filtering, checksum verification, and atomic rollback behavior.
3. Add regression tests for parser, filesystem, network, archive, registry, and state changes.
4. Run the full canonical validation command before considering the change
   complete or opening a pull request:

```sh
./scripts/validate.sh
```

TarLink targets Linux, and development from macOS is supported. The required
full validation is local on Linux and uses Podman for Linux-specific checks on
macOS when available. The `CI` workflow repeats the full validation on
`ubuntu-24.04`; Ubuntu GitHub Actions is the authoritative final Linux
integration environment, so its result must be green for the exact change
being completed.

The validation image is published at
`ghcr.io/drobilica/tarlink-validation` when its `Containerfile`, `go.mod`, or
`go.sum` changes. A repository maintainer must make that GHCR package public
once in GitHub: open the package's **Package settings**, choose **Change
visibility**, select **Public**, and confirm. Until then, anonymous pulls may
fail and the script builds the exact image locally instead.

5. Describe security impact and failure/rollback behavior in the pull request. Pre-1.0 changes should delete obsolete designs rather than add compatibility layers.

## Registry changes

Registry updates must use authoritative upstream HTTPS release and checksum-source URLs plus the exact lowercase SHA-256 or SHA-512 digest published by upstream. Algorithms not supported by the manifest contract, substituted algorithms, and derived replacement digests are not accepted. Store each platform manifest at the strict path `apps/<id>/linux-amd64.yaml` or `apps/<id>/linux-arm64.yaml`; do not add a compatibility filename. Validate the registry with TarLink itself: `tarlink registry validate .`.

Platform availability is explicit. The client resolves its exact `GOOS`/`GOARCH` pair and fails when that variant is absent; it never substitutes another platform. For example, Blender is amd64-only when upstream has no Linux arm64 release, while Godot may publish both variants. Keep shared metadata identical across an application's platform manifests.

## Review expectations

Reviewers should check path containment, symlink behavior, resource bounds, cancellation, atomicity, and deterministic output. A change that broadens the trust boundary needs an explicit design proposal before implementation.

Changes to archive extraction, manifest parsing, official-registry trust, filesystem deletion, locking, state, download verification, or atomic activation are security-sensitive and must include focused success, hostile-input, and failure-path tests.
