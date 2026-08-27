# Production implementation policy

These rules apply to production implementation below `internal/`.

## Runtime boundary

* Production code remains pure Go and compatible with `CGO_ENABLED=0`.
* Do not import `os/exec`, `unsafe`, `plugin`, or `C`, and never execute external commands.
* Keep business, registry, download, extraction, integration, ownership, state, and lifecycle behavior in `internal/`; frontends must consume these services rather than reimplement them.
* Do not create runtime extension points, hidden command execution, package-manager dependencies, or alternate install destinations.

## Safe I/O and lifecycle

* Keep network reads and redirects HTTPS-only, timed, and bounded by the existing resource limits.
* Verify exact bytes before materialization. Preserve archive type, path, entry, size, depth, special-file, hardlink, and same-directory regular-file symlink-chain checks.
* Stage mutations privately and publish through same-filesystem atomic replacement with directory flushes where the existing contract requires them.
* Preserve lifecycle and narrower lock ordering, explicit ownership validation, rollback on publication failure, and current-plus-one-previous retention.
* Treat cache and discovery data as disposable, never as an authority that can bypass the official registry or verified state.

## Testing

Changes to security or lifecycle behavior require focused success and hostile or
failure-path regression tests. Prefer injected clocks, clients, and filesystem
fixtures over timing or external-network dependence.
