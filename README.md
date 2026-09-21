# TarLink

### A rootless Linux application manager for verified portable software.

TarLink installs user-owned applications from an official
registry. Applications are versioned, verified, and easy to roll back—without
`sudo`, system-wide installation, or taking over your package manager.

## Quick start

Install TarLink:

```sh
RELEASE=v0.18.0
curl -fL --proto '=https' --tlsv1.2 -o tarlink-install.sh \
  "https://raw.githubusercontent.com/drobilica/tarlink/$RELEASE/install.sh"
sed -n '1,$p' tarlink-install.sh
sh tarlink-install.sh "$RELEASE"
```

Uninstall TarLink:

```sh
RELEASE=v0.18.0
curl -fL --proto '=https' --tlsv1.2 -o tarlink-uninstall.sh \
  "https://raw.githubusercontent.com/drobilica/tarlink/$RELEASE/uninstall.sh"
sed -n '1,$p' tarlink-uninstall.sh
sh tarlink-uninstall.sh
```

The release-specific installer uses the same `RELEASE` for the official
binary and `checksums.txt`; the uninstaller is downloaded from that release as
well. These commands make the shell scripts inspectable before execution.
Release tags and assets are publisher-controlled, so their checksums do not
independently authenticate the publisher. Self-update is explicit and binds
the latest resolved stable version to its checksum and binary before atomic
replacement. Uninstallation removes every managed application and the
TarLink binary, leaving files it cannot prove TarLink owns and reporting them
as warnings.

