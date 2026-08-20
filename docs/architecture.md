# TarLink architecture

TarLink is a rootless, single-user application manager for Linux amd64 and arm64. Application manifests describe one exact platform each and are stored as `apps/<id>/linux-amd64.yaml` or `apps/<id>/linux-arm64.yaml`. The client resolves its exact `runtime.GOOS`/`runtime.GOARCH` pair and refuses a missing or mismatched variant; there is no compatibility filename and no cross-platform fallback.

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
strict manifest
        │ exact HTTPS artifact URL + SHA-256 or SHA-512 digest
        │ + authoritative checksum-source provenance
        ▼
bounded download ── digest verification ── staging directory
                                                │ safe extraction
                                                ▼
                                   versioned application tree
                                                │ atomic relative symlink
                                                ▼
                                           active version
```

The official registry is the only catalog authority. TarLink directly enumerates the strict platform files below `apps/<id>/`; there is no generated index, compatibility filename fallback, secondary approved-source policy, or registry-local parser. A sync validates a staged repository archive, moves only its normalized `apps/` data into a private generation, validates that generation again, flushes it, and atomically changes the relative `current` pointer. Registry refresh retains only the current and immediately previous generations.

Normal registry-dependent commands bootstrap a missing cache automatically. Valid caches remain local-only for 24 hours. A stale cache triggers a refresh attempt; a failed attempt may fall back only to the already validated cache. `registry sync` always attempts a refresh, while local operations such as rollback and uninstall do not require networking.

TarLink release discovery is separate from the application registry. A 24-hour
XDG cache makes version checks advisory and non-blocking for normal operations.
Explicit self-upgrade selects the latest strict stable release, the exact
Linux architecture asset, and `checksums.txt`, then verifies and atomically
replaces the canonical owned binary.

## Installation flow

1. Load or refresh the validated official registry and resolve the application manifest.
2. Resolve the exact `GOOS`/`GOARCH` manifest and require it to match the running client.
3. Acquire the lifecycle and per-application locks and inspect strict, layout-bound state.
4. Download the declared HTTPS artifact with timeouts, redirect and size bounds. Redirects must remain HTTPS.
5. Verify the exact artifact bytes with the manifest's lowercase SHA-256 or SHA-512 digest before extraction or staging.
6. Extract archives into a private staging directory using the archive path, link, type, count, size, depth, and XZ dictionary limits. AppImages are instead checked as opaque ELF Type 2 files and copied as `appimage` without execution or mounting.
7. Validate every declared executable and rename the completed tree into the versioned application directory on the same filesystem.
8. Create only the known executable link and optional desktop entry/icon in the XDG hicolor hierarchy, then atomically switch the relative `current` link. Icon changes are ownership-checked and rolled back with activation failures.
9. Atomically write explicit ownership state and retain at most the current and one previous version.

No step invokes an archive-provided program, shell, hook, installer, or arbitrary argument.

## Removal flow

Single-application uninstall loads strict state, requires canonical paths for the configured layout, validates every existing executable link, desktop entry, and icon, removes only those integrations, then removes the exact application root and state file. Full purge first enumerates those same state records and uses normal application uninstall. It removes fixed TarLink-owned child roots after application cleanup succeeds and removes product parents only when empty; shared directories such as `~/.local/bin`, `$XDG_DATA_HOME/applications`, and the hicolor icon hierarchy are never broadly deleted.

The bootstrap `uninstall.sh` contains no application-cleanup implementation. It validates the install marker against the canonical binary, invokes `~/.local/bin/tarlink uninstall --all`, rechecks the retained digest after Go reports success, and then removes the marker and binary.

## Failure and concurrency

Failed registry refreshes cannot replace the validated cache. Failed downloads, verification, extraction, integration, activation, or state writes leave the previously active version usable. Mutations and registry refreshes share a cross-process lifecycle `flock` on the existing home-directory inode, then use narrower per-application or registry locks. The lifecycle lock leaves no file for purge to race or retain. `update --all` processes stable application IDs, continues after independent errors, and returns a non-zero result when any update fails.

Network and archive limits are part of the security contract. See [security-model.md](security-model.md).
