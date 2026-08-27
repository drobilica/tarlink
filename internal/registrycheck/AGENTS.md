# Registry checker instructions

The `internal/registrycheck` package owns registry structural validation,
artifact selection, materialization, and icon-checking support used by the
registry-checking CLI.

* Reuse TarLink's production Go download, checksum, archive, install, integration, state, and uninstall behavior; do not duplicate those implementations.
* Always perform full registry structural validation.
* Materialize only new or materially changed artifacts by default. Full-registry artifact audits are explicit, not the default for a change.
* Compare schema-v5 changes by resolved package fingerprint, not by the containing `manifest.yaml` file. A change to one artifact must not select an unchanged sibling platform, and formatting-only or informational-only changes select nothing.
* Enforce approved-release history independently for each exact platform definition; there is no manifest-authored revision or schema-v4 comparison fallback.
* Never execute third-party application binaries during registry checks.
* For desktop-enabled application changes, run `tarlink registry icons <registry-path>` first; use `--fix` only when explicitly repairing missing icons. The tooling must not approve icon sources, execute binaries, or widen the manifest icon trust contract.
