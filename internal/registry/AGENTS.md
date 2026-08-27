# Registry cache and sync policy

This package owns the private official-registry cache, staged validation,
generation activation, and cache freshness semantics.

* Accept only the compiled official TarLink registry HTTPS URL and its bounded archive response; do not add mirrors, caller-selected registry URLs, or alternate trust roots.
* Validate staged registry contents before activation, normalize generations to the validated `apps/` tree, and atomically replace only the relative `current` pointer.
* A generation's checked-at value means the UTC time TarLink successfully fetched, validated, and activated it. Store it as explicit internal generation metadata; directory mtimes are not semantic state.
* Publish checked-at metadata as part of the generation before activation. Never advance or report a successful check when fetching, validation, metadata publication, or activation fails.
* An explicit refresh always attempts the current official registry. Normal registry-dependent operations retain the 24-hour stale-cache shortcut and may fall back only to the previously validated active generation after refresh failure.
* A cache generation without required current metadata is disposable/stale; do not add a migration or compatibility metadata path.
* Successful activation must become visible to subsequent catalog operations in the same process. Do not retain a stale in-memory catalog after sync.
* Keep current-plus-one-previous generation retention and the existing lifecycle/registry lock ordering.
* This cache is private to TarLink. Do not expose its layout or couple it to TarLink Data state, recipes, sources, or caches.
* Schema v4 is the current registry contract: each application has exactly one `apps/<id>/manifest.yaml` with shared metadata once and complete platform definitions under exact canonical keys.
* Resolve the runtime platform exactly once into the existing single-platform package view. Never add aliases, fallback, emulation, defaults, or overrides between platform entries.

Use injected clocks and fetch clients for freshness, activation, and failure
tests; do not depend on wall-clock sleeps or the live registry.
