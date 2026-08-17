# Security model

TarLink relies on a narrow manifest language, verified bytes, constrained extraction, explicit ownership, and atomic filesystem changes.

## Network and registry

- The official registry URL is compiled into the program. Alternate registries and arbitrary manifest locations are not accepted.
- Registry and release traffic uses HTTPS. Release redirects must remain valid HTTPS URLs and are capped at five; a digest authenticates the final bytes independently of redirect hosting.
- Connect, TLS handshake, response-header, and overall timeouts bound waits.
- Registry responses are limited to 64 MiB. Application downloads are limited to 8 GiB.
- Each release declares SHA-256 or SHA-512, an exact lowercase digest, and an authoritative upstream HTTPS checksum source. Verification completes before extraction or activation. MD5, SHA-1, unknown algorithms, missing verification, and malformed digests are rejected.
- Registry generations contain only a revalidated `apps/` tree. The active pointer must be relative and remain below `generations/`.
- A missing registry is fetched automatically. Failed stale refreshes cannot replace or invalidate the last successfully validated cache.
- XDG data, state, and cache homes must be absolute paths below the user's home and cannot contain control characters. Managed directory chains are checked without accepting symlink components before mutation.

The checksum source is provenance reviewed with the manifest; TarLink does not fetch or cryptographically bind that source at runtime, and manifests cannot turn it into a command. The official registry branch is a trusted, mutable catalog rather than signed metadata: compromise of that registry can replace both an artifact URL and its digest and is outside the current authenticity guarantee.

## Archive policy

Accepted formats are exactly `tar.gz`, `tar.xz`, and ZIP, and the declared format must agree with content magic.

| Resource | Limit |
| --- | ---: |
| Compressed archive bytes | 8 GiB for applications; 64 MiB for registry sync |
| Entries | 100,000 |
| Extracted bytes | 24 GiB |
| One regular file | 8 GiB |
| Entry path | 4,096 bytes |
| Path depth | 64 components |
| XZ dictionary | 1 GiB |

Names must be valid UTF-8, relative, slash-separated, canonical, NUL-free, and free of empty, `.` and `..` components. Unix absolute, drive-qualified, UNC, and backslash-containing names are rejected. Duplicate names and file/directory collisions are errors.

Regular files and directories are materialized directly. Bounded POSIX PAX global metadata records create no filesystem object. A symbolic link is accepted only when its target is one canonical component in the same directory; every complete link chain must end at an extracted regular file. Links cannot be extraction parents. Absolute, traversing, cross-directory, dangling, cyclic, and directory links are rejected. Hardlinks, devices, FIFOs, sockets, special permission bits, and unknown entry types are rejected.

Files are created exclusively and every parent is checked with `lstat`. Archive directories become `0755`; files become `0644` or `0755` when the portable archive marks them executable. Ownership metadata is never preserved, and the declared primary executable is validated independently.

## Local ownership and lifecycle

Effective UID 0 is rejected. State uses a temporary file, flush, atomic rename, and directory `fsync`. Active-version links are relative and replaced atomically. Only the current and one previous validated version are retained.

State is accepted for removal only when its application ID, versions, executable, executable integration, optional desktop integration, and integration digest match the canonical current-user layout. Existing integrations are validated before deletion. Missing integrations make interrupted cleanup retryable; replacements or modifications are conflicts. Application roots and TarLink product roots are removed through containment and symlink checks that never select their broader parent directories.

Corrupt state, unexpected symlinks, untracked entries, integration conflicts, and partial cleanup errors stop full purge. The shell does not remove the TarLink binary after such a failure.

## Explicit exclusions

TarLink has no telemetry, plugins, arbitrary command arguments, hooks, custom destinations, self-update, daemon, background updater, system-wide installation, or external command execution. It uses no CGO or operating-system package manager.

TarLink proves that downloaded bytes match the reviewed registry digest. It does not independently prove that the registry or upstream publisher is uncompromised and does not sandbox the installed application at runtime.
