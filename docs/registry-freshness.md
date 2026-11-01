# Registry freshness discovery

Check every application in the official registry with:

```sh
tarlink registry update
tarlink registry update -n 5
tarlink registry update -n 5 --json
```

This command first refreshes and validates the official registry, then checks
every declared application platform and channel against its compiled approved
GitHub upstream. It includes applications that are not installed or available
on the host platform. Built-in reviewed rules select the release repository,
normalize project-specific
numeric tag prefixes/suffixes, and narrow asset names for applications sharing a
repository. Manifest URLs never authorize a new network source. For uncovered
apps, current artifact URLs are used only to explain what is missing:

- `disabled`: the registry explicitly disables the app.
- `unsupported_provider`: GitLab or the named download host needs a provider adapter.
- `unreviewed_repository`: the identified GitHub repository needs a reviewed built-in rule.
- `ambiguous_upstream`: current artifacts identify multiple sources needing review.
- `missing_upstream` or `invalid_upstream`: source evidence is missing or malformed.

Every skipped result includes a human-readable explanation and a next step.
JSON results identify the selected repository and rule when one applies.

Use `-n <count>` (or `--number <count>`) to cap the report at that many
newer missing releases per app/platform across its channels. The count must be
positive; omitting it reports all candidates within the existing lookup bound. Candidates
are sorted newest first by publication time, preserving discovery order for ties
or absent timestamps. The limit applies after filtering approved releases and
channels, equally to text and JSON. It limits output, not API requests or registry
entries.

Each app/platform result is `candidates`, `no-candidates`, `skipped`, or `error`.
The schema-version-1 JSON report includes the validated catalog revision,
registry checked-at time, all results, and a summary counting app/platform results
by status and skip reason. Text output includes the same tally and per-app
explanations. Failed upstream checks preserve
successful results and return a nonzero exit status; cancellation stops the scan.
A failed registry refresh aborts before upstream checks, even if a previous
cache exists. `--offline` is unsupported.

Candidates are published releases missing from that platform/channel's approved
set and newer than its current registry head. Dotted numeric versions are
compared numerically, so `1.10` follows `1.9`; other identifiers require upstream
publication times for both the candidate and current head. Missing chronology
produces an error requiring manual review, rather than a historical list.
Godot uses its official `godotengine/godot-builds` repository; numeric
`-stable` tags map to registry versions without that suffix in discovery and apply.

Candidates still require compatibility review. Discovery and apply examine up to
100 releases using at most two pages of 50 releases, each bounded to 4 MiB.
Advisory requests reject redirects and have a 30-second timeout per request.
API errors produce failed checks rather than a clean result. Reasons distinguish
`repository_moved`, `repository_not_found`, `rate_limited`,
`authentication_failed`, `response_too_large`, `invalid_metadata`, and
`unknown_release_order`; other failures use `lookup_failed`. A moved repository
requires rule review; redirects never silently change the approved source.
Within one scan, shared repositories are fetched once and reused across apps,
platforms, and channels, with filtering applied separately. The next scan fetches
fresh metadata. Set `GH_TOKEN` to use authenticated GitHub API quotas for large
scans; the credential is sent only to the fixed GitHub API endpoint, never to
registry or artifact downloads, and is not stored in reports or caches. The scan
does not change registry manifests, Git state, or installed applications.

To prepare updates in an explicit local checkout:

```sh
tarlink registry update --apply --registry ../tarlink-registry --dry-run
tarlink registry update --apply --registry ../tarlink-registry
```

This maintainer mode validates the selected local tree and compares it directly
with freshly fetched approved upstream releases; it does not refresh the official
catalog cache. It selects only the newest published release per declared channel
and adds it only when newer than the current head and absent from history. It
never backfills older missing releases or downgrades a numeric version.
`-n` cannot be combined with `--apply`.

Every platform of the current channel release must have exactly one upstream
asset matching its platform, archive format, and any reviewed application asset
prefix. This prevents shared repositories from mixing assets for different apps.
AppImage filenames identify Linux implicitly but must still name the architecture;
the acquired binary is independently inspected for that architecture.
`missing_platform_asset` and `ambiguous_platform_asset` explain when selection
needs manual review. Assets must be canonical GitHub
release URLs in the compiled approved repository. Downloads use bounded HTTPS
acquisition and safe static inspection; published supported digests are verified,
and missing upstream digests are handled by computing the registry
algorithm's digest from the exact bytes, as in existing maintainer onboarding. Invalid or mismatched published
digests fail. Executable paths, archive format, runtime references, and desktop
configuration are preserved. Changed executable/icon paths, nested archives,
ambiguous assets, unsupported formats, or inspection blockers require manual review.

The preview performs the same acquisition, inspection, and staged tree validation
without modifying the checkout. Apply adds release entries and advances channel
heads using the existing owned-tree lock, concurrent-edit checks, atomic manifest
replacement, and directory flush. Existing history is retained. Each application's
manifest is published separately; a failure leaves that application unchanged,
while other applications can succeed. JSON and text report per-app outcomes and
errors cause a nonzero exit. As with existing registry edits, a directory-flush
failure after publication is reported with `edit.written: true`.
Review the resulting diff and submit it through normal registry review: this
command does not commit, push, publish, or install anything. `--offline` is
unsupported in both modes.

For a single host-platform application, use
`tarlink registry freshness <app> --json`.

`internal/freshness` is a TarLink-provided, read-only maintainer tool for
discovering GitHub Releases candidates. A caller supplies an explicit
`owner/repository`, application ID, channel, and the versions already approved
by the official registry. The provider queries only GitHub's HTTPS Releases
API and reports upstream tags that are not in that approved set. Numeric
GitHub tags with the conventional `v` prefix are reported using the registry's
canonical unprefixed spelling (for example, `v2.7.519` becomes `2.7.519`);
other version identifiers remain opaque.

The reviewed app-to-repository allowlist lives in
`internal/freshness/approved-upstreams.yaml` and is embedded in the TarLink
binary. It is deliberately separate from registry metadata: maintainers add a
mapping only after verifying the official registry artifact uses that GitHub
repository's Releases, then review the mapping as TarLink trust configuration.
The same rules serve registry-wide discovery, single-app freshness, and local
apply. Numeric-only version rules handle Godot's `-stable`, Conker's `V`, DKR's
`Version`, and TRX's `trx-`; opaque identifiers remain opaque. Conflicting version
rules, malformed fields, and duplicate entries are rejected deterministically.

Channel filtering is deliberate: `stable` considers published non-prereleases,
while non-stable channels such as `nightly`, `beta`, and `preview` consider
published prereleases. A release is therefore not presented as a candidate for
every channel. This is discovery filtering only; it does not infer approval or
change the channel recorded in the registry.

The result is advisory metadata. It does not contain trusted artifact URLs or
checksums, does not modify registry files, and cannot approve or install a
release. The explicit local apply mode composes discovery with the existing
maintainer acquisition, inspection, and registry editing services. A maintainer
selects the exact official upstream HTTPS artifact, calculates and records its digest, and submits it for normal official-registry
review. Upstream checksum publication is optional. There is no daemon or
scheduled updater.
