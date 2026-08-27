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
$HOME/.local/bin/tarlink
$HOME/.local/bin/<id>
$XDG_DATA_HOME/applications/tarlink-<id>.desktop
$XDG_DATA_HOME/icons/hicolor/scalable/apps/tarlink-<id>.svg
$XDG_DATA_HOME/icons/hicolor/48x48/apps/tarlink-<id>.<raster-ext>
$XDG_DATA_HOME/icons/hicolor/<WxW>/apps/tarlink-<id>.png
```

Unset XDG variables fall back to `~/.local/share`, `~/.local/state`, and `~/.cache`. Configured XDG homes must be absolute, control-character-free paths within `$HOME`; TarLink does not manage data outside the user's home tree. Application and registry `current` pointers are relative symlinks constrained below their owning roots. New versions and registry generations are completed before activation, and only the current and one previous generation are retained.

The installer records the exact SHA-256 of the canonical TarLink binary in `install.sha256`, using an atomic write. Reinstallation and the bootstrap uninstaller require that private, regular, non-symlink marker to match the binary. Per-application state records the exact executable link and target plus the exact desktop entry and icon paths with content digests when desktop integration is enabled. State is rejected unless those paths equal the canonical layout for the recorded application. TarLink refuses to overwrite or remove an occupied integration that it cannot prove it owns.

State records also retain the current and previous resolved-package SHA-256 fingerprints alongside the verified artifact kind (`tar.gz`, `tar.xz`, `zip`, or opaque `appimage`) so lifecycle audits can distinguish package identities and AppImage files. Verified remote PNG icons are retained at a reserved `.tarlink-icon.png` path inside each fingerprinted package payload so re-activation and rollback need no network; their hicolor destination size is recorded in state. Full purge removes the exact application, state, lock, and cache roots only after managed applications have been processed. The data and state product parents are removed only when empty, so an unexpected sibling stops broad cleanup rather than being deleted. Purge never removes shared XDG parent directories, `~/.local/bin`, or `$XDG_DATA_HOME/applications`; only exact TarLink-owned entries inside those shared directories are removed.
