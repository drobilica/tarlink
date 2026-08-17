# Filesystem layout

TarLink creates only user-owned application roots and narrowly named integrations.

```text
$XDG_DATA_HOME/tarlink/apps/<id>/
├── <version>/
├── <previous-version>/
└── current -> <version>
$XDG_STATE_HOME/tarlink/
├── locks/<id>.lock
└── states/<id>.json
$XDG_CACHE_HOME/tarlink/
├── artifacts/
└── registry/
    ├── generations/<id>/apps/
    └── current -> generations/<id>
$HOME/.local/bin/tarlink
$HOME/.local/bin/<id>
$XDG_DATA_HOME/applications/tarlink-<id>.desktop
```

Unset XDG variables fall back to `~/.local/share`, `~/.local/state`, and `~/.cache`. Configured XDG homes must be absolute, control-character-free paths within `$HOME`; TarLink does not manage data outside the user's home tree. Application and registry `current` pointers are relative symlinks constrained below their owning roots. New versions and registry generations are completed before activation, and only the current and one previous generation are retained.

Per-application state records the exact executable link and target plus the exact desktop entry and its generated-content digest when desktop integration is enabled. State is rejected unless those paths equal the canonical layout for the recorded application. TarLink refuses to overwrite or remove an occupied integration that it cannot prove it owns.

Full purge removes the exact application, state, lock, and cache roots only after managed applications have been processed. The data and state product parents are removed only when empty, so an unexpected sibling stops broad cleanup rather than being deleted. Purge never removes shared XDG parent directories, `~/.local/bin`, or `$XDG_DATA_HOME/applications`; only exact TarLink-owned entries inside those shared directories are removed.
