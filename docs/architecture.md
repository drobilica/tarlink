# TarLink architecture

TarLink is a rootless, single-user Linux application manager for amd64 and
arm64. Each application has one strict v5 manifest at `apps/<id>/manifest.yaml`.
Shared metadata is declared once; retained releases declare exact platform
artifacts. Resolution matches `GOOS`/`GOARCH` exactly and refuses absent
platforms. Executables use either shared or explicit per-platform paths.

## Interface boundaries

`internal/app` composes two service graphs:

- `NewCore` owns registry refresh, catalog, application lifecycle, local launch
  preparation, self-upgrade and doctor.
- `NewMaintainer` owns registry validation, research, artifact inspection,
  candidate construction, blockers and icons. It uses only a filesystem layout
  and download client; it cannot install, self-upgrade, sync the registry or
  infer artifacts from the host platform.

`registry check` deliberately uses the application runtime to exercise real
install/uninstall in a temporary layout. `registry freshness` also uses that
runtime to resolve the approved host-platform manifest.
`registry update` uses the core's explicit official-registry refresh, then
checks all declared application platforms and channels for advisory upstream
candidates newer than each current channel head, independent of the host
platform. Discovery uses compiled reviewed
repository/version/asset rules. Current artifact URLs only explain uncovered
sources; they cannot select a network destination. Each scan reuses repository
metadata across platforms and channels and reports actionable reasons plus a
status tally.
Its `--apply --registry <path>` mode uses the maintainer graph to prepare only the
newest missing release per channel in an explicit local checkout, with the same
artifact inspection and locked atomic publication boundaries as other maintainer
edits. `--dry-run` validates proposed changes without editing that tree. Neither
mode installs candidates or publishes registry metadata.

`cli` handles arguments, streams, JSON and exit codes. `tui` handles Bubble Tea
state and views with Bubbles and Lip Gloss. Both consume the core service and
progress model; neither implements acquisition, verification or publication.
Maintainer inspection derives architecture from asset naming and ELF evidence,
reports contradictions and ambiguity, and never substitutes the host target.

## Trust boundaries

```text
compiled official registry URL
        │ HTTPS, bounded archive, validated apps/ and runtimes/ trees
        ▼
strict v5 manifest and exact platform/channel/version resolution
        │ exact approved HTTPS artifact URL + SHA-256 or SHA-512 digest
        ▼
bounded download ── digest verification ── staging directory
                                                │ safe extraction
                                                ▼
                                   versioned application tree
                                                │ atomic relative symlink
                                                ▼
                                           active version
```

The official registry is the only catalog and artifact-approval authority. TarLink directly enumerates each strict `apps/<id>/manifest.yaml` and resolves only its exact canonical platform entry; there is no generated index, compatibility filename fallback, architecture fallback, secondary approved-source policy, or registry-local parser. A sync validates a staged repository archive, moves its normalized `apps/` and optional `runtimes/` data into a private generation, records the successful checked-at value as private generation metadata, validates and flushes that generation, and atomically changes the relative `current` pointer. Registry refresh retains only the current and immediately previous generations. Historical releases remain registry-approved metadata; they do not imply local retention, and channel heads are never inferred by version sorting.

