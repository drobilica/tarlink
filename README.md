# TarLink

### A rootless Linux application manager for verified portable software.

TarLink installs user-owned applications from an official, platform-specific
registry. Applications are versioned, verified, and easy to roll back—without
`sudo`, system-wide installation, or taking over your package manager.

## Quick start

Install TarLink:

```sh
curl -fsSL https://raw.githubusercontent.com/drobilica/tarlink/main/install.sh | sh
```

Install an application from the [official registry](https://github.com/drobilica/tarlink-registry):

```sh
tarlink install blender
```

Run `tarlink` without a command to open the interactive TUI.

## Why TarLink?

- **Rootless and user-owned.** Everything TarLink manages stays in your user
  environment; no `sudo`, daemon, or system package is required.
- **Verified upstream artifacts.** Registry manifests identify exact official
  upstream releases and require a registry-approved SHA-256 or SHA-512 digest.
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
Official registry
        ↓
Exact upstream release
        ↓
Digest verification
        ↓
Safe staging
        ↓
Versioned user-owned install
        ↓
Known executable and optional desktop integration
```

## Common commands

```sh
tarlink search <query>
tarlink install <app>
tarlink update <app>
tarlink rollback <app>
tarlink uninstall <app>
tarlink doctor
tarlink self-update
```

Use `tarlink` for the TUI. The registry is refreshed automatically when needed
and can be refreshed explicitly with `tarlink refresh`. Explicit refresh always
checks the current official registry and reports the successful UTC check time.
`tarlink list` shows the available catalog with installed state; use
`--installed` or `--updates` to filter it.

In the TUI, use `↑`/`↓` or `j`/`k` to navigate and `Enter` to open details or
review the current selection. `Space` selects applications by stable ID; move
the cursor afterward without changing the selection. Confirming a batch shows
the complete resolved set before mutation. Batch installs freeze each
application's default channel and current version before mutation.

## Applications

The live catalog is maintained in the [official TarLink registry](https://github.com/drobilica/tarlink-registry),
not in this README. It covers development tools, portable utilities, and game,
emulator, and recompilation projects. Registry entries are platform-specific,
so availability depends on the Linux architecture published by each upstream.

## Security

TarLink treats registry data, release downloads, archives, and local managed
files as untrusted input. It accepts only the official registry and verified
official release artifacts, enforces HTTPS and resource limits, rejects unsafe
archive structures, and validates ownership before changing or removing files.
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

## License

TarLink is licensed under [Apache-2.0](LICENSE). See [NOTICE](NOTICE) for
attribution information.
