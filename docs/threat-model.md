# Threat model

## Trusted

- TarLink code.
- The reviewed official TarLink registry.
- The local user account under which TarLink runs.

## Potentially hostile

- Downloaded archives, filenames, and archive metadata.
- Malformed registry data and unexpected filesystem state.
- Network failures and responses.
- A concurrent TarLink process.

## Assets

- The user's existing files and active application.
- The integrity of downloaded application bytes.
- Registry policy and manifest identity.
- The confidentiality of local state and cache metadata.

## Adversaries

1. A malicious or compromised registry mirror, release host, or redirect target.
2. A malicious release archive attempting path traversal, link traversal, device creation, or resource exhaustion.
3. A local unprivileged process racing files in the staging or cache directories.
4. A malformed registry, manifest, network response, or compressed stream.

## Mitigations

| Threat | Control |
| --- | --- |
| Wrong release bytes | HTTPS approved-source policy and SHA-256 verification |
| Registry substitution | Official source only, validated immutable generation, relative `current` |
| Zip-slip / tar traversal | UTF-8, canonical relative path validation and depth/length limits |
| Symlink or hardlink escape | Hardlinks rejected; symlinks confined to same-directory regular-file chains; parent `lstat`; exclusive creation |
| Device/FIFO/socket abuse | All special and unknown entry types rejected |
| Decompression bomb | Entry, byte, file, archive-input, and XZ dictionary bounds |
| Partial activation | Staging, same-filesystem rename, atomic relative symlink and state writes |
| Concurrent update corruption | Per-application locks and deterministic `update --all` behavior |
| Resource starvation | Explicit network timeouts, redirect cap, response caps, and context cancellation |

## Outside TarLink's security boundary

TarLink does not claim to sandbox the installed application. Once activated, the application runs with the user's normal permissions and can access whatever that user can access. Supply-chain authenticity beyond HTTPS, registry policy, and SHA-256 is outside the current design; signed metadata is a future, separately reviewed feature.
