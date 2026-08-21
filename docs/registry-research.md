# Registry research

These commands are advisory maintainer tooling. They report current GitHub
release evidence and local artifact inspection; neither command approves an
application or changes the official registry.

```text
tarlink registry provenance OWNER/REPO [--release TAG] [--asset NAME] [--json] [--refresh]
tarlink registry inspect OWNER/REPO [--release TAG] [--asset NAME] [--json] [--refresh]
```

Repository input may also be an exact `https://github.com/OWNER/REPO` URL.
The canonical identity is GitHub's case-insensitive owner/repository pair,
stored in lowercase as `owner/repo`; URL paths must contain exactly those two
components and no query, fragment, credentials, encoding tricks, or trailing
path components. This policy is used consistently for discovery and artifact
cache identity.
Release selection uses the tag, while evidence retains GitHub's numeric
release ID. Asset selection uses its exact name, while evidence retains the
numeric asset ID, URL, size, and GitHub digest. If `--asset` is omitted there
must be exactly one asset in the selected release.

Discovery metadata is cached for 24 hours under
`$XDG_CACHE_HOME/tarlink/registry-research/discovery` (or
`$HOME/.cache/tarlink/registry-research/discovery`). `--refresh` bypasses this
metadata cache. Invalid, stale, or unknown-schema cache files are discarded
and refreshed. `--refresh` bypasses discovery metadata but does not force a
redownload of an unchanged immutable asset. Verified artifact bytes are kept
separately under the
`artifacts` child and are keyed by repository, release ID, asset ID, digest
algorithm, and digest. They are reusable after discovery refresh only when
that immutable identity is unchanged. Every inspection re-verifies cached
bytes against the authoritative GitHub digest before parsing; corrupt entries
are discarded and downloaded again. Only assets whose GitHub API upload state is
final/available and whose digest uses TarLink's exact accepted `sha256` or
`sha512` syntax enter the persistent verified cache. Assets without an
acceptable GitHub digest are never placed there. Before parsing, the cache
path must be a regular file (not a symlink or special file); the opened file is
hashed and that same verified object is consumed by the parser.

`ACCEPTABLE` means only that the exact GitHub asset exposes a digest compatible
with TarLink's current SHA-256/SHA-512 contract. It is evidence for review,
not a trust or approval decision. Inspection reports mechanical facts and
blockers such as unsupported artifacts; it never adds installation behavior.

## Candidate workflow

The durable maintainer ledger is
[`registry-research/candidates.yaml`](../registry-research/candidates.yaml).
It is not registry data and is never read by installation or runtime registry
sync. Each entry has a deterministic `id`, canonical `upstream` repository,
the last checked release identity (`release_tag` and numeric `release_id`), a
small status (`blocked`, `deferred`, or `rejected`), durable blocker codes, and
explicit reconsideration conditions. The ledger intentionally excludes raw
GitHub payloads, checksums, archive listings, downloaded paths, and credentials.

Use this sequence at the start of a fresh registry task:

```text
./scripts/agent-context.sh
tarlink registry candidates --changed
tarlink registry inspect OWNER/REPO --json
tarlink registry provenance OWNER/REPO --release TAG --asset NAME --json
```

`candidates --changed` performs lightweight release discovery and compares the
immutable GitHub release ID as well as its tag. A recreated release with the
same tag is therefore `RECHECK`; an unchanged identity is `UNCHANGED`, and a
discovery failure is `ERROR`. It does not download assets or edit the ledger.
`capability:<id>`, `new-upstream-release`, `provenance-policy-change`, and
`manual` are the supported reconsideration conditions. Conditions make an
entry eligible for review; they never approve it automatically.

`tarlink registry blockers` summarizes recurring blockers from the ledger.
`tarlink registry blockers --capability <id>` is an advisory preflight: it
shows which selected capability blockers would be removed, what blockers would
remain, and whether each candidate would be fully unlocked. Run this before
implementing a security or artifact capability whose justification is
onboarding candidates. A zero fully-unlocked result requires an independent
explicit reason to proceed.

For several repositories, use the batch form of `registry inspect` documented
by `tarlink registry inspect --help`; it invokes the same Task 1 inspection per
repository and reports independent results. Batch input is ephemeral and is
not a second ledger format.

The ledger is updated explicitly by maintainers after reviewing fresh
evidence. New upstream releases only produce `RECHECK`; they never cause
automatic approval, manifest generation, commits, or registry changes.
