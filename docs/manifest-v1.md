# Manifest v1

Manifest v1 is strict YAML containing exactly one document and these fields:

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
    algorithm: sha256 | sha512
    digest: <exact lowercase hexadecimal digest>
    source: <authoritative upstream HTTPS checksum URL>
application:
  executable: <canonical relative path below the extracted root>
desktop:
  enabled: true | false
  categories: [Development | Emulator | Game | Graphics | Utility]
```

SHA-256 digests contain exactly 64 lowercase hexadecimal characters. SHA-512 digests contain exactly 128. MD5, SHA-1, unknown algorithms, missing verification, malformed digests, and non-HTTPS or credential-bearing verification sources are rejected.

`verification.source` records the upstream checksum publication from which registry reviewers obtained the digest. TarLink verifies the artifact against the reviewed digest; it does not substitute a preferred algorithm or derive a different digest.

Unknown fields, aliases, anchors, merge keys, multiple documents, invalid UTF-8, noncanonical paths, unsupported categories, and arbitrary process metadata are rejected. A manifest cannot supply commands, hooks, environment variables, destinations, scripts, or post-install actions.

The canonical example is [manifest-v1.example.yaml](../schema/manifest-v1.example.yaml).
