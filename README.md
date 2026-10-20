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
tarlink update --all --dry-run
tarlink update --all --dry-run --json
tarlink rollback <app>
tarlink uninstall <app>...
tarlink doctor
tarlink repair <app>
```

Registry-dependent commands refresh the official catalog when needed;
`tarlink refresh` always checks it explicitly. `tarlink doctor` audits managed
state, payloads, executable links, and desktop integration without running apps.
Use `tarlink repair <app>` to restore the exact installed package and missing
integration entries without changing its pin or rollback history. Repair needs
valid installed state and the same package fingerprint in the approved catalog;
it refuses conflicting files. It restores the current application payload,
not a lost previous payload or a damaged existing shared runtime. PATH conflicts
are reported before installation.

`update --dry-run` previews current and target versions, channels, package
fingerprints, runtime changes, pin skips, resolution failures, and the package
retained for rollback. It may refresh catalog metadata but does not acquire
application artifacts or change installed applications. `--json` is available
with `--dry-run` and emits one schema-version-1 plan; resolution errors return
nonzero after printing the plan. A later CLI update creates a new plan. The TUI
shows a resolved update plan before confirmation and applies that same plan.
Updates recheck installed state and approved target fingerprints under the
lifecycle lock before acquisition, rejecting stale plans before any update.

In the TUI, navigate with `↑`/`↓` or `j`/`k`, open details with `Enter`, and select
applications with `Space`. Batch installs show the resolved set for confirmation.
Use `tarlink --help` or the [CLI reference](cli/README.md) for all commands,
including channels, pinning, registry maintenance, and static artifact mirrors.
Generate shell completions with `tarlink completion <shell>`.

### Offline installation and repair

```sh
tarlink --offline install <app>
tarlink --offline install -f tarlink.lock
tarlink --offline repair <app>
```

Prepare the machine's official catalog with `tarlink refresh` while online.
Application, exact runtime, and remote icon artifacts must already be in the
verified cache or a configured filesystem repository. To prepare a local
repository, use `repository init`, `repository sync`, and `repository add` as
described in the [repository guide](docs/filesystem-layout.md#static-artifact-repositories).

Offline mode accepts the validated local catalog regardless of age, without
refreshing it or advancing its check time. It skips HTTPS repositories and
blocks upstream downloads and advisory version checks. All selected artifacts
are verified before applications change; missing or corrupt resources are
reported by application, resource kind, algorithm, and digest. Preflight may
populate or repair the disposable cache from local repositories. The normal
ownership, extraction, atomic activation, and rollback checks still apply.
The flag supports only `install` and `repair`, including batch/lock installs;
other operations are rejected. A local mirror alone cannot replace missing
approved catalog metadata.

### Snapshots and composition

```sh
tarlink lock
tarlink install -f tarlink.lock
tarlink installed --json | tarlink-data sync
```

A lock records exact platforms, channels, versions, and package fingerprints.
Replay resolves entries through the official catalog, rejects changed package
identities, and leaves extra installed applications alone. It requires the
approved registry metadata, including in offline mode; the lock itself is not
an artifact or metadata backup.
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
