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
cannot authenticate that publisher. The reviewed GitHub metadata reports the
current `v0.18.0` release as `immutable: false`; its annotated tag is also
unsigned, so the release tag is an identity selector, not an independent trust
root.

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

## Pending repository settings

The settings proposal is unapplied. Apply it in this order:

1. Confirm the exact checks run on pull requests: `Canonical validation` in
   TarLink, and `structural` plus `artifacts` in the registry.
2. Protect TarLink `main` with pull requests required, one approving review,
   stale approval dismissal, and `Canonical validation` required; enforce it
   for administrators, disallow force-push/deletion, and configure no bypass
   actors. Keep the registry's existing `structural`/`artifacts` requirements,
   then add the same review and bypass restrictions.
3. Restrict `v*` tag creation, update, and deletion to the release-maintainer
   identity, disallow force updates, and review the effective release workflow
   token permissions. The draft/publish jobs retain write access; remote
   verification remains read-only.
4. Read back both repositories' effective rulesets/protection and verify a
   normal pull request cannot merge without the named checks.

No live repository settings, branch protection, bypass, release control, or tag
control was changed by this task.
