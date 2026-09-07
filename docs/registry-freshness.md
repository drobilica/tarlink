# Registry freshness discovery

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
Malformed or duplicate entries are rejected deterministically.

Channel filtering is deliberate: `stable` considers published non-prereleases,
while non-stable channels such as `nightly`, `beta`, and `preview` consider
published prereleases. A release is therefore not presented as a candidate for
every channel. This is discovery filtering only; it does not infer approval or
change the channel recorded in the registry.

The result is advisory metadata. It does not contain trusted artifact URLs or
checksums, does not modify registry files, and cannot approve or install a
release. A maintainer selects the exact official upstream HTTPS artifact,
calculates and records its digest, and submits it for normal official-registry
review. Upstream checksum publication is optional. There is no daemon or
scheduled updater.
