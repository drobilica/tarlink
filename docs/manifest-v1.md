# Manifest v1

Manifest v1 is strict YAML containing exactly one document and these fields:

The registry stores each manifest under one of these exact paths:

```text
apps/<id>/linux-amd64.yaml
apps/<id>/linux-arm64.yaml
```

The filename and the manifest's `platform` fields must agree. A client resolves its exact `GOOS`/`GOARCH` pair and uses only the matching file; missing variants are unavailable rather than falling back to another platform.

```yaml
schema: 1
id: <lowercase application id>
name: <display name>
summary: <short description>
homepage: <HTTPS URL>
categories: [game-development | emulation | graphics | development | utilities]
platform:
  os: linux
  arch: amd64 | arm64
release:
  version: <filesystem-safe version>
  url: <HTTPS release artifact URL>
  archive: tar.gz | tar.xz | zip
  verification:
    algorithm: sha256
    digest: <exact lowercase hexadecimal digest>
    source: <authoritative upstream HTTPS checksum URL>
application:
  executable: <canonical relative path below the extracted root>
desktop:
  enabled: true | false
  categories: [Development | Emulator | Game | Graphics | Utility]
```

SHA-256 digests contain exactly 64 lowercase hexadecimal characters. Other algorithms, missing verification, malformed digests, and non-HTTPS or credential-bearing verification sources are rejected.

`verification.source` records the upstream checksum publication from which registry reviewers obtained the digest. TarLink verifies the artifact against the reviewed digest; it does not substitute a preferred algorithm or derive a different digest.

Unknown fields, aliases, anchors, merge keys, multiple documents, invalid UTF-8, noncanonical paths, unsupported categories, and arbitrary process metadata are rejected. A manifest cannot supply commands, hooks, environment variables, destinations, scripts, or post-install actions.

The canonical example is [manifest-v1.example.yaml](../schema/manifest-v1.example.yaml).
