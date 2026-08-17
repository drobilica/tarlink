# TarLink architecture

TarLink is a rootless, single-user application manager for Linux amd64. The design keeps trust decisions explicit and keeps installation data separate from mutable state.

## Trust boundaries

```text
official registry source
        │ HTTPS, bounded response, validated generation
        ▼
manifest + approved-source policy
        │ HTTPS URL prefix + SHA-256 + archive limits
        ▼
download cache ── hash verification ── staging directory
                                           │ safe archive extraction
                                           ▼
                              versioned app directory
                                           │ atomic relative symlink
                                           ▼
                                      active version
```

The registry is the only catalog authority. A registry sync downloads the official repository archive, validates its policy, manifests, deterministic index, and tree links, then publishes a complete generation. The `current` pointer changes only after the generation is complete.

## Installation flow

1. Resolve the application manifest from the validated registry.
2. Acquire the per-application lock and inspect current state.
3. Download only from the manifest URL and its approved HTTPS prefix, enforcing timeouts, redirect count, response bounds, and SHA-256.
4. Extract into an existing empty staging directory. The extractor accepts only tar.gz, tar.xz, or ZIP and enforces all archive and path limits. Hardlinks and special files are rejected; symlinks are limited to validated same-directory regular-file chains and are never followed during extraction.
5. Validate the declared executable path below the extracted root.
6. Rename the completed staging tree into the versioned application directory on the same filesystem.
7. Atomically replace the application `current` symlink with a relative link to the new version.
8. Write state through a temporary file, `fsync`, and rename. A previous version is retained for rollback.

No step invokes an archive-provided program, shell, hook, or installer. No step needs root, a system service, or a runtime sandbox.

## Failure and concurrency policy

Operations hold a lock per application. Failed downloads and extractions leave the active version unchanged. `update --all` sorts IDs, attempts each independently, continues after errors, and returns a summary with deterministic ordering; successful applications remain updated when another application fails.

## Resource policy

Network and archive limits are part of the security contract, not tuning suggestions: registry responses are capped at 64 MiB, release downloads at 8 GiB, and extracted archives at 24 GiB total. See [security-model.md](security-model.md) for the complete table.
