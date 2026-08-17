# Manifest v1

Manifest v1 is strict YAML with exactly one document and exactly these fields:

```yaml
schema: 1
id: <lowercase application id>
name: <display name>
summary: <short description>
homepage: <HTTPS URL>
categories: [game-development | emulation | graphics | development | utilities]
platform:
  os: linux
  arch: amd64
release:
  version: <filesystem-safe version>
  url: <HTTPS release URL>
  sha256: <64 lowercase hexadecimal characters>
  archive: tar.gz | tar.xz | zip
application:
  executable: <relative path below extracted root>
desktop:
  enabled: true | false
  categories: [Development | Emulator | Game | Graphics | Utility]
```

Unknown fields, aliases, anchors, merge keys, multiple documents, invalid UTF-8, non-HTTPS URLs, unsupported categories, and arbitrary process metadata are rejected. A manifest is data only: it cannot supply commands, hooks, arguments, environment variables, destinations, or post-install actions.

The canonical example is [manifest-v1.example.yaml](../schema/manifest-v1.example.yaml).