Static artifact repositories supply bytes, never approval. Acquisition checks
verified local cache objects, then configured repositories in order, then the
registry-declared upstream URL. Every source must supply the exact approved
digest. Repository sync uses one validated registry snapshot and is additive:
it acquires missing objects, repairs required corrupt objects, and retains
all valid objects outside the selected scope. The closure includes application,
exact runtime, and verified remote icon artifacts. Default and `--all-retained`
selection both cover the complete eligible retained release set; opaque
versions have no inferred ordering or release-count bound. See
[Filesystem layout](filesystem-layout.md#static-artifact-repositories) for the
repository format, commands, output, and hosting contract.

Normal registry-dependent commands bootstrap a missing official-registry cache automatically. Valid official-registry caches remain local-only for 24 hours according to their explicit checked-at metadata. A stale cache triggers a refresh attempt; a failed attempt may fall back only to the already validated cache without advancing checked-at. `refresh` always fetches and validates the current official registry, activates it before returning, and prints the successful UTC checked-at value. Local operations such as rollback and uninstall do not require networking. The CLI `list` command enumerates the available platform catalog and annotates installed state; the TUI's installed list remains a separate view.

TarLink release discovery is separate from the application registry. A 24-hour
XDG cache makes version checks advisory and non-blocking for normal operations.
Explicit self-upgrade selects the latest strict stable release, the exact
Linux architecture asset, and `checksums.txt`, then verifies and atomically
replaces the canonical owned binary.

## Runtime-backed execution closures

An application is normally self-contained. A runtime-backed application instead
resolves one immutable closure from the same registry generation: application
release plus exact runtime ID, version, platform, artifact and interface. The
runtime reference belongs to that exact application release because the
application release and exact runtime together form the tested TarLink-managed
execution closure; it is not application-global. Different retained releases
may therefore select different runtimes or remain self-contained.
The only current runtime kind is `steam-linux-runtime`. Runtime manifests live at
`runtimes/<id>/manifest.yaml`; application manifests may only reference an ID
and exact version, never a command, executor path, environment, mount, hook or
script. Runtime metadata participates in the package fingerprint, so a
runtime-only change is an update and rollback retains its earlier closure.

Deployments are stored separately below `~/.local/share/tarlink/runtimes/` and
never have a mutable `current` link. Their outer artifact is SHA-256 verified,
then extracted using the dedicated Valve deployment allowlist and atomically
published before application activation. GC derives liveness from every current
and previous state closure and fails closed when retained state is uncertain.

`tarlink run <id> [-- args...]` uses local state only: it does not refresh the
registry or perform TarLink networking. Self-contained applications run
directly. The compiled Valve adapter invokes `_v2-entry-point
--verb=waitforexitandrun -- PROGRAM [ARGS...]`; its writable variable directory
is outside the immutable deployment. Steam is not required and UMU is not used.

## Installation flow

1. Load or refresh the validated official registry and resolve the application manifest.
2. Resolve the exact `GOOS`/`GOARCH` artifact key in the application manifest and require it to match the running client.
3. Acquire the lifecycle and per-application locks and inspect strict, layout-bound state.
4. Download the declared HTTPS artifact with timeouts, redirect and size bounds. Redirects must remain HTTPS.
5. Verify the exact artifact bytes with the manifest's lowercase SHA-256 or SHA-512 digest before extraction or staging.
6. Extract archives into a private staging directory using the archive path, link, type, count, size, depth, and XZ dictionary limits. AppImages are instead checked as opaque ELF Type 2 files and copied as `appimage` without execution or mounting; a remote icon is downloaded and validated separately and never reads the opaque payload.
7. Validate every declared executable and rename the completed tree into the versioned application directory on the same filesystem.
8. Create only the known executable link and, for desktop integration, the optional icon before the desktop entry that references its themed name in the XDG hicolor hierarchy, then atomically switch the relative `current` link. Icon changes are ownership-checked and rolled back with activation failures. Archive-contained icons keep extension-based sizing (`scalable` for SVG, otherwise `48x48`). Verified remote PNG and SVG icons are downloaded through the bounded artifact client (max 4 MiB) and retained byte-for-byte inside the version payload. PNGs are placed at the hicolor size validated from their signature and IHDR dimensions; SVGs are XML-checked against a restricted static-artwork policy and placed in `scalable` without rasterization.
9. Atomically write explicit ownership state and retain at most the current and one previous version.

No step invokes an archive-provided program, shell, hook, installer, or arbitrary argument.

## Update planning

`PlanUpdates` freezes an ID-ordered selection from one validated catalog
snapshot. An empty selection means every installed application. Unqualified
updates follow each installed channel; explicit channel/version selectors keep
their existing pin override semantics. Each entry reports current and target
package identities, runtime identities, the retained rollback package, and an
`update`, `current`, `pinned`, or `error` status. Same-version fingerprint
changes are updates. Planning may refresh the official catalog but does not
acquire application, runtime, or icon artifacts.

The CLI exposes human and JSON previews with `update --dry-run` and
`update --dry-run --json`, including `--all`. JSON is an inspection format,
not a replay file or an approval source. A separate CLI invocation makes a new
plan. The TUI holds the in-memory plan through review and confirmation.

`ApplyUpdatePlan` checks that the displayed plan is intact, then takes the
lifecycle lock, reloads strict installed state and the validated local catalog,
and re-resolves every actionable target. Changed installed state, a changed
all-installed selection, or changed/withdrawn target fingerprints reject the
whole review before artifact acquisition. Unrelated catalog metadata changes
need not invalidate a plan. Each update also checks its expected state under
the application lock. The batch retains per-application atomicity and continues
after independent acquisition/publication failures; it is not a transaction
across applications. Pin/current skips and planned resolution failures remain
explicit outcomes. Both ordinary CLI updates and TUI confirmation use this
same planner and apply path.

## Repair flow

`tarlink repair <app>` loads valid layout-bound state and resolves its exact
version, channel, platform, and fingerprint through the official catalog.
The installer rechecks the installed identity and metadata under lifecycle
and application locks. It refuses changed or withdrawn package identities,
corrupt state, symlinked payload roots, unexpected active pointers, and
integration files whose ownership cannot be proven.

The normal verified materializer prepares a fresh application tree and icon.
Repair recreates missing integration entries and atomically exchanges an
existing payload with the staged tree using Linux `RENAME_EXCHANGE`; a missing
payload is published by rename. Filesystems without atomic exchange fail
without a remove-then-rename fallback. Missing application directories and the
recorded `current` link can be recreated. Publication failures restore prior
payload, activation, and integrations; a failed payload restoration retains its
staged backup and reports the path. State is never rewritten, preserving the
pin, channel, runtime reference, and rollback identity. The previous payload
is neither repaired nor recreated. Required runtimes use normal immutable
deployment validation/acquisition; repair does not replace a damaged existing
shared runtime. Repair also supports the explicit offline mode below.

## Installed payload verification

The shared materializer records `.tarlink-integrity.json` inside the private
verified payload after executable modes and icons are finalized. The reserved
path must not exist in the upstream payload. Its schema-1 canonical inventory
binds the application ID, version, resolved fingerprint, and AppImage kind to a
sorted list of regular-file SHA-256 digests and sizes, file/directory permission
bits, and symlink targets. The baseline is flushed before publication and travels
with atomic install, repair, update, retention, and rollback. State schema is
unchanged; no inventory is inferred from an existing installation.

`verify <app>` and `verify --all` use layout-bound local state and inspect both
current and previous application payloads without network access, locks, file
writes, application execution, or automatic repair. Confined root-relative
reads do not follow leaf symlinks when hashing files. Symlinks are compared by
target text and never traversed by the tree walk; hardlinked regular files and
special files make verification unavailable. Reads check cancellation and enforce
100,010 entries, 4,096-byte paths, 64 path components, 24 GiB plus 4 MiB cumulative
file bytes, and a 32 MiB inventory limit. Inventory recording enforces these same
limits; an oversized inventory fails installation before publication.

Malformed, missing, noncanonical, or identity-mismatched inventories report
unavailable; they never trigger baseline creation. Byte, mode, target, missing,
and unexpected-file differences report changed. AppImage `appimage.home` and
`appimage.config` trees are excluded as portable user data. Other added payload
files remain unexpected. Self-updated application bytes are drift from the
recorded installation, not proof of malicious modification. Verification does
not gate launch. Runtime deployments, integration files, and external user data
are outside this audit. It is a read-only observation rather than a locked
snapshot: concurrent application writes or lifecycle operations can require a
rerun. Explicit repair replaces the full current payload, including any portable
data inside it, with a freshly verified tree and baseline.

## Offline acquisition

`--offline` is a startup option for `install` and `repair` only. The CLI rejects
other operations before invoking their services, including the TUI, refresh,
self-update and maintainer discovery. The core reads only the validated local
official catalog, even when stale, and never bootstraps or refreshes missing or
invalid metadata. Its successful checked-at value stays unchanged. Advisory
release checks are suppressed; explicit refresh and self-upgrade are blocked
at the core boundary as well.

The download client permits verified cache objects and configured filesystem
repositories, skips HTTPS repositories, and blocks all HTTP entry points before
requests or destination publication. Local sources remain byte suppliers;
only official metadata selects URLs, algorithms and digests.

Offline preflight resolves lock entries before mutation and verifies each
selected application's complete artifact/runtime/remote-icon closure into the
disposable cache. Shared objects are checked once per algorithm, digest and
consumer size limit during a batch preflight. Metadata errors and unavailable
resources are reported before the first selected application is installed;
no runtime deployment is published while a required artifact is unavailable.
Installation and repair recheck their closure and use the normal verified
materializer, so preflight never substitutes for verification on use. Cache
population/repair and lock creation can occur during preflight. Subsequent
extraction, ownership or I/O failures retain the existing per-application
failure guarantees; batch installation is not a cross-application transaction.
No alternate catalog source or registry-import format is introduced.

## Removal flow

Uninstall validates strict state and canonical integration paths, removes only
owned links, desktop entries and icons, then removes the exact application root
and state. Corrupt or layout-invalid state permits degraded removal only of
TarLink product paths and integrations proven by canonical paths and content
markers; unprovable files, including icons, remain with warnings.

Full purge uses that same application cleanup before removing fixed TarLink
data, state, cache and config roots. Shared XDG directories survive and product
parents are removed only when empty. `uninstall.sh` delegates cleanup to the
canonical binary, verifies its retained ownership marker, then removes the
marker and binary. See [Security model](security-model.md#local-ownership-and-lifecycle)
for the exact ownership rules.

## Failure and concurrency

Failed registry refreshes cannot replace the validated cache. Failed downloads,
verification, extraction, integration, activation, or state writes preserve the
previous installation. Mutations and registry refreshes share a cross-process
lifecycle `flock` on the existing home-directory inode, then acquire narrower
per-application or registry locks. Purge cannot remove that lifecycle lock.
`update --all` processes stable application IDs, continues after independent
errors, and returns nonzero when any update fails.

Network and archive limits are part of the
[security contract](security-model.md).
