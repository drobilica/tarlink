TarLink turns portable Linux application archives into managed applications.

It replaces manual extraction and ad-hoc PATH setup with rootless, versioned installs, stable executable links, desktop integration, explicit updates, and nearly instantaneous rollback. Its initial catalog focuses on game-development tools, emulators, and graphics/content tools.

TarLink is deliberately constrained: manifests cannot execute shell commands or arbitrary code, releases come only from the official reviewed registry, and the client writes only to its XDG directories and narrowly named user integrations. TarLink does not install system dependencies. It verifies the reviewed artifact bytes but does not sandbox or guarantee the security of the upstream application at runtime.

## Status

v0.1 supports Linux amd64 only. Other operating systems and architectures are rejected. The official registry contains Blender 5.2.0, whose reviewed binary passed TarLink's complete safe-extraction acceptance test. Godot and BizHawk are omitted until authoritative upstream binary SHA-256 evidence is available.

## Install

Install the latest command with Go:

```sh
go install github.com/drobilica/tarlink/cmd/tarlink@latest
```

The command is expected to be available in `$(go env GOPATH)/bin` (or the configured Go binary directory). TarLink uses the current user's XDG data, state, and cache directories; it never writes to system installation paths.

## Security model in one page

- Only the official registry is accepted. Registry generations and the current pointer are validated before use.
- Release URLs must be HTTPS and match the per-application approved source prefix.
- Downloads are bounded at 8 GiB, have explicit timeouts and a five-redirect limit, and are checked against the manifest's lowercase SHA-256.
- Only `tar.gz`, `tar.xz`, and ZIP archives are accepted. Archive names are canonicalized and bounded; absolute paths, traversal, invalid UTF-8, hardlinks, devices, FIFOs, sockets, and unknown entries are rejected. Symlinks are accepted only as same-directory library-style chains that terminate at an extracted regular file; they can never be extraction parents.
- Extraction is bounded to 100,000 entries, 24 GiB total, 8 GiB per file, 8 GiB compressed input, 4,096 path bytes, depth 64, and a 1 GiB XZ dictionary.
- Installation is staged and activated atomically through a relative symlink. A failed install cannot replace the active version.
- TarLink has no hooks, arbitrary commands, custom destinations, plugin system, telemetry, daemon, self-update mechanism, or runtime sandbox dependency.

See [the architecture](docs/architecture.md), [security model](docs/security-model.md), [security policy](SECURITY.md), and [the threat model](docs/threat-model.md) for the complete design.

## Common commands

The command-line surface is intentionally small:

```text
tarlink registry sync
tarlink search <query>
tarlink info <app-id>
tarlink install <app-id>
tarlink update <app-id>
tarlink update --all
tarlink list
tarlink versions <app-id>
tarlink rollback <app-id>
tarlink remove <app-id>
tarlink tui
tarlink version
```

`update --all` evaluates applications in stable ID order, continues after an individual failure, and reports every result. Its process exit is non-zero if any application failed; it does not roll back successful applications merely because another application failed.

Structured JSON is available for `search`, `list`, `info`, and `versions` with `--json`. Standard output then contains JSON only; diagnostics remain on standard error and failures remain machine-detectable by exit status.

## Exit status

The process exits with a non-zero status for errors. Exit statuses are:

| Status | Meaning |
| ---: | --- |
| 0 | Success |
| 2 | Invalid arguments or usage |
| 3 | Unsupported platform |
| 4 | Registry validation or availability failure |
| 5 | Network/download failure |
| 6 | Checksum mismatch |
| 7 | Archive validation or extraction failure |
| 8 | Application not found |
| 9 | Already installed |
| 10 | Application is not installed |
| 11 | No update available |
| 12 | Lock conflict |
| 13 | State corruption |
| 14 | Filesystem permission error |
| 15 | Existing-file or ownership conflict |
| 16 | Root execution refused |
| 1 | Other unexpected failure |

## Development

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

Contributions must preserve the boundaries in [AGENTS.md](AGENTS.md). See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## License

TarLink is open source under the Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
