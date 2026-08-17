# TarLink

TarLink installs portable Linux applications as rootless, versioned, user-owned software.

```sh
curl -fsSL https://raw.githubusercontent.com/drobilica/tarlink/main/install.sh | sh
```

Then install an application. TarLink fetches and validates the official registry automatically:

```sh
tarlink install blender
```

No root access, `sudo`, Go installation, shell-profile modification, daemon, or system package is required. The installer places one statically linked Go binary at `~/.local/bin/tarlink` and warns when that directory is not in `PATH`.

## Status

TarLink is pre-1.0. Its manifest and command interfaces may change without compatibility layers until the project explicitly approaches 1.0.

Release assets are raw `tarlink-linux-amd64` and `tarlink-linux-arm64` binaries plus `checksums.txt`. Application manifests remain architecture-specific, so an application is installable only when its manifest matches the running Linux architecture. The official registry currently provides reviewed amd64 manifests for Blender and Godot.

## Commands

```sh
tarlink search blender
tarlink install blender
tarlink update blender
tarlink rollback blender
tarlink uninstall blender
```

Additional commands include `list`, `info`, `versions`, `update --all`, `tui`, and `version`. Structured JSON is available for `search`, `list`, `info`, and `versions` with `--json`.

TarLink downloads the registry automatically when it is absent. A validated cache younger than 24 hours is used without networking. When the cache is stale, TarLink attempts a transactional refresh and falls back to the last successfully validated generation if the network is unavailable. Force a refresh with:

```sh
tarlink registry sync
```

Registry maintainers validate a checkout with:

```sh
tarlink registry validate .
```

## Remove TarLink

The following command is a **full purge**. It removes every application and version managed by TarLink, TarLink-owned executable links and desktop entries, registry and artifact caches, TarLink state and locks, and the TarLink binary itself:

```sh
curl -fsSL https://raw.githubusercontent.com/drobilica/tarlink/main/uninstall.sh | sh
```

The Go binary performs and validates managed cleanup first. The shell removes the binary only after cleanup succeeds. Unrelated user files are not removed; ownership conflicts or corrupt state stop the purge with an error.

If the canonical `~/.local/bin/tarlink` binary is already missing, rerun the installer and then rerun the removal command. The removal script deliberately refuses binaries found elsewhere on `PATH` and refuses a symlink at the canonical path.

## Security

- The official registry URL is compiled into TarLink; alternate registries are not accepted.
- Manifests are strict data and cannot run commands, hooks, scripts, or arbitrary arguments.
- Release artifacts and redirects must use HTTPS. Downloads are bounded, timed out, and verified before extraction with the authoritative upstream algorithm recorded by the registry: SHA-256 or SHA-512. MD5, SHA-1, malformed digests, and missing verification are rejected.
- Only `tar.gz`, `tar.xz`, and ZIP are accepted. Extraction rejects traversal, hardlinks, devices, special files, and unsafe symlinks, while retaining all documented size and depth limits.
- Installation uses staging, versioned directories, atomic activation, strict state, per-application locks, one previous rollback version, and explicit ownership validation.
- TarLink never invokes external programs and has no CGO, plugins, telemetry, daemon, self-update, custom install destinations, or system dependencies.

TarLink verifies that downloaded bytes match the reviewed registry digest. The official mutable registry is the trust anchor; its checksum-source field records reviewer provenance but is not fetched at runtime. TarLink does not sandbox or guarantee the safety of an upstream application after activation. See [the architecture](docs/architecture.md), [security model](docs/security-model.md), [security policy](SECURITY.md), and [threat model](docs/threat-model.md).

## Development

End users should use `install.sh`. Contributors with Go installed may use `go install github.com/drobilica/tarlink/cmd/tarlink@latest` for development.

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

TarLink is licensed under Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
