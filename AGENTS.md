# Repository instructions

TarLink is a rootless Linux amd64 application manager. Preserve the approved architecture and security contract.

## Boundaries

- Production code must remain pure Go with `CGO_ENABLED=0` support.
- Do not import `os/exec`, `unsafe`, `plugin`, or `C`; do not invoke external commands.
- Archive symlinks are limited permanently to validated same-directory, single-component targets whose complete chains end at extracted regular files. Do not broaden this policy or add hardlinks, hooks, arbitrary commands/arguments, custom install destinations, telemetry, daemon behavior, plugins, self-update, or system dependencies.
- Accept only the official registry, HTTPS release sources, SHA-256 verification, and tar.gz/tar.xz/zip archives.
- Preserve archive limits, path validation, atomic symlink/state generation, and current-plus-one-previous retention.

## Editing and validation

Keep edits within the requested scope and preserve unrelated work. Use `apply_patch` for file edits. Run `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...`, and `CGO_ENABLED=0 go build ./...` when the Go toolchain is available. Never commit from an agent task.

Security-sensitive changes require tests and a short threat-boundary explanation. Documentation must describe behavior that exists or is explicitly marked planned; do not invent registry entries, hashes, or commands.

## Documentation and registry

Apache-2.0 is canonical. Keep `NOTICE` and dependency review information current. Registry manifests are strict manifest v1 and must use authoritative upstream URLs and exact lowercase SHA-256 values.
