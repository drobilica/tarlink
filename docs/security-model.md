# Security model

TarLink's security posture is based on a narrow input language and atomic filesystem changes.

## Network and registry

- The official registry URL is fixed in the program; alternate registries and arbitrary manifest locations are not accepted.
- Registry and release traffic must use HTTPS. Redirects are followed only up to five times and every destination must remain within the application's approved source scope.
- Explicit connect, TLS handshake, response-header, and overall timeouts prevent an unbounded wait.
- Registry responses are limited to 64 MiB. Release downloads are limited to 8 GiB.
- Every release is verified against exactly 64 lowercase hexadecimal SHA-256 characters from the manifest before extraction.
- Registry generations are validated before publication. The `current` link must be relative and remain below `generations/`.

## Archive policy

Accepted formats are exactly `tar.gz`, `tar.xz`, and ZIP, and the declared format must agree with content magic. Limits are:

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

Regular files and directories are materialized directly. A symbolic link is accepted only when its target is a canonical, single path component in the same directory. Links can never be used as extraction parents, and after the archive is complete every link chain is checked without following it and must terminate at an extracted regular file. Absolute, traversing, cross-directory, dangling, cyclic, and directory links are rejected. Hardlinks, character/block devices, FIFOs, sockets, setuid/setgid/sticky modes, and unknown archive entry types are rejected.

Files are created exclusively and every parent is checked with `lstat` before creation. Archive-created directories are `0755`. Files are `0644`, or `0755` when the upstream portable archive identifies an executable; special permission bits and ownership metadata are never preserved. The declared primary executable is independently validated and set to `0755`.

## Local data and lifecycle

All managed data is user-owned, and effective UID 0 is rejected. State uses a temporary file, flush, atomic rename, and directory `fsync`. An active-version link is relative and replaced atomically. The manager retains the current and one previous version; rollback switches only between already validated local trees.

TarLink proves ownership before removing integrations and refuses to overwrite or delete an unrelated file. Executable-link targets and a SHA-256 of the generated desktop entry are recorded in state; a replaced or modified integration is a conflict, even if it retains a TarLink marker. Tests use temporary XDG roots and never touch the developer's real home directories.

## Explicit exclusions

TarLink has no telemetry, plugins, arbitrary command arguments, archive hooks, custom destinations, self-update, daemon, background updater, or system-wide installation mode. It never invokes an external process and does not install operating-system dependencies.

TarLink verifies that downloaded bytes match the reviewed registry artifact. TarLink does not guarantee that the upstream application itself is secure, and it does not sandbox the application at runtime.
