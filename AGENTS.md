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
* `tarlink-registry` remains declarative application metadata only.
* `tarlink-data` remains the separate external user-selected application-data resolver. Do not move its recipes, data hashes, copyrighted-data mappings, or source configuration here.

## Security invariants

* Only the compiled official registry and official TarLink GitHub releases are trusted; network sources remain HTTPS and exact downloaded bytes remain digest-verified.
* Preserve bounded safe extraction, confined path/link behavior, atomic activation/state, locking, ownership checks, and current-plus-one-previous retention.
* Do not add hardlinks, hooks, scripts, arbitrary commands/arguments, custom install destinations, plugins, telemetry, daemons, automatic installation, system-wide installation, package-manager integration, or system dependencies.
* Explicit self-upgrade is permitted only through the canonical TarLink-owned binary and verified official release assets.
* Manifest-declared external desktop icons remain the only narrow verified external resource exception; this does not authorize arbitrary downloads.
* Trust-boundary changes require explicit authorization and deliberate design. Treat `docs/architecture.md`, `docs/security-model.md`, and `docs/threat-model.md` as canonical.

## Pre-1.0 policy

* Before `v1.0.0`, prefer clean breaking changes over compatibility code.
* Do not add migrations, deprecated aliases, compatibility filenames, legacy schema support, or fallback behavior unless explicitly requested.

## Agent workflow

The primary thread is the orchestrator and owns scope, delegation, integration,
final review, Git actions, releases, and user communication. Default routine
implementation, exploration, testing, and debugging to workers. Use the
specialist only for genuinely difficult architecture, security, lifecycle, or
debugging work. Keep scopes narrow and concurrency small.

### Effort levels

**Effort 1 — small or straightforward**

`orchestrator → worker → orchestrator review`

**Effort 2 — normal engineering work**

`orchestrator → focused worker(s) → orchestrator integration`

**Effort 3 — difficult or high-risk**

`orchestrator → workers + specialist as needed → orchestrator final review`

**Effort 4 — major coordinated change**

`Sol xhigh orchestrator → Luna high implementation/testing workers → Sol xhigh independent reviewer → Sol xhigh orchestrator integration`

Use Effort 4 only when explicitly requested or clearly warranted by broad,
high-impact work; it is not the default. Start the primary session with
`gpt-5.6-sol` and `xhigh`, use the configured `effort4_worker` and `reviewer`
roles, and have the reviewer inspect the integrated diff and validation
evidence rather than worker summaries. Workers and reviewers never perform Git
or release actions.

## Git

* Worker/subagents must not commit, push, tag, publish releases, or change repository settings.
* For validated pre-1.0 changes, the orchestrator commits all and only task-created changes before successful completion.
* Before `v1.0.0`, the orchestrator pushes validated changes directly to `main` unless the user says otherwise. The `v1.0.0` change uses a branch and pull request; after `v1.0.0`, all changes to `main` do.
* Never include unrelated pre-existing changes.
* Do not bump versions, create tags, or publish releases unless explicitly requested.

## Validation

Detect the host OS. Run `./scripts/validate.sh --quick` during implementation
and `./scripts/validate.sh` before completion. On Linux, validate natively; on
macOS use ephemeral Linux Podman when available, otherwise run host-compatible
checks and rely on Ubuntu 24.04 GitHub Actions. Security-sensitive changes need
focused success, hostile-input, and failure-path tests. After pushing
Linux-sensitive changes, require authoritative CI green for the exact commit.
Documentation must describe implemented behavior and must not invent commands,
registry entries, URLs, or hashes.
