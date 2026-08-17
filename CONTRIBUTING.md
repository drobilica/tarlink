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

Registry updates must use authoritative upstream HTTPS release and checksum-source URLs plus the exact lowercase digest for the algorithm upstream publishes. SHA-256 and SHA-512 are supported; do not substitute an algorithm or derive a replacement digest. Validate the registry with TarLink itself: `tarlink registry validate .`.

## Review expectations

Reviewers should check path containment, symlink behavior, resource bounds, cancellation, atomicity, and deterministic output. A change that broadens the trust boundary needs an explicit design proposal before implementation.

Changes to archive extraction, manifest parsing, official-registry trust, filesystem deletion, locking, state, download verification, or atomic activation are security-sensitive and must include focused success, hostile-input, and failure-path tests.
