# Threat model

## Trusted

- TarLink code and release workflow.
- The reviewed official TarLink registry.
- The local user account running TarLink.

## Potentially hostile

- Downloaded archives, names, metadata, redirects, and compressed streams.
- Malformed registry manifests and repository archives.
- Corrupt state and unexpected local filesystem objects.
- Network failure and concurrent cooperating TarLink processes.

## Assets

- Unrelated user files and integrations.
- The active and previous managed application versions.
- The integrity of downloaded application bytes.
- The integrity and continuity of the canonical TarLink executable.
- The identity and provenance of registry manifests.
- Local state and cache integrity.

## Adversaries

1. A malicious or compromised release host, checksum host, or redirect target.
2. A malicious archive attempting traversal, unsafe links, special files, or resource exhaustion.
3. A concurrent TarLink process racing integration, state, staging, activation, or purge paths.
4. Accidental state corruption or a user file occupying a TarLink-managed name.

## Mitigations

| Threat | Control |
| --- | --- |
| Wrong release bytes | Strict SHA-256 digest from reviewed upstream checksum provenance |
| Weak or ambiguous verification | Explicit SHA-256 algorithm, fixed digest length, lowercase hex, HTTPS source; other algorithms rejected |
| Alternate registry substitution | Exact compiled HTTPS source, bounded staged archive, direct manifest validation, normalized immutable generation |
| Offline or failed refresh | Previously validated cache remains active; absent/invalid cache cannot fall back |
| Zip-slip / tar traversal | UTF-8 canonical relative paths with depth and length limits |
| Symlink or hardlink escape | Hardlinks rejected; symlinks confined to same-directory regular-file chains; parent `lstat`; exclusive creation |
| Device or special-file abuse | Devices, FIFOs, sockets, special bits, and unknown types rejected |
| Decompression bomb | Entry, byte, file, archive-input, depth, and XZ dictionary bounds |
| Partial activation | Staging, same-filesystem rename, atomic relative link, atomic state |
| Concurrent mutation | Shared lifecycle `flock`, narrower registry/per-application locks, and non-overwriting integration creation |
| Unsafe self-upgrade | Official stable release filtering, exact platform asset, strict checksum, owned canonical path/marker, same-directory staging, atomic replacement, and rollback on publication failure |
| Arbitrary deletion | Canonical layout-bound state, pre-deletion integration validation, contained exact-root removal |
| Broad purge | Only fixed TarLink product roots and recorded narrow integrations are candidates; shared parents survive |

## Outside the boundary

TarLink does not sandbox installed applications. After activation, an application runs with the user's permissions. A malicious process already running as the same user can mutate that user's TarLink directories and is outside the local-attacker boundary; the ownership checks are designed for accidental state corruption, unexpected objects, and cooperating TarLink concurrency. The mutable official registry is trusted and not signed: compromise of that registry alone can replace both an artifact URL and its digest. Self-upgrade similarly trusts the official GitHub release channel but never installs without checksum verification and ownership validation. Runtime fetching of `verification.source` and signed registry metadata would require separate designs.
