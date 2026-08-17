# Contributing

Contributions are welcome when they preserve TarLink's narrow, rootless design.

Development requires Go 1.26 or newer within the 1.26 release line. Core, CLI, TUI, tests, and developer utilities are Go; production builds must remain compatible with `CGO_ENABLED=0`.

## Before opening a change

1. Read [AGENTS.md](AGENTS.md), [docs/architecture.md](docs/architecture.md), and [docs/security-model.md](docs/security-model.md).
2. Keep changes scoped. Do not add hooks, arbitrary command execution, custom destinations, telemetry, plugins, a daemon, CGO, or a system dependency.
3. Add regression tests for parser, filesystem, network, archive, registry, and state changes.
4. Run:

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

5. Describe security impact and failure/rollback behavior in the pull request. Pre-1.0 changes should delete obsolete designs rather than add compatibility layers.

## Registry changes

Registry updates must use authoritative upstream HTTPS release and checksum-source URLs plus the exact lowercase SHA-256 digest published by upstream. Non-SHA-256 algorithms, substituted algorithms, and derived replacement digests are not accepted. Store each platform manifest at the strict path `apps/<id>/linux-amd64.yaml` or `apps/<id>/linux-arm64.yaml`; do not add a compatibility filename. Validate the registry with TarLink itself: `tarlink registry validate .`.

Platform availability is explicit. The client resolves its exact `GOOS`/`GOARCH` pair and fails when that variant is absent; it never substitutes another platform. For example, Blender is amd64-only when upstream has no Linux arm64 release, while Godot may publish both variants. Keep shared metadata identical across an application's platform manifests.

## Review expectations

Reviewers should check path containment, symlink behavior, resource bounds, cancellation, atomicity, and deterministic output. A change that broadens the trust boundary needs an explicit design proposal before implementation.

Changes to archive extraction, manifest parsing, official-registry trust, filesystem deletion, locking, state, download verification, or atomic activation are security-sensitive and must include focused success, hostile-input, and failure-path tests.
