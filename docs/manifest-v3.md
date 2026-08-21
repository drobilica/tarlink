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
```

`nested-archive` is release-specific and optional. Its path is resolved
without following symlinks or hardlinks, and its declared format is checked
against magic bytes. AppImages cannot declare nested archives. Unknown fields,
aliases, anchors, merge keys, multiple documents, malformed digests, and
unsafe paths are rejected.

The canonical example is [manifest-v3.example.yaml](../schema/manifest-v3.example.yaml).
