# Dependency review

Runtime dependencies are intentionally few and pure Go where practical:

| Module | Version | Purpose | License / review |
| --- | --- | --- | --- |
| `go.yaml.in/yaml/v3` | v3.0.5 | Strict YAML decoding | Dual MIT / Apache-2.0; aliases and unknown fields are disabled by policy |
| `github.com/ulikunitz/xz` | v0.5.16 | Pure-Go XZ decompression | BSD-3-Clause; dictionary capped at 1 GiB |
| `github.com/clipperhouse/displaywidth` | v0.11.0 | Unicode/grapheme-aware TUI width and truncation | MIT; used only for presentation layout |
| `charm.land/bubbletea/v2` | v2.0.8 | Terminal input and rendering only | MIT; no application lifecycle logic is placed in the TUI |
| `charm.land/bubbles/v2` | v2.1.1 | Reusable TUI key/help and deterministic progress components | MIT; TarLink state and progress semantics remain authoritative |
| `charm.land/lipgloss/v2` | v2.0.6 | Semantic terminal styling and layout | MIT; color output follows Bubble Tea and TarLink terminal capability detection |

The pinned Charm runtime graph was reviewed from `go.mod`, module source, and license files. The Charmbracelet, Clipperhouse, colorful, runewidth, cancelreader, uniseg, and terminfo modules are MIT-licensed. `golang.org/x/sys` and `golang.org/x/sync` use BSD-3-Clause terms. All are pure Go for the supported build and do not execute external programs. Exact versions and checksums are recorded in `go.mod` and `go.sum`.

The standard library provides archive parsing, hashing, HTTP, locking primitives, and filesystem operations. TarLink does not use CGO, shell commands, `os/exec`, unsafe code, plugins, or a system runtime dependency. Dependency upgrades require a source, license, security, and behavior review; dependency notices must be updated when the transitive graph changes.
