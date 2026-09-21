# Repository instructions

TarLink is a rootless, single-user Linux application manager. Preserve its
narrow architecture, trust model, and security boundaries.

## Scope and authority

* Implement only the requested task and changes strictly necessary to make it correct.
* Do not perform unrelated refactors, cleanup, renames, dependency upgrades, redesigns, or speculative features.
* Report unrelated problems; do not fix them unless they block the requested task.
* Preserve unrelated user work. Never discard, stash, overwrite, or force-push it.
* Do not edit `AGENTS.md` unless the task explicitly asks for it.
* When acceptance criteria are met and validation passes, stop. Review the full diff and remove scope creep.

## Instruction routing

* These root rules apply repository-wide.
* Before modifying a subtree, inspect the closest applicable nested `AGENTS.md`.
* Nested files specialize these rules; do not duplicate repository-wide rules into them.

## Repository boundaries

* `tarlink` owns the application manager, official-registry consumer, and registry validator/maintainer tooling.
* `tarlink-registry` remains declarative application and runtime metadata only;
  it must not contain runtime acquisition or execution logic.
* `tarlink-data` remains the separate external user-selected application-data resolver. Do not move its recipes, data hashes, copyrighted-data mappings, or source configuration here.

## Security invariants

* Only the compiled official registry and official TarLink GitHub releases are trusted; network sources remain HTTPS and exact downloaded bytes remain digest-verified.
* Preserve bounded safe extraction, confined path/link behavior, atomic activation/state, locking, ownership checks, and current-plus-one-previous retention.
* Do not add hardlinks, hooks, scripts, manifest-controlled arbitrary commands/arguments, custom install destinations, plugins, telemetry, daemons, automatic installation, system-wide installation, package-manager integration, or system dependencies.
* Explicit self-upgrade is permitted only through the canonical TarLink-owned binary and verified official release assets.
* Manifest-declared external desktop icons remain the only narrow verified external resource exception; this does not authorize arbitrary downloads.
* Trust-boundary changes require explicit authorization and deliberate design. Treat `docs/architecture.md`, `docs/security-model.md`, and `docs/threat-model.md` as canonical.

## Pre-1.0 policy

* Before `v1.0.0`, prefer clean breaking changes over compatibility code.
* Do not add migrations, deprecated aliases, compatibility filenames, legacy schema support, or fallback behavior unless explicitly requested.

## Git

Task changes use a branch and pull request. The TarLink pull request must pass
the exact required check `Canonical validation` before merge. The safe settings
application order is: first confirm the workflow emits that check reliably on
pull requests, then configure the repository ruleset/branch protection to
require it and require pull requests, then restrict bypasses and verify the
effective ruleset. These settings are a proposal for review; no live settings
are claimed or changed by this task.

* Worker/subagents must not commit, push, tag, publish releases, or change repository settings.
* For validated pre-1.0 changes, the orchestrator commits all and only task-created changes before successful completion.
* All changes use task branches and pull requests; no direct `main` push is part of this policy proposal.
* Never include unrelated pre-existing changes.
* Do not bump versions, create tags, or publish releases unless explicitly requested.

### Remote write verification

* After any authorized remote Git/GitHub write, command success alone is not completion: the orchestrator must read back the authoritative remote state before reporting pushed, merged, tagged, published, PR created/updated, CI complete, or task complete.
* Branch pushes: fetch and verify `origin/<branch>` equals the intended commit (e.g. `git rev-parse origin/<branch>` plus `git ls-remote origin refs/heads/<branch>`). The push exit status is not evidence.
* Pull requests: after creating, updating, or merging, read the PR back and verify base, head, head SHA, and the expected state (open after create/update, merged after merge). A merge counts only when GitHub reports the PR merged and a fresh `origin/main` fetch contains the intended changes; squash/rebase merges leave the branch's commits unreachable. A pushed PR branch or a zero-exit merge command is not a merged PR; report open or blocked PRs as unmerged.
* Tags: verify fresh remote state (e.g. `git ls-remote origin refs/tags/<tag> refs/tags/<tag>^{}`) shows the tag and that it peels to the intended commit, since annotated tags point at a tag object.
* Releases: verify the published release remotely — existence, tag, status, required assets/checksums, and tag-to-commit resolution — not a dispatched workflow, pushed tag, draft, or zero-exit command.
* CI: identify the exact authoritative remote commit after push/merge and require its checks complete and successful; never count local HEAD, PR-head runs, earlier commits, or runs for a different SHA.
* If authoritative remote state does not match the intended result, the remote operation is not complete: reconcile within scope or report the exact blocker, and never convert an attempted remote write into a success claim.
* Completion reports must state the real milestone — local commit created, branch pushed, PR open, PR merged, main updated, tag published, release published, CI green — rather than a generic `done`.

## Validation

Detect the host OS. Run `./scripts/validate.sh --quick` during implementation
and `./scripts/validate.sh` before completion. On Linux, validate natively; on
macOS use ephemeral Linux Podman when available, otherwise run host-compatible
checks and rely on Ubuntu 24.04 GitHub Actions. Security-sensitive changes need
focused success, hostile-input, and failure-path tests. After pushing
Linux-sensitive changes, require authoritative CI green for the exact commit.
Documentation must describe implemented behavior and must not invent commands,
registry entries, URLs, or hashes.
