# Manifest contract

This package owns the current strict schema-v5 registry document and its
resolved single-platform package view.

* One application is one `apps/<id>/manifest.yaml`; shared metadata appears once.
* Supported platforms are derived only from exact `linux-amd64` and/or `linux-arm64` artifact keys in retained releases. Omit unsupported platforms.
* Release history remains `current + releases`; single-channel manifests omit channel plumbing, while genuinely multi-channel manifests retain it.
* The resolved package fingerprint, not a manifest-authored revision, identifies materialization and integration inputs.
* Resolve exactly one runtime platform before the install lifecycle. Never add aliases, architecture fallback, inheritance, templates, defaults, or override semantics.
* Preserve bounded input, strict known fields, duplicate/anchor/alias/merge/tag rejection, exact digest and HTTPS validation, and safe path/archive/integration validation.
