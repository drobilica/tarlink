# Registry maintenance

The normal contribution path is intentionally short:

```text
choose an official portable Linux HTTPS artifact
        ↓
inspect/download it with bounded TarLink tooling and calculate its digest
        ↓
write the schema-v5 manifest
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
not need to be rehashed. Keep schema-v5 `verification.source` as an honest
official upstream release page or artifact-origin HTTPS URL. It is
informational metadata, not independent checksum provenance. Never fabricate a
checksum URL.

Registry CI resolves the latest published stable TarLink release at the start
of each workflow run and uses that released binary for structural validation
and lifecycle materialization. The resolved version is run-local; neither
repository stores a maintained cross-repository commit or tag pin.

## Inspect and construct a candidate manifest

```text
tarlink registry inspect OWNER/REPO [--release TAG] [--asset NAME] [--json] [--refresh]
tarlink registry inspect https://github.com/OWNER/REPO/releases/download/TAG/ASSET
tarlink registry inspect apps/example/manifest.yaml
tarlink registry inspect ../tarlink-registry --json
tarlink registry add https://github.com/OWNER/REPO/releases/download/TAG/ASSET
tarlink registry add https://github.com/OWNER/REPO/releases/download/TAG/ASSET --non-interactive --json
```

Repository input may also be an exact `https://github.com/OWNER/REPO` URL.
Release and asset selection are explicit when ambiguous. `registry inspect`
downloads the selected official release asset through TarLink's existing
HTTPS-only, timeout- and size-bounded client, calculates exact SHA-256 and
SHA-512 values, and reports archive/AppImage facts without executing the
application. A direct release-asset URL additionally verifies that the exact
owner, repository, release tag, and asset name are present in GitHub metadata.
The locally calculated SHA-256 is the generated manifest digest. GitHub's
supported digest, when supplied, is compared as corroborating evidence;
absence is not a blocker.

For archives, inspection detects content type from the bytes, safely extracts
once within the existing bounds, and ranks static executable and icon
candidates. It never executes application files. A local `manifest.yaml` is
parsed through the schema-v5 implementation and explained as a concise
checklist. A directory scan is local-only, lexical, does not follow symlinked
directories, and searches at most two levels for `manifest.yaml`; it does not
download artifacts or replace `registry check`. JSON is a stable summary with
one result per manifest.

`registry add` consumes the same derived facts and writes nothing by default:
it prints schema-v5 YAML suitable for review. `--output PATH` creates a new
file only and refuses to overwrite an existing path; `--dry-run` prints the
candidate without writing. The interactive command asks only for semantic
metadata (normally a category and, for games/recompilations, bin-link policy)
or a genuinely ambiguous executable/icon selection. Use `--non-interactive`
for automation. It emits `status: needs-input` with stable field/reason codes
when decisions remain; provide reviewed values with `--categories`, and, when
needed, `--create-bin-link` or `--no-create-bin-link`. This tool does not
modify `tarlink-registry`, run Git, or approve an application.

The official registry review remains the approval boundary. `registry inspect`
explains facts, `registry add` constructs a candidate, `registry validate`
authoritatively validates its structure, and `registry check` authoritatively
materializes artifacts through the lifecycle.

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
Automatic application version updates, scheduled manifest mutation, and
registry-update bots remain outside this design.
