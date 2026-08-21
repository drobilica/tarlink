# Repository instructions

TarLink is a rootless, single-user Linux application manager. Preserve its narrow architecture, trust model, and security boundaries.

## Scope and authority

* Implement only the requested task and changes strictly necessary to make it correct.
* Do not perform unrelated refactors, cleanup, renames, dependency upgrades, redesigns, or speculative features.
* Report unrelated problems; do not fix them unless they block the requested task.
* Preserve unrelated user work. Never discard, stash, overwrite, or force-push it.
* Do not edit `AGENTS.md` unless the task explicitly asks for it.
* When acceptance criteria are met and validation passes, stop. Review the full diff and remove scope creep.

## Security contract

* Production code remains pure Go and compatible with `CGO_ENABLED=0`.
* Do not import `os/exec`, `unsafe`, `plugin`, or `C`, and do not execute external commands.
* Only the compiled official registry and official TarLink GitHub releases are trusted. Release sources remain HTTPS and verified according to the current manifest/release contract.
* Preserve archive/path/resource limits, safe extraction, same-directory regular-file symlink-chain policy, atomic activation/state, locking, ownership checks, and current-plus-one-previous retention.
* Do not add hardlinks, hooks, scripts, arbitrary commands/arguments, custom install destinations, plugins, telemetry, daemons, automatic installation, system-wide installation, package-manager integration, or system dependencies.
* Explicit self-upgrade is permitted only through the canonical TarLink-owned binary and verified official release assets.
* A task does not implicitly authorize widening a trust or security boundary. Such changes require explicit user authorization and a deliberate design change.
* Treat `docs/architecture.md`, `docs/security-model.md`, and `docs/threat-model.md` as canonical design references.

## Pre-1.0 policy

* Before `v1.0.0`, prefer clean breaking changes over compatibility code.
* Do not add migrations, deprecated aliases, compatibility filenames, legacy schema support, or fallback behavior unless explicitly requested.

## Agent workflow

Use the configured `orchestrator`, `worker`, and `specialist` roles.

* In Codex, the primary thread is the orchestrator; in OpenCode, the configured orchestrator fills that role. It owns planning, delegation, integration, final review, and user communication.
* Default implementation, repository exploration, testing, and routine debugging to workers.
* Use the specialist only for genuinely difficult implementation, debugging, refactoring, or when a worker is blocked.
* Keep delegated scopes focused. Parallelize only independent work.
* Do not spawn redundant agents merely to satisfy a multi-agent workflow.
* The orchestrator should not perform long implementation/test-fix loops when they can reasonably be delegated.
* The orchestrator must review delegated results and the final diff before completion.

### Effort levels

**Effort 1 — small or straightforward**

`orchestrator → worker → orchestrator review`

Use one worker. Escalate only if blocked.

**Effort 2 — normal engineering work**

`orchestrator → worker(s) → orchestrator integration`

Scope the task first. Delegate independent implementation, investigation, and testing. Use the specialist only for difficult portions.

**Effort 3 — difficult or high-risk**

`orchestrator → workers + specialist as needed → orchestrator final review`

Use workers for exploration, routine implementation, and testing. Assign only the genuinely difficult portion to the specialist. The orchestrator owns final integration and regression review.

## Git

* Worker/subagents may inspect, edit, and test their assigned scope, but must not commit, push, tag, publish releases, or change repository settings.
* Before `v1.0.0`, the orchestrator should commit and push validated task changes directly to `main` unless the user says otherwise.
* The change that prepares or targets `v1.0.0` must use a branch and pull request.
* After `v1.0.0`, all changes to `main` must go through branches and pull requests.
* Never commit unrelated pre-existing changes.
* Do not bump versions, create tags, or publish releases unless the task explicitly requests it.

## Validation

TarLink targets Linux, but development may occur on Linux or macOS.

* Detect the host OS before choosing the validation strategy.
* On Linux, run required validation natively.
* On macOS, use an ephemeral Linux Podman environment for Linux-specific validation when available.
* If Podman is unavailable, run all host-compatible validation locally and rely on Ubuntu GitHub Actions for remaining Linux checks.
* Ubuntu 24.04 GitHub Actions is the authoritative final Linux integration environment.
* After pushing Linux-sensitive changes, inspect CI for the exact pushed commit. Fix and repeat until green.

Use the canonical validation entry point:

```sh
./scripts/validate.sh
```

During implementation, prefer:

```sh
./scripts/validate.sh --quick
```

Before reporting completion, run the full validation.

It includes the required checks and repository-specific installer, uninstaller, and architecture checks:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

Registry validation tooling must reuse TarLink's production Go download, checksum, archive, install, integration, state, and uninstall packages; never duplicate those implementations.

* Full registry structural validation is always required.
* Artifact materialization targets only new or materially changed artifacts.
* Registry checks must never execute third-party application binaries.
* Full-registry artifact audits are explicit and are not a default per-change requirement.

Security-sensitive changes require focused success, hostile-input, and failure-path tests plus a short explanation of the affected trust boundary.

Documentation must describe implemented behavior or clearly marked plans; never invent commands, registry entries, URLs, or hashes.

## TUI architecture

* TarLink's interactive TUI uses the Charm v2 stack: Bubble Tea v2 for event/state runtime, Bubbles v2 for reusable components, and Lip Gloss v2 for styling/layout.
* Prefer established components over reimplementing generic viewport, progress, help/keymap, input, or styling primitives.
* Keep TarLink-specific application state and lifecycle logic custom.
* Do not introduce another TUI framework without explicit authorization.
* The TUI remains presentation-only and calls the same `internal/app` service as the CLI.

## Registry candidate research

Before registry or candidate work, run `./scripts/agent-context.sh`, inspect
`registry-research/candidates.yaml`, and run `tarlink registry candidates
--changed`. Reuse Task 1 inspection and provenance evidence; do not repeat
artifact investigation for an unchanged immutable release. Reinvestigate only
when a recorded reconsideration condition is triggered, using manual web
research only for facts canonical tooling cannot establish.

Before implementing a security or artifact capability intended to unblock
applications, run `tarlink registry blockers --capability <capability>` and
record the affected and fully unlocked candidates. If none would be fully
unlocked, do not implement it unless explicitly required for another reason.
The inspector is advisory evidence; official registry manifests remain the
trust boundary.
