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
- Only the compiled official registry and official TarLink GitHub releases are trusted. Release sources remain HTTPS and verified according to the current manifest/release contract.
- Preserve archive/path/resource limits, safe extraction, same-directory regular-file symlink-chain policy, atomic activation/state, locking, ownership checks, and current-plus-one-previous retention.
- Do not add hardlinks, hooks, scripts, arbitrary commands/arguments, custom install destinations, plugins, telemetry, daemons, automatic installation, system-wide installation, package-manager integration, or system dependencies. Explicit self-upgrade is permitted only through the canonical TarLink-owned binary and verified official release assets.
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

### Development environment and Linux validation

- TarLink targets Linux, but development may occur on Linux or macOS.
- Detect the host operating system before choosing the validation strategy.
- On Linux, run the required validation natively.
- On macOS, do not skip Linux-specific validation merely because the host is macOS.
- When Podman is available, use an ephemeral Linux Podman environment for Linux-specific local validation.
- A macOS host is never sufficient reason to report required Linux validation as unavailable.
- If Podman is unavailable, run all host-compatible validation locally and rely on Ubuntu GitHub Actions for the remaining Linux checks.
- Ubuntu GitHub Actions is the authoritative final integration validation environment.
- After pushing Linux-sensitive changes, inspect the CI run for the exact pushed commit and require it to pass before reporting completion.
- If CI fails, inspect the logs, fix the issue, push again, and repeat until green.

Use the canonical local validation entry point:

```sh
./scripts/validate.sh
```

During implementation, prefer `./scripts/validate.sh --quick`. Before
reporting completion, run `./scripts/validate.sh`.

Registry validation tooling must reuse TarLink's production Go download,
checksum, archive, install, integration, state, and uninstall packages; never
duplicate those implementations. Full registry structural validation is cheap
and always required, while artifact materialization targets only new or
materially changed artifacts. Registry checks must never execute third-party
application binaries. Ubuntu 24.04 GitHub Actions is authoritative Linux CI;
full-registry artifact audits are explicit and are not a default per-change
requirement.

It includes the required checks below and the repository-specific installer, uninstaller, and architecture checks:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

Security-sensitive changes require focused success, hostile-input, and failure-path tests plus a short explanation of the affected trust boundary. Documentation must describe implemented behavior or clearly marked plans; never invent commands, registry entries, URLs, or hashes.
