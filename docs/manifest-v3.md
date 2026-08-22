# Manifest v3

Manifest v3 is strict YAML containing exactly one document. It stores a
platform-specific history of TarLink-approved releases. Each release may
declare one explicit inner archive using `nested-archive`; extraction is
bounded to exactly two layers and uses one cumulative resource budget.

```yaml
schema: 3
id: <lowercase application id>
name: <display name>
summary: <short description>
homepage: <HTTPS URL>
categories: [games]
platform: {os: linux, arch: amd64}
release:
  default-channel: stable
  channels: {stable: {current: <version>}}
  releases:
    - channel: stable
      version: <opaque filesystem-safe version>
      url: <immutable HTTPS artifact URL>
      archive: tar.gz | tar.xz | zip | appimage
      nested-archive:
        path: <canonical relative path in outer output>
        archive: tar.gz | tar.xz | zip
      verification:
        algorithm: sha256 | sha512
        digest: <lowercase hexadecimal digest>
        source: <authoritative HTTPS checksum URL>
application:
  executables:
    - name: <command name>
      path: <canonical relative path>
desktop:
  enabled: true
  categories: [Development, Emulator, Game, Graphics, Utility]
  icon:
    path: <canonical relative path inside the extracted application>
    # or, for a verified remote PNG:
    url: <HTTPS PNG URL>
    sha256: <lowercase SHA-256 digest>
```

`nested-archive` is release-specific and optional. Its path is resolved
without following symlinks or hardlinks, and its declared format is checked
against magic bytes. AppImages cannot declare nested archives. Unknown fields,
aliases, anchors, merge keys, multiple documents, malformed digests, and
unsafe paths are rejected.

## Desktop icon

`desktop.icon` is a mapping that must declare exactly one of two forms. The
`path` form references an icon inside the extracted application and keeps the
extension-based hicolor sizing (`scalable` for SVG, otherwise `48x48`). The
remote form requires an HTTPS `url` ending in `.png` and a lowercase SHA-256
`sha256`. The URL path is not required to carry a size token; the hicolor
raster size is determined at install time from the downloaded PNG's signature
and IHDR dimensions, which must be square and one of the supported hicolor
sizes (16, 22, 24, 32, 48, 64, 96, 128, 256, or 512). Remote icons are
downloaded through the same bounded artifact client (maximum 4 MiB), verified
against the declared SHA-256, validated as a PNG header, and retained inside
each version payload so rollback needs no network. Non-PNG, malformed,
non-square, or unsupported-dimension downloads fail installation. AppImage
releases remain opaque: their payload is never extracted, so they cannot
declare an archive-contained `path` icon, but they may declare a verified
remote icon that is installed as a separate external file.

The canonical example is [manifest-v3.example.yaml](../schema/manifest-v3.example.yaml).
