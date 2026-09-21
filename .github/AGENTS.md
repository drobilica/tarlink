# CI and release policy

These rules apply to GitHub Actions workflows and release support under
`.github/`.

* Keep action dependencies pinned by full immutable commit SHA with a readable version comment.
* All workflow and release-support changes use pull-request branches. Required
  TarLink pull-request validation is the exact check `Canonical validation`.
  Apply repository settings only after that check is observed reliably on pull
  requests, then require the pull request and check, then restrict bypasses and
  verify the effective ruleset. This is an unapplied settings proposal.
* Cross-repository TarLink/registry refs are runtime-resolved only. Never commit a maintained TarLink-to-registry or registry-to-TarLink commit/tag pin.
* When multiple jobs require one external repository snapshot, resolve its current approved branch or latest stable release exactly once per workflow run, record the exact run-local release/asset identity as job outputs, and pass those outputs to all consumers. Verify downloaded bytes with their recorded digests; never write the identity back to the repository.
* Release jobs must build and validate the exact triggering commit and tag, keep `CGO_ENABLED=0`, produce only the canonical binaries and checksum file, and verify remote assets before publication.
* Only the orchestrator may push, tag, edit releases, or invoke release workflows, and only when the task explicitly authorizes release work.
* After a push or tag, inspect GitHub Actions for the exact commit. Do not treat another commit's green run as evidence.
