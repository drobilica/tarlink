# Manifest v2

Manifest v2 is strict YAML containing exactly one document. It stores a
platform-specific history of TarLink-approved releases; version identifiers
are opaque strings and are never sorted as semantic versions.

The registry stores each manifest under one of these exact paths:

```text
apps/<id>/linux-amd64.yaml
apps/<id>/linux-arm64.yaml
```

The filename and the manifest's `platform` fields must agree. A client resolves
its exact `GOOS`/`GOARCH` pair; missing variants are unavailable rather than
falling back to another platform.

```yaml
schema: 2
id: <lowercase application id>
name: <display name>
summary: <short description>
homepage: <HTTPS URL>
categories: [game-development | emulation | graphics | development | utilities | games | recompilation]
requirements: [original-game-data]
platform:
  os: linux
  arch: amd64 | arm64
release:
  default-channel: stable
  channels:
    stable:
      current: <exact approved version identifier>
  releases:
    - channel: stable
      version: <opaque, filesystem-safe identifier>
      url: <immutable HTTPS release artifact URL>
      archive: tar.gz | tar.xz | zip | appimage
      verification:
        algorithm: sha256 | sha512
        digest: <exact lowercase hexadecimal digest>
        source: <authoritative upstream HTTPS checksum URL>
application:
  executables:
    - name: <installed command name>
      path: <canonical relative path below the extracted root>
desktop:
  enabled: true | false
  categories: [Development | Emulator | Game | Graphics | Utility]
  icon: <optional canonical relative path to an icon below the extracted root>
```

Each channel head must name exactly one release in that channel. A version may
not appear in more than one channel, and an exact version can be installed only
when that release is present in the official registry. The default channel is
explicit; it is not inferred by sorting or by upstream freshness.

All releases use the same HTTPS, checksum, archive, path, and executable
validation rules. Unknown fields, aliases, anchors, merge keys, multiple
documents, malformed digests, unsupported categories, and arbitrary process
metadata are rejected. For `archive: appimage`, every executable mapping targets
the canonical `appimage` payload file; extraction and declarative desktop icons
are not supported.

`requirements` is optional and currently accepts only `original-game-data`. It
indicates legally obtained original game content must be supplied separately by
the user; TarLink does not locate or download that content.

The canonical example is [manifest-v2.example.yaml](../schema/manifest-v2.example.yaml).
