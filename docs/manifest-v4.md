# Manifest v4

Manifest v4 is the current strict registry contract. Each application is one
YAML document at `apps/<id>/manifest.yaml`. Shared application metadata appears
once, and every actually supported platform has a complete definition under
its exact canonical key.

```yaml
schema: 4
id: <lowercase application id>
name: <display name>
summary: <short description>
homepage: <HTTPS URL>
categories: [utilities]
requirements: [original-game-data] # optional
platforms:
  linux-amd64:
    revision: 1 # optional; defaults to 1
    release:
      default-channel: stable
      channels: {stable: {current: <version>}}
      releases:
        - channel: stable
          version: <opaque filesystem-safe version>
          url: <immutable HTTPS artifact URL>
          verification:
            algorithm: sha256 | sha512
            digest: <lowercase hexadecimal digest>
            source: <official upstream release or artifact-origin HTTPS URL>
          archive: tar.gz | tar.xz | zip | appimage
          nested-archive:
            path: <canonical relative path in outer output>
            archive: tar.gz | tar.xz | zip
    application:
      executables:
        - name: <optional logical command name>
          path: <canonical relative path>
          create-bin-link: true
    desktop:
      enabled: true
      executable: <logical executable name>
      working-directory: application-root
      categories: [Development, Emulator, Game, Graphics, Utility]
      icon:
        path: <canonical relative path inside the extracted application>
        # or, for a verified remote PNG:
        url: <HTTPS PNG URL>
        sha256: <lowercase SHA-256 digest>
```

The only platform keys are `linux-amd64` and `linux-arm64`. A manifest omits a
platform when upstream does not publish a supported artifact for it. TarLink
resolves the runtime `GOOS`/`GOARCH` pair to exactly one canonical key and
fails when that entry is absent. It never substitutes, emulates, aliases, or
falls back to another architecture. Platform order has no meaning.

Each platform owns its `revision`, complete release history, executable
integration, and desktop integration. An omitted revision defaults to `1`.
Increment only the affected platform's revision when its packaging or
integration definition changes without a new upstream version. There are no
defaults, overrides, inheritance, templates, URL substitutions, YAML anchors,
or placeholder platform entries.

`nested-archive` is release-specific and optional. Its path is resolved
without following symlinks or hardlinks, and its declared format is checked
against magic bytes. AppImages cannot declare nested archives. Unknown fields,
duplicate keys, aliases, anchors, merge keys, unsupported tags, multiple
documents, malformed digests, and unsafe paths are rejected.

`verification.digest` pins the exact artifact bytes approved by the official
registry. A maintainer may calculate it locally from the selected official
upstream HTTPS artifact; upstream does not need to publish a checksum file.
`verification.source` remains required and records an honest official upstream
release page or artifact-origin location as informational metadata. TarLink
does not fetch or cryptographically bind it at runtime. Schema v4 retains the
same digest, HTTPS, size, archive, extraction, and path restrictions as v0.12.

Executable names default to the basename of `path`; resolved names must be
unique. `create-bin-link` defaults to true. Applications categorized as
`games` or `recompilation` must declare it explicitly for every executable.
Desktop `executable` selects a logical executable and may be omitted when
exactly one exists. The only supported working directory is
`application-root`.

Desktop-enabled platform definitions must explicitly account for icons. The
`path` form references a bundled icon. The remote form requires an HTTPS PNG
URL and exact lowercase SHA-256; downloaded bytes remain bounded, verified,
and validated as a supported square PNG before installation. `icon: null` is
allowed only after maintainer investigation. AppImages are opaque and cannot
declare bundled icons, but may use a verified remote icon.

The canonical example is [manifest-v4.example.yaml](../schema/manifest-v4.example.yaml).
