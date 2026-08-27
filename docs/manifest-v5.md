# Manifest v5

Manifest v5 is the strict registry contract. Each application is one YAML
document at `apps/<id>/manifest.yaml`. Shared application, release, and
desktop facts are declared once. Platform availability is derived only from
the exact artifact keys in each retained release.

```yaml
schema: 5
id: <lowercase application id>
name: <display name>
summary: <short description>
homepage: <HTTPS URL>
categories: [utilities]
requirements: [original-game-data] # optional
release:
  current: <version>                # omit for the expanded multi-channel form
  archive: tar.gz                   # omit when archives differ by artifact
  verification:
    algorithm: sha256 | sha512
  releases:
    - version: <opaque filesystem-safe version>
      nested-archive:                # optional and release-specific
        path: <canonical relative path in outer output>
        archive: tar.gz | tar.xz | zip
      artifacts:
        linux-amd64:
          url: <immutable HTTPS artifact URL>
          verification:
            digest: <lowercase hexadecimal digest>
            source: <official upstream HTTPS origin>
        linux-arm64:
          url: <immutable HTTPS artifact URL>
          verification:
            digest: <lowercase hexadecimal digest>
            source: <official upstream HTTPS origin>
application:
  executable:
    name: <optional logical command name>
    path: <canonical relative path>
    # or: paths: {linux-amd64: ..., linux-arm64: ...}
    create-bin-link: true
desktop:                               # omit when desktop integration is absent
  executable: <only when multiple logical executables exist>
  working-directory: application-root
  categories: [Development, Emulator, Game, Graphics, Utility]
  icon:
    path: <canonical relative bundled path>
    # or:
    url: <HTTPS PNG URL>
    sha256: <lowercase SHA-256 digest>
```

Single-channel manifests use `release.current` and omit channel plumbing.
Multi-channel manifests retain `default-channel`, `channels`, and a
`channel` on every release entry. Release history remains
`current + releases`; retained releases are never inferred or sorted.

Only `linux-amd64` and `linux-arm64` are valid artifact and executable-path
keys. A missing key means that exact platform is unavailable. TarLink never
uses aliases, architecture substitution, fallback, placeholders, or
inheritance. A common `release.archive` is mutually exclusive with
per-artifact archives; when formats differ, every affected artifact declares
its archive explicitly. The same rule applies to the single shared
verification algorithm.

The exact URL, digest, and source remain attached to each artifact. The source
is required informational metadata and is not fetched at runtime. Nested
archives are release-specific. AppImages are opaque and cannot contain nested
archives or bundled icons.

The singular `application.executable` is the normal form. Use
`application.executables` only for genuinely multiple logical executables.
Exactly one of `path` and `paths` is allowed for each executable. Omitted
names use the safe filesystem basename; `create-bin-link` defaults to true,
except games and recompilations must state it explicitly. Desktop integration
is enabled by the presence of `desktop`; omit `icon` when there is no icon.
Icons are either bundled paths or immutable, HTTPS, verified PNGs.

Schema v5 has no manifest-authored revision. TarLink derives a deterministic
resolved-package fingerprint from the selected release artifact and all
materialization and integration inputs. It excludes summaries, homepages,
registry categories, formatting, and informational verification sources, so
metadata-only edits do not trigger application updates.

Bounded input, one-document parsing, duplicate-key rejection, anchor/alias/
merge/tag rejection, HTTPS and path restrictions, exact digest validation,
archive limits, opaque AppImage handling, nested extraction limits, and
executable and desktop validation remain enforced. Schema v5 is a clean
pre-1.0 break; schema v4 is not parsed or migrated at runtime.

The canonical example is
[manifest-v5.example.yaml](../schema/manifest-v5.example.yaml).
