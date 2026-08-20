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

Release assets are exactly the raw `tarlink-linux-amd64` and `tarlink-linux-arm64` binaries plus `checksums.txt`. The installer detects Linux and the machine architecture, selects only the matching release binary, and does not fall back to another architecture. Application manifests are also platform-specific: registry entries live at `apps/<id>/linux-amd64.yaml` and/or `apps/<id>/linux-arm64.yaml`, with no compatibility filename. At runtime, TarLink resolves the manifest matching its exact `GOOS`/`GOARCH` pair. Blender is currently amd64-only because its upstream release does not provide Linux arm64; Godot has both Linux variants.

## Commands

```sh
tarlink search blender
tarlink install blender
tarlink update blender
tarlink rollback blender
tarlink uninstall blender
```

Additional commands include `list`, `info`, `versions`, `update --all`, `upgrade`, and `version`. `update` manages applications; `upgrade` explicitly upgrades TarLink itself. Running `tarlink` launches the TUI. In the TUI, `x`/`delete` opens an uninstall confirmation and `U` opens the TarLink upgrade flow; Enter confirms and Esc cancels. Structured JSON is available for `search`, `list`, `info`, and `versions` with `--json`.

Before installing, TarLink checks `PATH` for a command that would shadow or hide the executable it is about to manage. If `~/.local/bin` is missing from `PATH`, or an earlier `PATH` entry already provides a command with the same name, `tarlink install` refuses unless you acknowledge the conflict with `tarlink install <app> --force-path`. The TUI shows the same conflicts on a confirmation screen and requires Enter to proceed; it never changes or executes anything on your `PATH`.

TarLink downloads the registry automatically when it is absent. A validated cache younger than 24 hours is used without networking. When the cache is stale, TarLink attempts a transactional refresh and keeps the last successfully validated generation if the network is unavailable. Force a refresh with:

```sh
tarlink registry sync
```

Registry maintainers validate a checkout with:

```sh
tarlink registry validate .
```

## Releases

Tagged releases build both supported static Linux binaries, verify the exact two-entry lowercase SHA-256 `checksums.txt`, and publish a release only after the assets pass the release-artifact checks. A release contains no packaged installer, archive, or alternate binary names. `install.sh` downloads only the selected binary and checksum file, verifies the bytes, records a private digest marker under `$XDG_STATE_HOME/tarlink`, and installs the canonical user-owned binary atomically. Reinstallation requires that marker to match the existing binary.

## Remove TarLink

The following command is a **full purge**. It removes every application and version managed by TarLink, TarLink-owned executable links and desktop entries, registry and artifact caches, TarLink state and locks, and the TarLink binary itself:

```sh
curl -fsSL https://raw.githubusercontent.com/drobilica/tarlink/main/uninstall.sh | sh
```

The Go binary performs and validates managed cleanup first. The shell validates the private install marker and binary again after cleanup, then removes both only after cleanup succeeds, and only attempts best-effort removal of exact empty TarLink product directories. Unrelated user files are not removed; ownership conflicts or corrupt state stop the purge with an error. Shared XDG parents such as `~/.local/bin`, `$XDG_DATA_HOME/applications`, and `$XDG_DATA_HOME/icons/hicolor` remain in place. Manifest-provided icons are copied into fixed hicolor paths and are removed only when their ownership digest still matches.

If the canonical `~/.local/bin/tarlink` binary is already missing and no TarLink product roots or install marker remain, removal is an idempotent no-op. If product roots remain, rerun the installer and then rerun removal so Go can validate ownership and finish cleanup. The removal script deliberately refuses binaries found elsewhere on `PATH`, refuses a symlink at the canonical path, and refuses any binary without an exact install marker.

## Security

- The official registry URL is compiled into TarLink; alternate registries are not accepted.
- Manifests are strict data and cannot run commands, hooks, scripts, or arbitrary arguments.
- Application release artifacts and redirects must use HTTPS. Downloads are bounded, timed out, and verified before extraction with an exact lowercase SHA-256 or SHA-512 digest recorded by the registry. Other algorithms, malformed digests, and missing verification are rejected. TarLink's own release assets and install marker remain SHA-256-only.
- Only `tar.gz`, `tar.xz`, and ZIP are accepted. Extraction rejects traversal, hardlinks, devices, special files, and unsafe symlinks, while retaining all documented size and depth limits.
- Installation uses staging, versioned directories, atomic activation, strict state, per-application locks, one previous rollback version, and explicit ownership validation.
- TarLink never invokes external programs and has no CGO, plugins, telemetry, daemon, automatic updater, custom install destinations, or system dependencies. Explicit `tarlink upgrade` and TUI `U` use only the canonical TarLink-owned binary and verified official GitHub release assets.

TarLink verifies that downloaded bytes match the reviewed registry digest. The official mutable registry is the trust anchor; its checksum-source field records reviewer provenance but is not fetched at runtime. TarLink does not sandbox or guarantee the safety of an upstream application after activation. See [the architecture](docs/architecture.md), [security model](docs/security-model.md), [security policy](SECURITY.md), and [threat model](docs/threat-model.md).

## Development

End users should use `install.sh`. Contributors with Go installed may use `go install github.com/drobilica/tarlink/cmd/tarlink@latest` for development. See [CONTRIBUTING.md](CONTRIBUTING.md) for the first pull/build, reusable checkout workflow, macOS Podman path, quick iteration checks, and the required full validation before opening a change.

```sh
./scripts/validate.sh
```

TarLink is licensed under Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
