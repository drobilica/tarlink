# Registry maintenance

The normal contribution path is intentionally short:

```text
choose an official portable Linux HTTPS artifact
        ↓
inspect/download it with bounded TarLink tooling and calculate its digest
        ↓
write the schema-v4 manifest
        ↓
tarlink registry validate .
        ↓
materialize changed artifacts through tarlink registry check
        ↓
commit / pull request
```

The official registry is the approval boundary. The exact artifact digest is
mandatory, but upstream does not need to publish a checksum file. For a newly
calculated pin, prefer SHA-256; existing valid SHA-256 and SHA-512 releases do
not need to be rehashed. Keep schema-v4 `verification.source` as an honest
official upstream release page or artifact-origin HTTPS URL. It is
informational metadata, not independent checksum provenance. Never fabricate a
checksum URL.

Registry CI resolves the latest published stable TarLink release at the start
of each workflow run and uses that released binary for structural validation
and lifecycle materialization. The resolved version is run-local; neither
repository stores a maintained cross-repository commit or tag pin.

## Artifact inspection

```text
tarlink registry inspect OWNER/REPO [--release TAG] [--asset NAME] [--json] [--refresh]
tarlink registry provenance OWNER/REPO [--release TAG] [--asset NAME] [--json] [--refresh]
```

Repository input may also be an exact `https://github.com/OWNER/REPO` URL.
Release and asset selection are explicit when ambiguous. `registry inspect`
downloads the selected official release asset through TarLink's existing
HTTPS-only, timeout- and size-bounded client, calculates exact SHA-256 and
SHA-512 values, and reports archive/AppImage facts without executing the
application. It remains useful when GitHub or upstream publishes no digest.
Missing upstream checksum publication is not an inspection blocker or an
admission blocker.

When GitHub supplies a supported digest for the exact final uploaded asset,
TarLink can use it to verify a persistent research-cache entry. Otherwise the
bounded download is temporary and the locally calculated digest is reported.
`registry provenance` reports available GitHub release/asset metadata as
advisory corroboration only; its verdict does not approve or reject a registry
entry. The official registry review remains authoritative.

Discovery metadata is cached for 24 hours under
`$XDG_CACHE_HOME/tarlink/registry-research/discovery` (or the corresponding
`$HOME/.cache` location). `--refresh` bypasses discovery metadata but does not
turn the command into an updater. No command here edits manifests, commits
changes, executes application binaries, or automatically accepts a release.

## Candidate tools are optional

[`registry-research/candidates.yaml`](../registry-research/candidates.yaml),
`tarlink registry candidates`, `tarlink registry blockers`, and
`./scripts/agent-context.sh` are advisory backlog tools for coordinated
research. They are not a prerequisite or evidence ledger for an ordinary new
manifest. Use them when maintaining the existing candidate backlog or
evaluating a proposed TarLink capability; do not create replacement provenance
ceremony.

`registry icons` remains the separate bounded desktop-icon workflow:

```text
tarlink registry icons .
tarlink registry icons . --fix
```

It does not execute application binaries or widen the manifest icon contract.
Automatic application version updates, scheduled manifest mutation, candidate
onboarding, and registry-update bots remain outside this design.
