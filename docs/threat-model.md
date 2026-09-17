# Threat model

## Trusted

- TarLink code and release workflow.
- The reviewed official TarLink registry.
- Exact digest-pinned official Valve Steam Linux Runtime deployments admitted
  by the reviewed registry and the compiled Valve v2 adapter.
- The local user account running TarLink.

## Potentially hostile

- Downloaded archives, names, metadata, redirects, and compressed streams.
- Downloaded AppImage bytes and their embedded filesystem metadata.
- Malformed registry manifests and repository archives.
- Malicious or accidentally extra entries in a static artifact repository and
  response bodies that fail or truncate during source acquisition.
- Corrupt state and unexpected local filesystem objects.
- Network failure and concurrent cooperating TarLink processes.

## Assets

- Unrelated user files and integrations.
- The active and previous managed application versions.
- The integrity of downloaded application bytes.
- The integrity and continuity of the canonical TarLink executable.
- The identity and approved integrity metadata of registry manifests.
- Approved historical release and channel-selection metadata.
- Local state and cache integrity.

## Adversaries

1. A malicious or compromised release host or redirect target.
2. A malicious archive attempting traversal, unsafe links, special files, or resource exhaustion.
3. A concurrent TarLink process racing integration, state, staging, activation, or purge paths.
4. Accidental state corruption or a user file occupying a TarLink-managed name.

## Mitigations

| Threat | Control |
| --- | --- |
| Wrong application release bytes | Exact SHA-256 or SHA-512 digest approved in the trusted official registry and verified before materialization |
| Unapproved historical/channel target | Exact platform, version, and channel resolution are limited to schema-v5 artifact definitions and explicit channel heads in the validated official registry; local retention remains current-plus-one-previous |
| Weak or ambiguous verification | Explicit SHA-256 or SHA-512 algorithm, fixed digest length, lowercase hex, official HTTPS artifact and informational origin; other algorithms rejected |
| Alternate registry substitution | Exact compiled HTTPS source, bounded staged archive, direct manifest validation, normalized immutable generation |
| Static repository tree confusion | Exact descriptor, `v1` algorithm directories, digest-named regular objects, and narrowly allowlisted sync transients; public extras and links are rejected |
| Repository source failure | Body-read, truncation, size, and digest failures fall through ordered sources; cancellation and destination writes remain terminal |
| Offline or failed refresh | Previously validated cache remains active; absent/invalid cache cannot fall back |
| Zip-slip / tar traversal | UTF-8 canonical relative paths with depth and length limits |
| Symlink or hardlink escape | Hardlinks rejected; symlinks confined to same-directory regular-file chains; parent `lstat`; exclusive creation |
| Device or special-file abuse | Devices, FIFOs, sockets, special bits, and unknown types rejected |
| Decompression bomb | Entry, byte, file, archive-input, depth, and XZ dictionary bounds |
| Runtime substitution or mutable runtime selection | Exact runtime artifact SHA-256, immutable deployment path, runtime-inclusive package fingerprint, and current-plus-previous closure retention |
| Unsupported Valve launcher behavior | One compiled `_v2-entry-point --verb=waitforexitandrun --` interface per admitted runtime version; no manifest-controlled executor or fallback |
| AppImage installation code execution | AppImages are only checksum-verified and structurally checked as opaque Type 2 ELF files; TarLink never executes, mounts, or extracts them |
| Partial activation | Staging, same-filesystem rename, atomic relative link, atomic state |
| Concurrent mutation | Shared lifecycle `flock`, narrower registry/per-application locks, and non-overwriting integration creation |
| Unsafe self-upgrade | Official stable release filtering, exact platform asset, strict checksum, owned canonical path/marker, same-directory staging, atomic replacement, and rollback on publication failure |
| Arbitrary deletion | Canonical layout-bound state, pre-deletion integration validation, contained exact-root removal |
| Broad purge | Only fixed TarLink data, state, cache, and config product roots plus recorded narrow integrations are candidates; shared parents survive |

## Outside the boundary

TarLink does not sandbox installed applications. After activation, an application runs with the user's permissions. A malicious process already running as the same user can mutate that user's TarLink directories and is outside the local-attacker boundary; the ownership checks are designed for accidental state corruption, unexpected objects, and cooperating TarLink concurrency. The mutable official registry is trusted and not signed: compromise of that registry alone can replace both an artifact URL and its digest. Self-upgrade similarly trusts the official GitHub release channel but never installs without checksum verification and ownership validation. Informational `verification.source` metadata is not an independent authenticity guarantee; signed registry metadata would require a separate design.
