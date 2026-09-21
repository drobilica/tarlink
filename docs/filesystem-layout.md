# Filesystem layout

TarLink creates only user-owned application roots and narrowly named integrations.

```text
$XDG_DATA_HOME/tarlink/apps/<id>/
├── <version>/.tarlink-package-<sha256-fingerprint>/
├── <previous-version>/.tarlink-package-<sha256-fingerprint>/
└── current -> <version>/.tarlink-package-<sha256-fingerprint>
$XDG_STATE_HOME/tarlink/
├── install.sha256
├── locks/<id>.lock
└── states/<id>.json
$XDG_CACHE_HOME/tarlink/
├── artifacts/
└── registry/
    ├── generations/generation-*/apps/
    └── current -> generations/generation-*
$XDG_CONFIG_HOME/tarlink/repositories.json
$REPOSITORY/
├── repository.json
└── v1/{sha256,sha512}/<digest>
$HOME/.local/bin/tarlink
$HOME/.local/bin/<id>
$XDG_DATA_HOME/applications/tarlink-<id>.desktop
$XDG_DATA_HOME/icons/hicolor/scalable/apps/tarlink-<id>.svg
$XDG_DATA_HOME/icons/hicolor/48x48/apps/tarlink-<id>.<raster-ext>
$XDG_DATA_HOME/icons/hicolor/<WxW>/apps/tarlink-<id>.png
```

Desktop entries reference themed icon identifiers such as `tarlink-<id>` rather
than absolute icon paths. Desktop environments resolve these identifiers using
the standard hicolor theme hierarchy shown above.

Icons are published before the desktop entry that references them, and an icon
is removed only after the entry that references it has been replaced or
removed, so a launcher never becomes visible while its themed icon is missing.
TarLink remains pure Go and never executes external commands, so it cannot
invoke D-Bus, cache updaters, or any desktop-specific refresh. One known
desktop-cache limitation remains: verified on Linux Mint Cinnamon 6.6.9, an
application installed while the session is running appears in the menu without
its icon even though the icon was published before the entry and resolves
correctly in newly started processes; the icon appears once the desktop
reloads its menu (restarting Cinnamon or relogging in), while installs made on
a fresh session resolve normally. The same limitation affects package-manager
installs (upstream linuxmint/Cinnamon#4498), whose triggers only rebuild
system-prefix caches, so no standards-based refresh exists for TarLink to
perform; this is recorded as a desktop-cache limitation, not an integration
defect.

Unset XDG variables fall back to `~/.local/share`, `~/.local/state`, and `~/.cache`. Configured XDG homes must be absolute, control-character-free paths within `$HOME`; TarLink does not manage data outside the user's home tree. Application and registry `current` pointers are relative symlinks constrained below their owning roots. New versions and registry generations are completed before activation, and only the current and one previous generation are retained.

`repositories.json` is the ordered, global user configuration for static
artifact sources; TarLink never reads project-local repository configuration.
Each configured source is either an absolute filesystem path or an HTTPS URL.
Repository objects are immutable digest paths, so ordinary static file tooling
can host them without a directory listing or server-side hash calculation.
Initialized repository roots and public subdirectories are `0755`; the
descriptor and verified objects are `0644` for a separate static reader.
TarLink's sync lock and staging entries remain private and are not part of the
published tree. Repository bytes do not preserve installability by themselves: the trusted release
definitions and their exact runtime references must also remain available in a
validated registry snapshot. The repository contains bytes only; it is not a
registry or a release-definition archive.

The installer records the exact SHA-256 of the canonical TarLink binary in `install.sha256`, using an atomic write. Reinstallation and the bootstrap uninstaller require that private, regular, non-symlink marker to match the binary. Per-application state records the exact executable link and target plus the exact desktop entry and icon paths with content digests when desktop integration is enabled. State is rejected unless those paths equal the canonical layout for the recorded application. TarLink refuses to overwrite or remove an occupied integration that it cannot prove it owns. When a state record is corrupt, uninstall falls back to removing only the TarLink-owned product paths plus the integrations proven by canonical path and content markers — `~/.local/bin` links resolving into the app payload and the canonical desktop entry carrying TarLink's `X-TarLink-AppID` marker while referencing the payload — leaving icons and any other unprovable files in place with warnings.

State records also retain the current and previous resolved-package SHA-256 fingerprints alongside the verified artifact kind (`tar.gz`, `tar.xz`, `zip`, or opaque `appimage`) so lifecycle audits can distinguish package identities and AppImage files. Verified remote PNG icons are retained at a reserved `.tarlink-icon.png` path inside each fingerprinted package payload so re-activation and rollback need no network; their hicolor destination size is recorded in state. Full purge removes the exact application, state, lock, cache, and config product roots — including `repositories.json` and its lock — only after managed applications have been processed. The data and state product parents are removed only when empty, so an unexpected sibling in a product parent stops that parent's removal rather than being deleted. Purge never removes shared XDG parent directories, `~/.local/bin`, or `$XDG_DATA_HOME/applications`; only exact TarLink-owned entries inside those shared directories are removed.
# Static artifact repositories (current main; not in v0.18.0)

TarLink repositories are static trees with a strict `repository.json`
descriptor (`format` `content-repository`, `version` `1`) and optional
`v1/sha256/<lowercase-64-hex>` and `v1/sha512/<lowercase-128-hex>` objects.
There is no listing or index. Descriptors reject unknown fields and
unsupported versions. Objects are opened as regular files, hashed from the
opened descriptor, staged in the repository directory, flushed, and published
with atomic rename; corrupt objects may be repaired by replacement. Unexpected
public-tree entries are rejected, while the narrowly named sync transients are
allowed only during atomic mutation.

Repository sources are ordered in `$XDG_CONFIG_HOME/tarlink/repositories.json`.
Relative paths are made absolute when added; URL sources must be HTTPS and may
include a path prefix. Acquisition checks the local destination/cache, then
these sources, then the registry-declared upstream URL. Filesystem sources do
not use links or hardlinks. A static repository can be published with nginx,
for example by copying the completed tree into a document root, or to S3 with
an external sync tool; TarLink does not perform publication or configure those
services. Offline installation and recovery are not implemented. Any future
offline mode must use a validated local registry snapshot and disable registry
refresh and every network repository source.
