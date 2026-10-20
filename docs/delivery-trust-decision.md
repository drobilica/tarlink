# TarLink delivery trust decision

This is the delivery hardening decision for bootstrap installation, explicit
self-update, and registry CI. It does not implement a signing protocol or
change a trusted authority.

## Threats and current guarantees

The relevant threats are mutable release tags or assets, a compromised
publisher account, truncated or substituted downloads, a malicious registry
change, path/symlink attacks, and failed replacement or removal. TarLink uses
HTTPS, exact stable release identities, SHA-256 checksums, bounded downloads,
ownership markers, confined paths, locks, staged atomic replacement, rollback,
and current-plus-one-previous application retention. Bootstrap and self-update
preserve the last verified installation when download, digest, or activation
fails. Registry CI resolves one published stable TarLink release and verifies
the binary bytes against both GitHub's asset digest and `checksums.txt` before
validation. Registry pull-request validation keeps the required `structural`
and `artifacts` checks running when release resolution fails, and their guard
steps fail closed.

These are integrity and failure-safety guarantees, not independent publisher
authentication. Release tags, assets, the registry, and their checksums remain
publisher-controlled. A checksum delivered by the same compromised publisher
cannot authenticate that publisher. Release tags select identities; neither a
tag nor a checksum published through the same trusted channel supplies an
independent trust root.

## Proposed trust-root distribution

For bootstrap, distribute a future independently authenticated trust root with
the project release process, while retaining the inspectable release-specific
script path as the current bootstrap experience. For self-update, keep the
compiled official GitHub release authority and bind one resolved stable tag to
its checksum and architecture-specific binary. For registry CI, keep the
published stable TarLink release as the validator authority and pass the exact
release ID, asset IDs, and API digests to all consumers in one workflow run.

A future independent root should be published through at least two separately
controlled project channels and recorded in the repository documentation and
release metadata. The specific signing protocol and authority change require a
separate design and are intentionally not part of this task.

## Custody, rotation, and recovery

Release and registry identities should be held by separate least-privilege
maintainer accounts or protected automation identities. Secrets should be
stored only in the hosting provider's protected secret facility, with routine
access review and audit logs. Rotation should overlap old and new identities
long enough to validate one release, then revoke the old identity and document
the effective boundary. A suspected compromise requires stopping publication,
revoking affected credentials, preserving evidence, auditing tags/assets and
registry history, publishing a new identity through the independent trust-root
channels, and issuing a clearly documented recovery release. Existing installed
bytes cannot be retroactively authenticated by replacing a checksum.

## Agent-compatible repository protection

The merge gate requires a pull request and successful GitHub Actions checks on
an up-to-date branch. Agents may merge without a human approval. Administrators
are subject to the gate; force-pushes and branch deletion are disallowed.
TarLink's desired configuration is stored in
[branch-protection.json](../.github/branch-protection.json), with application and
verification instructions in [Contributing](../CONTRIBUTING.md#agent-pull-requests).
The registry uses its existing `structural` and `artifacts` check names; TarLink
uses `Canonical validation`. Require the exact checks only after confirming
that their workflows emit them on every pull request. Read back the effective
protection after changing settings.

Release tag restrictions, identity custody, and independent signing remain
separate proposals. This merge policy does not change the registry or release
trust authorities.
