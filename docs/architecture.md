# TarLink architecture

TarLink is a rootless, single-user application manager for Linux amd64 and arm64. Each application is one strict schema-v5 manifest that stores shared application, release, and integration metadata once. Release entries may explicitly declare one inner archive extraction layer. Every application is stored at `apps/<id>/manifest.yaml`, and each retained release declares its exact supported platforms as artifact keys such as `artifacts.linux-amd64` and `artifacts.linux-arm64`; unsupported platforms are omitted. The client resolves its exact `runtime.GOOS`/`runtime.GOARCH` pair to the matching artifact key in the selected release and refuses an absent platform; there is no alias, compatibility filename, or cross-platform fallback. Executables declare one shared path or explicit per-platform paths, exactly as the schema allows, with no inherited defaults.

## Interface boundaries

`internal/app` is the UI-independent core. It owns registry freshness,
application lifecycle, and TarLink self-upgrade operations. `cli` owns argument
parsing, streams, JSON, and exit codes. `tui` owns Bubble Tea state and views,
using Bubbles for reusable terminal components and Lip Gloss for styling and
layout. Both frontends call the same core service and progress model; neither
performs release discovery, checksum verification, or filesystem replacement
itself.

## Trust boundaries

```text
compiled official registry URL
        │ HTTPS, bounded archive, validated apps/ tree
        ▼
strict v5 manifest and exact platform/channel/version resolution
        │ exact approved HTTPS artifact URL + SHA-256 or SHA-512 digest
        ▼
bounded download ── digest verification ── staging directory
                                                │ safe extraction
                                                ▼
                                   versioned application tree
                                                │ atomic relative symlink
                                                ▼
                                           active version
```

The official registry is the only catalog and artifact-approval authority. TarLink directly enumerates each strict `apps/<id>/manifest.yaml` and resolves only its exact canonical platform entry; there is no generated index, compatibility filename fallback, architecture fallback, secondary approved-source policy, or registry-local parser. A sync validates a staged repository archive, moves only its normalized `apps/` data into a private generation, records the successful checked-at value as private generation metadata, validates and flushes that generation, and atomically changes the relative `current` pointer. Registry refresh retains only the current and immediately previous generations. Historical releases remain registry-approved metadata; they do not imply local retention, and channel heads are never inferred by version sorting.

Normal registry-dependent commands bootstrap a missing cache automatically. Valid caches remain local-only for 24 hours according to their explicit checked-at metadata. A stale cache triggers a refresh attempt; a failed attempt may fall back only to the already validated cache without advancing checked-at. `refresh` always fetches and validates the current official registry, activates it before returning, and prints the successful UTC checked-at value. Local operations such as rollback and uninstall do not require networking. The CLI `list` command enumerates the available platform catalog and annotates installed state; the TUI's installed list remains a separate view.

TarLink release discovery is separate from the application registry. A 24-hour
XDG cache makes version checks advisory and non-blocking for normal operations.
Explicit self-upgrade selects the latest strict stable release, the exact
Linux architecture asset, and `checksums.txt`, then verifies and atomically
replaces the canonical owned binary.

## Installation flow

1. Load or refresh the validated official registry and resolve the application manifest.
2. Resolve the exact `GOOS`/`GOARCH` artifact key in the application manifest and require it to match the running client.
3. Acquire the lifecycle and per-application locks and inspect strict, layout-bound state.
4. Download the declared HTTPS artifact with timeouts, redirect and size bounds. Redirects must remain HTTPS.
5. Verify the exact artifact bytes with the manifest's lowercase SHA-256 or SHA-512 digest before extraction or staging.
6. Extract archives into a private staging directory using the archive path, link, type, count, size, depth, and XZ dictionary limits. AppImages are instead checked as opaque ELF Type 2 files and copied as `appimage` without execution or mounting; a remote icon is downloaded and validated separately and never reads the opaque payload.
7. Validate every declared executable and rename the completed tree into the versioned application directory on the same filesystem.
8. Create only the known executable link and optional desktop entry/icon in the XDG hicolor hierarchy, then atomically switch the relative `current` link. Icon changes are ownership-checked and rolled back with activation failures. Archive-contained icons keep extension-based sizing (`scalable` for SVG, otherwise `48x48`); verified remote PNG icons are downloaded through the bounded artifact client (max 4 MiB), retained inside the version payload, and placed at the hicolor size validated from their PNG signature and IHDR dimensions.
9. Atomically write explicit ownership state and retain at most the current and one previous version.

No step invokes an archive-provided program, shell, hook, installer, or arbitrary argument.

## Removal flow

Single-application uninstall loads strict state, requires canonical paths for the configured layout, validates every existing executable link, desktop entry, and icon, removes only those integrations, then removes the exact application root and state file. Full purge first enumerates those same state records and uses normal application uninstall. It removes fixed TarLink-owned child roots after application cleanup succeeds and removes product parents only when empty; shared directories such as `~/.local/bin`, `$XDG_DATA_HOME/applications`, and the hicolor icon hierarchy are never broadly deleted.

The bootstrap `uninstall.sh` contains no application-cleanup implementation. It validates the install marker against the canonical binary, invokes `~/.local/bin/tarlink uninstall --all`, rechecks the retained digest after Go reports success, and then removes the marker and binary.

## Failure and concurrency

Failed registry refreshes cannot replace the validated cache. Failed downloads, verification, extraction, integration, activation, or state writes leave the previously active version usable. Mutations and registry refreshes share a cross-process lifecycle `flock` on the existing home-directory inode, then use narrower per-application or registry locks. The lifecycle lock leaves no file for purge to race or retain. `update --all` processes stable application IDs, continues after independent errors, and returns a non-zero result when any update fails.

Network and archive limits are part of the security contract. See [security-model.md](security-model.md).
