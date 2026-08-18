# Repository instructions

TarLink is a rootless, single-user Linux application manager. Preserve its narrow architecture, trust model, and security boundaries.

## Scope and authority

- Implement only the requested task and changes strictly necessary to make it correct.
- Do not perform unrelated refactors, cleanup, renames, dependency upgrades, redesigns, or speculative features.
- Report unrelated problems; do not fix them unless they block the requested task.
- Preserve unrelated user work. Never discard, stash, overwrite, or force-push it.
- Do not edit `AGENTS.md` unless the task explicitly asks for it.
- When acceptance criteria are met and validation passes, stop. Review the full diff and remove scope creep.

## Security contract

- Production code remains pure Go and compatible with `CGO_ENABLED=0`.
- Do not import `os/exec`, `unsafe`, `plugin`, or `C`, and do not execute external commands.
- Only the compiled official registry is trusted. Release sources remain HTTPS and verified according to the current manifest contract.
- Preserve archive/path/resource limits, safe extraction, same-directory regular-file symlink-chain policy, atomic activation/state, locking, ownership checks, and current-plus-one-previous retention.
- Do not add hardlinks, hooks, scripts, arbitrary commands/arguments, custom install destinations, plugins, telemetry, daemons, self-update, system-wide installation, package-manager integration, or system dependencies.
- A task does not implicitly authorize widening a trust or security boundary. Such changes require explicit user authorization and a deliberate design change.
- Treat `docs/architecture.md`, `docs/security-model.md`, and `docs/threat-model.md` as canonical design references.

## Pre-1.0 policy

- Before `v1.0.0`, prefer clean breaking changes over compatibility code.
- Do not add migrations, deprecated aliases, compatibility filenames, legacy schema support, or fallback behavior unless explicitly requested.

## Agents and Git

- Worker/subagents may inspect, edit, and test their assigned scope, but must not commit, push, tag, publish releases, or change repository settings.
- Before `v1.0.0`, the orchestrating agent should commit and push validated task changes directly to `main` unless the user says otherwise.
- The change that prepares or targets `v1.0.0` must use a branch and pull request.
- After `v1.0.0`, all changes to `main` must go through branches and pull requests.
- Never commit unrelated pre-existing changes.
- Do not bump versions, create tags, or publish releases unless the task explicitly requests it.

## Validation

Before integration/push, run when the Go toolchain is available:

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

Security-sensitive changes require focused success, hostile-input, and failure-path tests plus a short explanation of the affected trust boundary. Documentation must describe implemented behavior or clearly marked plans; never invent commands, registry entries, URLs, or hashes.
