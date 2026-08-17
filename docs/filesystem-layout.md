# Filesystem layout

TarLink uses the current user's XDG directories and creates only user-owned paths.

```text
$XDG_DATA_HOME/tarlink/apps/<id>/
├── <version>/                 # complete application tree
├── <previous-version>/        # one retained rollback tree
└── current -> <version>
$XDG_STATE_HOME/tarlink/
├── locks/<id>.lock            # per-application lock
└── states/<id>.json           # explicit schema-versioned state
$XDG_CACHE_HOME/tarlink/
├── artifacts/                 # checksum-keyed release bytes
└── registry/
    ├── generations/<id>/      # complete validated registry generations
    └── current -> generations/<id>
$HOME/.local/bin/<id>          # stable executable link
$XDG_DATA_HOME/applications/tarlink-<id>.desktop
```

If the relevant XDG variable is unset, TarLink uses `~/.local/share`, `~/.local/state`, or `~/.cache`. Registry and application `current` pointers are relative symlinks constrained beneath their owning directory. New generations and versions are completed before activation. TarLink never writes to system directories and exposes no custom install prefix. v0.1 uses the desktop environment's generic application icon and therefore creates no icon file; any future icon support must use clearly TarLink-owned names below `$XDG_DATA_HOME/icons/`.
