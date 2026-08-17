# TarLink architecture

TarLink is a rootless, single-user application manager for Linux amd64 and arm64. Application manifests describe one architecture each, and the client refuses a manifest that does not match the running platform.

## Trust boundaries

```text
compiled official registry URL
        │ HTTPS, bounded archive, validated apps/ tree
        ▼
strict manifest
        │ exact HTTPS artifact URL + SHA-256/SHA-512 digest
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

The official registry is the only catalog authority. TarLink directly enumerates `apps/<id>/manifest.yaml`; there is no generated index, secondary approved-source policy, or registry-local parser. A sync validates a staged repository archive, moves only its normalized `apps/` data into a private generation, validates that generation again, flushes it, and atomically changes the relative `current` pointer. Registry refresh retains only the current and immediately previous generations.

Normal registry-dependent commands bootstrap a missing cache automatically. Valid caches remain local-only for 24 hours. A stale cache triggers a refresh attempt; a failed attempt may fall back only to the already validated cache. `registry sync` always attempts a refresh, while local operations such as rollback and uninstall do not require networking.

## Installation flow

1. Load or refresh the validated official registry and resolve the application manifest.
2. Require the manifest platform to match the running client.
3. Acquire the lifecycle and per-application locks and inspect strict, layout-bound state.
4. Download the declared HTTPS artifact with timeouts, redirect and size bounds. Redirects must remain HTTPS.
5. Verify the exact archive bytes with the manifest's supported upstream algorithm and digest before extraction.
6. Extract into a private staging directory using the archive path, link, type, count, size, depth, and XZ dictionary limits.
7. Validate the declared executable and rename the completed tree into the versioned application directory on the same filesystem.
8. Create only the known executable link and optional desktop entry, then atomically switch the relative `current` link.
9. Atomically write explicit ownership state and retain at most the current and one previous version.

No step invokes an archive-provided program, shell, hook, installer, or arbitrary argument.

## Removal flow

Single-application uninstall loads strict state, requires canonical paths for the configured layout, validates every existing integration, removes only those integrations, then removes the exact application root and state file. Full purge first enumerates those same state records and uses normal application uninstall. It removes fixed TarLink-owned child roots after application cleanup succeeds and removes product parents only when empty; shared directories such as `~/.local/bin` and `$XDG_DATA_HOME/applications` are never broadly deleted.

The bootstrap `uninstall.sh` contains no application-cleanup implementation. It invokes the canonical `~/.local/bin/tarlink uninstall --all` and removes that binary only after Go reports success.

## Failure and concurrency

Failed registry refreshes cannot replace the validated cache. Failed downloads, verification, extraction, integration, activation, or state writes leave the previously active version usable. Mutations and registry refreshes share a cross-process lifecycle `flock` on the existing home-directory inode, then use narrower per-application or registry locks. The lifecycle lock leaves no file for purge to race or retain. `update --all` processes stable application IDs, continues after independent errors, and returns a non-zero result when any update fails.

Network and archive limits are part of the security contract. See [security-model.md](security-model.md).
