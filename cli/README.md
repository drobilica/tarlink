# CLI reference

Generated from Cobra. Regenerate with `./scripts/update-cli-readme.sh`.

- `tarlink [flags]` — Manage verified portable Linux applications
  - `tarlink completion <bash|zsh|fish|powershell>` — Generate a shell completion script
  - `tarlink doctor` — Audit TarLink-managed state
  - `tarlink help [command]` — Help about any command
  - `tarlink info <app> [flags]` — Show application information
  - `tarlink install <app>... [flags]` — Install applications
  - `tarlink installed [flags]` — Write versioned installed-application JSON
  - `tarlink list [flags]` — List available applications
  - `tarlink lock [flags]` — Write an installed-state lock snapshot
  - `tarlink pin <app>` — Pin an installed application
  - `tarlink refresh` — Refresh the application catalog
  - `tarlink registry` — Registry validation and maintainer tools
    - `tarlink registry add <owner/repo | release-asset-url> [flags]` — Construct a candidate manifest
    - `tarlink registry blockers [flags]` — Summarize candidate blockers
    - `tarlink registry candidates [flags]` — Review registry research candidates
      - `tarlink registry candidates discover [flags]` — Compare an external catalog with a local registry
    - `tarlink registry check <path> [flags]` — Check registry artifacts
    - `tarlink registry freshness <app> [flags]` — Inspect upstream release freshness
    - `tarlink registry icons <path> [flags]` — Audit registry desktop icons
    - `tarlink registry inspect <target> [flags]` — Inspect an upstream artifact
    - `tarlink registry validate <path>` — Validate a registry tree
  - `tarlink repository` — Manage static artifact repositories
    - `tarlink repository add URL_OR_PATH` — Add a repository source
    - `tarlink repository init PATH` — Initialize an artifact repository
    - `tarlink repository list` — List repository sources
    - `tarlink repository remove URL_OR_PATH` — Remove a repository source
    - `tarlink repository status PATH [flags]` — Show repository status
    - `tarlink repository sync PATH [flags]` — Mirror approved artifacts additively
    - `tarlink repository verify PATH [flags]` — Verify repository metadata and artifact digests
  - `tarlink rollback <app>` — Roll back an application
  - `tarlink search <query> [flags]` — Search applications
  - `tarlink self-update` — Update TarLink itself
  - `tarlink uninstall <app>... [flags]` — Uninstall applications
  - `tarlink unpin <app>` — Unpin an installed application
  - `tarlink update <app> [flags]` — Update applications
  - `tarlink version` — Show TarLink version
  - `tarlink versions <app> [flags]` — Show application versions

The `run` command is dispatched before Cobra so it can replace the process using only local installed state; it is documented by the main CLI help and is not part of the visible Cobra tree.
