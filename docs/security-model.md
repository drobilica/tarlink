# Security model

TarLink relies on a narrow manifest language, verified bytes, constrained extraction, explicit ownership, and atomic filesystem changes.

## Network and registry

- The official registry URL is compiled into the program. Alternate registries and arbitrary manifest locations are not accepted.
- Registry and release traffic uses HTTPS. Release redirects must remain valid HTTPS URLs and are capped at five; a digest authenticates the final bytes independently of redirect hosting.
- Connect, TLS handshake, response-header, and overall timeouts bound waits.
- Registry responses are limited to 64 MiB. Application downloads are limited to 8 GiB.
- Each application release declares an exact lowercase SHA-256 or SHA-512 digest approved in the official registry. Maintainers may calculate it from the exact official upstream HTTPS artifact; verification still completes before extraction or activation. Other algorithms, missing verification, and malformed digests are rejected.
- Version-5 manifests may retain multiple approved releases independently for each exact platform, but every historical release has the same exact URL, digest, archive, and informational origin metadata checks. Explicit channel heads and opaque exact versions are resolved only from the validated official registry; freshness candidates cannot alter trusted release metadata. A release may explicitly declare one nested archive layer, which shares cumulative extraction limits with its outer archive.
- AppImage releases are accepted only as verified, little-endian 64-bit ELF Type 2 artifacts matching the target architecture. TarLink stores them as opaque regular files, never executes, mounts, or extracts them, and rejects Type 1 markers and malformed headers.
- Remote desktop icons are HTTPS-only `.png` downloads capped at 4 MiB, verified against the manifest's lowercase SHA-256, and retained inside each version payload so rollback needs no network. The downloaded bytes must carry the PNG signature and an IHDR chunk whose square dimensions are a supported hicolor size; the size is validated from the header, never decoded from image pixels, and the URL path carries no size-token requirement. Non-PNG, malformed, non-square, or unsupported-dimension downloads fail installation. AppImages remain opaque and may only use a verified remote icon, which is downloaded and validated separately from the opaque payload; an archive-contained icon path inside an AppImage is rejected because no extracted tree exists.
- Registry generations contain only a revalidated `apps/` tree. The active pointer must be relative and remain below `generations/`.
- A missing registry is fetched automatically. Explicit refresh always fetches the current official registry. Each activated cache generation stores the successful UTC check time as private metadata; failed stale or explicit refreshes cannot advance that time or replace the last successfully validated cache.
- XDG data, state, and cache homes must be absolute paths below the user's home and cannot contain control characters. Managed directory chains are checked without accepting symlink components before mutation.

The official registry is the artifact-approval boundary. Schema-v5
`verification.source` records an official upstream release or artifact origin
for reviewer context; it is not an assertion
that upstream published a checksum. TarLink does not fetch or cryptographically
bind it at runtime, and manifests cannot turn it into a command. The official
registry branch is a trusted, mutable catalog rather than signed metadata:
compromise of that registry can replace both an artifact URL and its digest and
is outside the current authenticity guarantee.

## Archive policy

Accepted formats are exactly `tar.gz`, `tar.xz`, and ZIP, and the declared format must agree with content magic.

| Resource | Limit |
| --- | ---: |
| Compressed archive bytes | 8 GiB for applications; 64 MiB for registry refresh |
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

State is accepted for removal only when its application ID, versions, executable, executable integration, optional desktop integration, icon destination/source metadata, and ownership digests match the canonical current-user layout. Existing integrations are validated before deletion. Missing integrations make interrupted cleanup retryable; replacements or modifications are conflicts. Application roots and TarLink product roots are removed through containment and symlink checks that never select their broader parent directories. Icon sources are regular files below the verified application root; icon destinations are fixed hicolor `scalable/apps` (SVG), `48x48/apps` (archive rasters), or the inferred `WxW/apps` (verified remote PNGs) paths.

The shell installer records a private, atomic SHA-256 marker for the canonical TarLink binary. Replacement and bootstrap removal require a regular, non-symlink marker whose exact lowercase digest matches the binary; XDG state paths remain absolute, clean, below `HOME`, and free of symlink components. This self-upgrade marker and the official TarLink release contract remain SHA-256-only.

Corrupt, unparseable, or layout-invalid state records, untracked application directories, and unexpected entries inside managed product roots no longer stop full purge. Removal degrades to the exact TarLink-owned product paths plus only the integrations proven by canonical path and content markers — `~/.local/bin` links whose targets resolve inside the app payload, and the canonical desktop entry carrying TarLink's `X-TarLink-AppID` marker while referencing the payload — and never removes icons or anything else it cannot prove; unprovable leftovers stay in place and are reported as warnings. Integration conflicts and partial cleanup errors still stop the final product-root cleanup, and the shell does not remove the TarLink binary after such a failure.

## TarLink self-upgrade

Self-upgrade accepts only strict stable `vMAJOR.MINOR.PATCH` releases from the
official TarLink GitHub release channel. It requires the canonical regular
`~/.local/bin/tarlink`, a matching private install marker, Linux amd64/arm64,
HTTPS downloads, a strict checksum entry, and a verified SHA-256 digest. The
new executable is staged in the destination directory and atomically replaces
the old one; marker publication is transactional and failures restore the old
binary. Development, symlinked, unmarked, mismatched, or otherwise unowned
installations are refused.

## Explicit exclusions

TarLink has no telemetry, plugins, arbitrary command arguments, hooks, custom destinations, automatic updater, daemon, background updater, system-wide installation, or external command execution. It uses no CGO or operating-system package manager. Self-upgrade is explicit only; it never executes or restarts the replacement binary.

TarLink proves that downloaded bytes match the reviewed registry digest. It does not independently prove that the registry or upstream publisher is uncompromised and does not sandbox the installed application at runtime.
