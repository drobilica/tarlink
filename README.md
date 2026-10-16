# TarLink

A rootless Linux application manager for verified portable software.

TarLink installs user-owned applications from the
[official registry](https://github.com/drobilica/tarlink-registry), verifies their
exact bytes, and activates them atomically. It retains the current version and
one previous version for rollback, without `sudo`, a daemon, or system-wide
installation. Linux amd64 and arm64 are supported when upstream artifacts exist.

## Quick start

Install TarLink:

```sh
curl -fL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/drobilica/tarlink/main/install.sh | sh
```

Install an application or open the interactive TUI:

```sh
tarlink install blender
tarlink
```

The installer resolves the latest stable release and verifies its official
binary against `checksums.txt`. Updates to TarLink itself are explicit:
`tarlink self-update`
verifies and atomically replaces the canonical owned binary.

To remove TarLink and every managed application:

```sh
curl -fL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/drobilica/tarlink/main/uninstall.sh | sh
```

Uninstallation leaves files it cannot prove TarLink owns and reports warnings.
See [GitHub Releases](https://github.com/drobilica/tarlink/releases) for release
notes. TarLink is pre-1.0; manifest and command interfaces may change.

## Everyday use

```sh
tarlink search <query>
tarlink list --installed
tarlink install <app>...
tarlink update <app>
tarlink rollback <app>
tarlink uninstall <app>...
tarlink doctor
```

Registry-dependent commands refresh the official catalog when needed;
`tarlink refresh` always checks it explicitly. `tarlink doctor` audits managed
state, payloads, executable links, and desktop integration without running apps.
PATH conflicts are reported before installation.

In the TUI, navigate with `↑`/`↓` or `j`/`k`, open details with `Enter`, and select
applications with `Space`. Batch installs show the resolved set for confirmation.
Use `tarlink --help` or the [CLI reference](cli/README.md) for all commands,
including channels, pinning, registry maintenance, and static artifact mirrors.
Generate shell completions with `tarlink completion <shell>`.

### Snapshots and composition

```sh
tarlink lock
tarlink install -f tarlink.lock
tarlink installed --json | tarlink-data sync
```

A lock records exact platforms, channels, versions, and package fingerprints.
Replay resolves entries through the official catalog, rejects changed package
identities, and leaves extra installed applications alone. It requires the
approved registry metadata; it is not an offline backup.
`tarlink installed --json` provides a versioned machine interface; `tarlink-data`
is a separate application-data resolver.

## Security

The official registry approves artifact URLs and SHA-256 or SHA-512 digests.
Downloads use HTTPS and exact-byte verification before safe, bounded extraction
or opaque AppImage installation. Configured static repositories supply bytes
only; they cannot approve applications. Lifecycle changes use ownership checks,
locks, staging, and atomic publication. Manifests cannot run installation hooks
or arbitrary commands.

Digests prove agreement with trusted metadata, not independent publisher
authentication. The official registry and TarLink release channel remain trusted,
publisher-controlled sources. TarLink does not sandbox applications: launched
applications run with the user's permissions.

## Documentation

| Topic | Guide |
| --- | --- |
| Architecture and lifecycle | [Architecture](docs/architecture.md) |
| Enforced guarantees and limits | [Security model](docs/security-model.md) |
| Trust boundaries and adversaries | [Threat model](docs/threat-model.md) |
| Manifest contract | [Schema v5](docs/manifest-v5.md) |
| Owned paths and static artifact repositories | [Filesystem layout](docs/filesystem-layout.md) |
| Registry research and onboarding | [Registry research](docs/registry-research.md) |
| Complete command tree | [CLI reference](cli/README.md) |
| Development and validation | [Contributing](CONTRIBUTING.md) |
| Vulnerability reporting | [Security policy](SECURITY.md) |

The live catalog is maintained in the
[official registry](https://github.com/drobilica/tarlink-registry).
Report other problems through [GitHub Issues](https://github.com/drobilica/tarlink/issues).

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
