# Registry checker instructions

The `internal/registrycheck` package owns registry structural validation,
artifact selection, materialization, and icon-checking support used by the
registry-checking CLI.

* Reuse TarLink's production Go download, checksum, archive, install, integration, state, and uninstall behavior; do not duplicate those implementations.
* Always perform full registry structural validation.
* Materialize only new or materially changed artifacts by default. Full-registry artifact audits are explicit, not the default for a change.
* Never execute third-party application binaries during registry checks.
* For desktop-enabled application changes, run `tarlink registry icons <registry-path>` first; use `--fix` only when explicitly repairing missing icons. The tooling must not approve icon sources, execute binaries, or widen the manifest icon trust contract.
