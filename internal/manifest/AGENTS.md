# Manifest contract

This package owns the current strict schema-v4 registry document and its
resolved single-platform package view.

* One application is one `apps/<id>/manifest.yaml`; shared metadata appears once.
* Supported platforms are complete definitions under exact `linux-amd64` and/or `linux-arm64` keys. Omit unsupported platforms.
* Revision, release history, application integration, and desktop integration are independently platform-specific.
* Resolve exactly one runtime platform before the install lifecycle. Never add aliases, architecture fallback, inheritance, templates, defaults, or override semantics.
* Preserve bounded input, strict known fields, duplicate/anchor/alias/merge/tag rejection, exact digest and HTTPS validation, and safe path/archive/integration validation.