Install an application from the [official registry](https://github.com/drobilica/tarlink-registry):

```sh
tarlink install blender
```

Run `tarlink` without a command to open the interactive TUI.

## Version status

The latest stable release is `v0.18.0`. It predates static artifact
repositories, which were added to current `main` afterward. This README follows
current `main`; the `tarlink repository` commands and
`$XDG_CONFIG_HOME/tarlink/repositories.json` sources described in the technical
documentation are therefore not available in the `v0.18.0` binary. That
release acquires application, runtime, and remote-icon bytes from the HTTPS
sources declared by the official registry.

## Why TarLink?

- **Rootless and user-owned.** Everything TarLink manages stays in your user
  environment; no `sudo`, daemon, or system package is required.
- **Digest-pinned artifacts.** Registry manifests identify exact official
  upstream releases and require a registry-approved SHA-256 or SHA-512 digest.
  The digest verifies the downloaded bytes; it is not publisher authentication.
- **Safe, versioned lifecycle.** Downloads are staged, installs are activated
  atomically, and the current version plus one previous version are retained
  for rollback.
- **Integrity checks.** `tarlink doctor` audits managed state, payloads,
  executable links, and declared desktop integration without running apps.
- **PATH conflict protection.** TarLink detects executable conflicts and never
  edits or executes your `PATH`.
- **CLI and TUI.** Use the command line or the built-in interactive interface
  over the same application service.
- **Declarative integration.** Manifests may declare desktop entries and icons;
  TarLink does not run shell hooks or arbitrary installation scripts.
- **AppImage support.** Verified, supported AppImages are installed as opaque
  application files and are never mounted or executed by TarLink.

## How it works

```text
Official registry: exact release and digest
        ↓
Configured static repository or registry-declared HTTPS source
        ↓ exact-byte digest verification
Safe staging
        ↓
Versioned user-owned install
        ↓
Known executable and optional desktop integration
```

## Common commands

```sh
tarlink search <query>
tarlink installed --json
tarlink install <app>...
tarlink lock
tarlink install -f tarlink.lock
tarlink update <app>
tarlink rollback <app>
tarlink uninstall <app>...
tarlink doctor
tarlink self-update
```

Use `tarlink` for the TUI. The registry is refreshed automatically when needed
and can be refreshed explicitly with `tarlink refresh`. Explicit refresh always
checks the current official registry and reports the successful UTC check time.
`tarlink list` shows the available catalog with installed state; use
`--installed` or `--updates` to filter it.

`tarlink installed --json` writes the stable, versioned installed-application
machine contract used for Unix-style composition, including
`tarlink installed --json | tarlink-data sync`. Generate local shell
completion with `tarlink completion bash`, `zsh`, or `fish`.

`tarlink lock` writes a deterministic `tarlink.lock` snapshot of the currently
installed TarLink applications. It records the exact Linux architecture,
channel, version, and resolved-package fingerprint. Replaying it with
`tarlink install -f tarlink.lock` resolves every entry again through the
official catalog, rejects changed resolutions, converges only the listed
applications, and never removes extra installed applications. Use
`--output <path>` to write a different snapshot path; `--force-path` applies
to every selected application when installing either explicit applications or
a lock snapshot.

A lock snapshot contains no artifact URLs, checksums, or payloads. Replay is a
catalog re-resolution workflow, not an offline backup or recovery guarantee: it
cannot proceed when the validated registry metadata needed to resolve an entry
is unavailable. It may use already available local bytes, but it does not
replace the registry or retained application state.

In the TUI, use `↑`/`↓` or `j`/`k` to navigate and `Enter` to open details or
review the current selection. `Space` selects applications by stable ID; move
the cursor afterward without changing the selection. Confirming a batch shows
the complete resolved set before mutation. Batch installs freeze each
application's default channel and current version before mutation.

## Applications

The live catalog is maintained in the [official TarLink registry](https://github.com/drobilica/tarlink-registry),
not in this README. It covers development tools, portable utilities, and game,
emulator, and recompilation projects. Registry availability is platform-specific,
so it depends on the Linux architecture published by each upstream.

## Security

TarLink treats registry data, release downloads, archives, and local managed
files as untrusted input. It accepts only the official registry and exact
digest-verified artifact bytes, enforces HTTPS and resource limits, rejects
unsafe archive structures, and validates ownership before changing or removing
files. A digest proves that the bytes match trusted registry metadata; it does
not authenticate the upstream publisher or the mutable registry. Exact
platform and runtime-closure checks address compatibility, not isolation.
TarLink does not sandbox applications after activation; they run with the
user's permissions.

Read the [security model](docs/security-model.md), [threat model](docs/threat-model.md),
and [security policy](SECURITY.md) for the complete boundaries and reporting
process.

## Documentation

| Topic | Guide |
| --- | --- |
| Architecture and lifecycle | [docs/architecture.md](docs/architecture.md) |
| Enforced security guarantees and limits | [docs/security-model.md](docs/security-model.md) |
| Trust boundaries and adversaries | [docs/threat-model.md](docs/threat-model.md) |
| Manifest contract (schema v5) | [docs/manifest-v5.md](docs/manifest-v5.md) |
| Owned filesystem paths | [docs/filesystem-layout.md](docs/filesystem-layout.md) |
| Registry research workflow | [docs/registry-research.md](docs/registry-research.md) |
| Contributor workflow | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Vulnerability reporting | [SECURITY.md](SECURITY.md) |
| Live application catalog | [tarlink-registry](https://github.com/drobilica/tarlink-registry) |
| Releases | [GitHub Releases](https://github.com/drobilica/tarlink/releases) |
| Issues | [GitHub Issues](https://github.com/drobilica/tarlink/issues) |

## Project status

TarLink is pre-1.0. Manifest and command interfaces may make clean breaking
changes before the project reaches 1.0.

## Future direction (planned, not implemented)

An offline installation or recovery mode remains planned. It would require a
validated local registry snapshot and would have to disable registry refresh and
network repository sources; the current lock workflow is not that mode. Shared
low-level cache primitives may be considered after real implementations show
what should be shared, with possible reuse by other open-source tools. No cache
transport—including OCI/ORAS—is selected or promised.
The projects will continue to favor Unix-style composition and small, stable
machine interfaces. Minor releases receive a source dead-code audit; routine
patch releases do not require one.

## License

TarLink is licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE) for
attribution information.
