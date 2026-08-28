package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
	"github.com/drobilica/tarlink/internal/upgrade"
	"github.com/drobilica/tarlink/internal/version"
)

type Core struct {
	layout         filesystem.Layout
	installer      *install.Manager
	syncer         *registry.Syncer
	now            func() time.Time
	registryMaxAge time.Duration
	goos           string
	goarch         string
	upgrader       *upgrade.Service
}

func NewCore(layout filesystem.Layout, client *download.Client) (*Core, error) {
	installer := install.New(layout, client)
	syncer := &registry.Syncer{
		CacheRoot: filepath.Join(layout.Cache, "registry"),
		LocksRoot: layout.Locks,
		Client:    client,
	}
	return &Core{
		layout: layout, installer: installer, syncer: syncer,
		now: time.Now, registryMaxAge: registry.DefaultMaxAge,
		goos: runtime.GOOS, goarch: runtime.GOARCH,
		upgrader: &upgrade.Service{Layout: layout, Client: client, Current: version.Current},
	}, nil
}

func (core *Core) CheckTarLinkVersion(ctx context.Context) (TarLinkVersion, error) {
	value, err := core.upgrader.Check(ctx)
	return TarLinkVersion{Current: value.Current, Latest: value.Latest, UpgradeAvailable: upgrade.IsNewer(value.Current, value.Latest)}, err
}

func (core *Core) CheckTarLinkVersionFresh(ctx context.Context) (TarLinkVersion, error) {
	value, err := core.upgrader.CheckFresh(ctx)
	return TarLinkVersion{Current: value.Current, Latest: value.Latest, UpgradeAvailable: upgrade.IsNewer(value.Current, value.Latest)}, err
}

func (core *Core) UpgradeTarLink(ctx context.Context, sink ProgressSink) (TarLinkVersion, error) {
	var value upgrade.Version
	err := core.installer.WithLifecycle(ctx, func() error {
		var upgradeErr error
		value, upgradeErr = core.upgrader.Upgrade(ctx, func(stage string, done, total int64) {
			mapped := ProgressUpgrading
			if stage == "verifying" {
				mapped = ProgressVerifying
			}
			if stage == "installing" {
				mapped = ProgressInstalling
			}
			if stage == "complete" {
				return
			}
			core.emit(sink, mapped, "", done, total)
		})
		return upgradeErr
	})
	if err != nil {
		err = classify("upgrade TarLink", err)
	} else {
		core.emit(sink, ProgressComplete, "", 0, 0)
	}
	return TarLinkVersion{Current: value.Current, Latest: value.Latest, UpgradeAvailable: upgrade.IsNewer(value.Current, value.Latest)}, err
}

// CheckInstallPath inspects the current PATH for conflicts that could hide or
// shadow the executable TarLink would install for appID. It is a read-only,
// pre-install advisory and never modifies the environment or filesystem.
func (core *Core) CheckInstallPath(appID string) ([]integration.PathConflict, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, &Error{Code: CodeInvalidArguments, Op: "check install path", Err: err}
	}
	// The CLI performs this check before installation. Resolve the manifest so
	// every declared command is checked.
	item, _, err := core.resolve(context.Background(), appID, nil)
	if err != nil {
		return nil, err
	}
	if err := core.checkManifestPlatform(item); err != nil {
		return nil, err
	}
	return core.checkItemPath(item), nil
}

func (core *Core) Install(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	selector, item, _, err := core.resolveSelector(ctx, appID, sink)
	if err != nil {
		return Result{}, err
	}
	if err := core.checkManifestPlatform(item); err != nil {
		return Result{}, err
	}
	core.emit(sink, ProgressResolving, appID, 0, 0)
	outcome, err := core.installer.InstallWithOptionsSubject(ctx, item, install.Options{Channel: item.Release.Channel, Explicit: selector.Target != ""}, core.progress(sink, item.ID))
	if err != nil {
		return Result{}, classify("install "+appID, err)
	}
	core.emit(sink, ProgressComplete, item.ID, 0, 0)
	return Result{AppID: item.ID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}, nil
}

func (core *Core) Update(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	selector, item, _, err := core.resolveSelector(ctx, appID, sink)
	if err == nil && selector.Target == "" {
		if installed, stateErr := state.LoadForApp(core.layout, item.ID); stateErr == nil && installed.Channel != "" {
			catalog, catErr := core.catalog(ctx, sink)
			if catErr != nil {
				err = catErr
			} else {
				item, catErr = catalog.ReleaseForPlatform(item.ID, item.Platform.OS, item.Platform.Arch, installed.Channel)
				if catErr != nil {
					err = catErr
				}
			}
		}
	}
	if err != nil {
		return Result{}, err
	}
	if err := core.checkManifestPlatform(item); err != nil {
		return Result{}, err
	}
	core.emit(sink, ProgressResolving, appID, 0, 0)
	outcome, err := core.installer.UpdateWithOptionsSubject(ctx, item, install.Options{Channel: item.Release.Channel, Explicit: selector.Target != ""}, core.progress(sink, item.ID))
	if err != nil {
		return Result{}, classify("update "+appID, err)
	}
	core.emit(sink, ProgressComplete, item.ID, 0, 0)
	return Result{AppID: item.ID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}, nil
}

func (core *Core) Pin(ctx context.Context, appID string) error {
	return core.setPinned(ctx, appID, true)
}
func (core *Core) Unpin(ctx context.Context, appID string) error {
	return core.setPinned(ctx, appID, false)
}

func (core *Core) setPinned(ctx context.Context, appID string, pinned bool) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return &Error{Code: CodeInvalidArguments, Op: "pin", Err: err}
	}
	if err := core.installer.SetPinned(ctx, appID, pinned); err != nil {
		return classify("pin "+appID, err)
	}
	return nil
}

func (core *Core) UpdateAll(ctx context.Context, sink ProgressSink) (UpdateAllResult, error) {
	installed, err := core.installedStates()
	if err != nil {
		return UpdateAllResult{}, err
	}
	result := UpdateAllResult{Failed: make(map[string]string), FailureCodes: make(map[string]ErrorCode)}
	for _, installedState := range installed {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if installedState.Pinned {
			result.Skipped = append(result.Skipped, installedState.App)
			result.Pinned = append(result.Pinned, installedState.App)
			continue
		}
		updated, updateErr := core.Update(ctx, installedState.App, sink)
		if updateErr == nil {
			result.Updated = append(result.Updated, updated)
			continue
		}
		if CodeOf(updateErr) == CodeNoUpdate {
			result.Skipped = append(result.Skipped, installedState.App)
			continue
		}
		result.Failed[installedState.App] = updateErr.Error()
		result.FailureCodes[installedState.App] = CodeOf(updateErr)
	}
	return result, nil
}

func (core *Core) Rollback(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return Result{}, &Error{Code: CodeInvalidArguments, Op: "rollback", Err: err}
	}
	outcome, err := core.installer.RollbackSubject(ctx, appID, core.progress(sink, appID))
	if err != nil {
		return Result{}, classify("rollback "+appID, err)
	}
	core.emit(sink, ProgressComplete, appID, 0, 0)
	return Result{AppID: appID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}, nil
}

func (core *Core) List(ctx context.Context) ([]Application, error) {
	states, err := core.installedStates()
	if err != nil {
		return nil, err
	}
	catalog, catalogErr := core.catalog(ctx, nil)
	goos, goarch := core.platform()
	result := make([]Application, 0, len(states))
	for _, installed := range states {
		value := Application{
			ID: installed.App, Name: installed.App, InstalledVersion: installed.Current,
			PreviousVersion: installed.Previous, InstalledChannel: installed.Channel, Pinned: installed.Pinned,
		}
		if catalogErr == nil {
			if item := catalog.Variants[installed.App][manifest.Platform{OS: goos, Arch: goarch}]; item != nil {
				item = core.itemForInstalledChannel(catalog, item, installed.Channel)
				value = applicationFrom(item, &installed)
			}
		}
		result = append(result, value)
	}
	return result, nil
}

// ListAvailable returns the platform-specific catalog, including installed
// state where a local installation exists.
func (core *Core) ListAvailable(ctx context.Context) ([]Application, error) {
	catalog, err := core.catalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	goos, goarch := core.platform()
	installed, err := core.installedStates()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]state.State, len(installed))
	for _, value := range installed {
		byID[value.App] = value
	}
	items := catalog.SearchForPlatform("", goos, goarch)
	result := make([]Application, 0, len(items))
	for _, item := range items {
		if value, ok := byID[item.ID]; ok {
			item = core.itemForInstalledChannel(catalog, item, value.Channel)
			result = append(result, applicationFrom(item, &value))
		} else {
			result = append(result, applicationFrom(item, nil))
		}
	}
	return result, nil
}

func (core *Core) Info(ctx context.Context, appID string) (Application, error) {
	item, catalog, err := core.resolve(ctx, appID, nil)
	if err != nil {
		return Application{}, err
	}
	installed, stateErr := state.LoadForApp(core.layout, appID)
	if os.IsNotExist(stateErr) {
		return applicationFrom(item, nil), nil
	}
	if stateErr != nil {
		return Application{}, classify("read state", stateErr)
	}
	item = core.itemForInstalledChannel(catalog, item, installed.Channel)
	return applicationFrom(item, &installed), nil
}

func (core *Core) Search(ctx context.Context, query string) ([]Application, error) {
	catalog, err := core.catalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	goos, goarch := core.platform()
	items := catalog.SearchForPlatform(query, goos, goarch)
	result := make([]Application, 0, len(items))
	for _, item := range items {
		installed, stateErr := state.LoadForApp(core.layout, item.ID)
		if os.IsNotExist(stateErr) {
			result = append(result, applicationFrom(item, nil))
			continue
		}
		if stateErr != nil {
			return nil, classify("read state", stateErr)
		}
		item = core.itemForInstalledChannel(catalog, item, installed.Channel)
		result = append(result, applicationFrom(item, &installed))
	}
	return result, nil
}

func (core *Core) Versions(ctx context.Context, appID string) ([]Version, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, &Error{Code: CodeInvalidArguments, Op: "versions", Err: err}
	}
	installed, err := state.LoadForApp(core.layout, appID)
	if os.IsNotExist(err) {
		return nil, &Error{Code: CodeNotInstalled, Op: "versions " + appID, Err: install.ErrNotInstalled}
	}
	if err != nil {
		return nil, classify("versions "+appID, err)
	}
	result := []Version{{Version: installed.Current, Fingerprint: installed.CurrentFingerprint, Status: "current", Channel: installed.Channel, Pinned: installed.Pinned}}
	if installed.Previous != "" {
		result = append(result, Version{Version: installed.Previous, Fingerprint: installed.PreviousFingerprint, Status: "previous", Channel: installed.PreviousChannel})
	}
	if catalog, catErr := core.catalog(ctx, nil); catErr == nil {
		goos, goarch := core.platform()
		if item, itemErr := catalog.ManifestForPlatform(appID, goos, goarch); itemErr == nil {
			seen := map[string]bool{installed.Current: true, installed.Previous: true}
			for _, release := range item.ReleaseHistory.Releases {
				if seen[release.Version] {
					continue
				}
				current := item.ReleaseHistory.Channels[release.Channel].Current == release.Version
				projection := *item
				projection.Release = release
				fingerprint, _ := projection.ResolvedPackageFingerprint()
				result = append(result, Version{Version: release.Version, Fingerprint: fingerprint, Status: "approved", Channel: release.Channel, Current: current, Default: item.ReleaseHistory.DefaultChannel == release.Channel})
				seen[release.Version] = true
			}
		}
	}
	return result, nil
}

// SyncRegistry explicitly fetches and validates the official registry,
// regardless of cache age. The checked-at value is generated by the syncer
// only after successful validation and activation.
func (core *Core) SyncRegistry(ctx context.Context, sink ProgressSink) (time.Time, error) {
	var checkedAt time.Time
	err := core.installer.WithLifecycle(ctx, func() error {
		var syncErr error
		checkedAt, syncErr = core.syncRegistryAt(ctx, sink)
		return syncErr
	})
	if err != nil {
		return time.Time{}, classify("registry sync", err)
	}
	return checkedAt, nil
}

func (core *Core) syncRegistry(ctx context.Context, sink ProgressSink) error {
	_, err := core.syncRegistryAt(ctx, sink)
	return err
}

func (core *Core) syncRegistryAt(ctx context.Context, sink ProgressSink) (time.Time, error) {
	core.syncer.Progress = func(stage string, current, total int64) {
		mapped := ProgressVerifying
		if stage == "downloading" {
			mapped = ProgressDownloading
		}
		if stage == "validating" {
			mapped = ProgressVerifying
		}
		core.emit(sink, mapped, "", current, total)
	}
	if core.now != nil {
		core.syncer.Now = core.now
	} else if core.syncer.Now == nil {
		core.syncer.Now = time.Now
	}
	return core.syncer.SyncWithCheckedAt(ctx)
}

func (core *Core) catalog(ctx context.Context, sink ProgressSink) (*registry.Catalog, error) {
	cacheRoot := filepath.Join(core.layout.Cache, "registry")
	cached, cacheErr := registry.Open(cacheRoot)
	now := time.Now
	if core.now != nil {
		now = core.now
	}
	maxAge := core.registryMaxAge
	if maxAge <= 0 {
		maxAge = registry.DefaultMaxAge
	}
	if cacheErr == nil && !cached.Stale(now(), maxAge) {
		return cached, nil
	}
	var syncErr error
	if lifecycleErr := core.installer.WithLifecycle(ctx, func() error {
		syncErr = core.syncRegistry(ctx, sink)
		return syncErr
	}); lifecycleErr != nil {
		syncErr = lifecycleErr
	}
	if syncErr != nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if cacheErr == nil {
			return cached, nil
		}
		return nil, classify("refresh registry", syncErr)
	}
	refreshed, err := registry.Open(cacheRoot)
	if err != nil {
		return nil, classify("open refreshed registry", err)
	}
	return refreshed, nil
}

func (core *Core) resolve(ctx context.Context, appID string, sink ProgressSink) (*manifest.Manifest, *registry.Catalog, error) {
	_, item, catalog, err := core.resolveSelector(ctx, appID, sink)
	return item, catalog, err
}

func (core *Core) resolveSelector(ctx context.Context, value string, sink ProgressSink) (Selector, *manifest.Manifest, *registry.Catalog, error) {
	selector, err := ParseSelector(value)
	if err != nil {
		return Selector{}, nil, nil, &Error{Code: CodeInvalidArguments, Op: "resolve application", Err: err}
	}
	catalog, err := core.catalog(ctx, sink)
	if err != nil {
		return selector, nil, nil, err
	}
	goos, goarch := core.platform()
	var item *manifest.Manifest
	if selector.Target == "" {
		item, err = catalog.ManifestForPlatform(selector.App, goos, goarch)
	} else {
		item, err = catalog.ReleaseForPlatform(selector.App, goos, goarch, selector.Target)
	}
	if err != nil {
		if errors.Is(err, registry.ErrUnavailableForPlatform) {
			return selector, nil, catalog, &Error{Code: CodeUnsupportedPlatform, Op: "resolve application", Err: err}
		}
		return selector, nil, catalog, &Error{Code: CodeNotFound, Op: "resolve application", Err: err}
	}
	return selector, item, catalog, nil
}

func (core *Core) checkManifestPlatform(item *manifest.Manifest) error {
	goos, goarch := core.platform()
	if item.Platform.OS != goos || item.Platform.Arch != goarch {
		return &Error{
			Code: CodeUnsupportedPlatform,
			Op:   "resolve application",
			Err:  fmt.Errorf("%s is available for %s/%s, not %s/%s", item.ID, item.Platform.OS, item.Platform.Arch, goos, goarch),
		}
	}
	return nil
}

func (core *Core) platform() (string, string) {
	goos, goarch := core.goos, core.goarch
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (core *Core) installedStates() ([]state.State, error) {
	entries, err := os.ReadDir(core.layout.States)
	if os.IsNotExist(err) {
		return []state.State{}, nil
	}
	if err != nil {
		return nil, classify("list installed applications", err)
	}
	result := make([]state.State, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			if strings.HasPrefix(entry.Name(), ".state-") {
				continue
			}
			return nil, &Error{Code: CodeStateCorrupt, Op: "list installed applications", Err: fmt.Errorf("unexpected state entry %q", entry.Name())}
		}
		appID := strings.TrimSuffix(entry.Name(), ".json")
		installed, err := state.LoadForApp(core.layout, appID)
		if err != nil {
			return nil, classify("load state "+appID, err)
		}
		result = append(result, installed)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].App < result[j].App })
	return result, nil
}

func applicationFrom(item *manifest.Manifest, installed *state.State) Application {
	value := Application{
		ID: item.ID, Name: item.Name, Summary: item.Summary, Homepage: item.Homepage,
		Categories: append([]string(nil), item.Categories...), Requirements: append([]string(nil), item.Requirements...), RegistryVersion: item.Release.Version,
		DefaultChannel: item.ReleaseHistory.DefaultChannel,
		ChannelHeads:   make(map[string]string, len(item.ReleaseHistory.Channels)),
	}
	value.RegistryFingerprint, _ = item.ResolvedPackageFingerprint()
	for channel, head := range item.ReleaseHistory.Channels {
		value.ChannelHeads[channel] = head.Current
	}
	value.ApprovedReleases = make([]Version, 0, len(item.ReleaseHistory.Releases))
	for _, release := range item.ReleaseHistory.Releases {
		projection := *item
		projection.Release = release
		fingerprint, _ := projection.ResolvedPackageFingerprint()
		value.ApprovedReleases = append(value.ApprovedReleases, Version{
			Version: release.Version, Fingerprint: fingerprint, Status: "approved", Channel: release.Channel,
			Current: item.ReleaseHistory.Channels[release.Channel].Current == release.Version,
			Default: item.ReleaseHistory.DefaultChannel == release.Channel,
		})
	}
	if installed != nil {
		value.InstalledVersion = installed.Current
		value.InstalledFingerprint = installed.CurrentFingerprint
		value.PreviousVersion = installed.Previous
		value.PreviousFingerprint = installed.PreviousFingerprint
		value.InstalledChannel = installed.Channel
		value.Pinned = installed.Pinned
		value.UpdateAvailable = installed.Current != item.Release.Version || installed.CurrentFingerprint != value.RegistryFingerprint
	}
	return value
}

// itemForInstalledChannel changes only the selected release view. The
// manifest metadata and history remain those of the same approved platform
// variant, while update availability follows the channel retained in state.
func (core *Core) itemForInstalledChannel(catalog *registry.Catalog, item *manifest.Manifest, channel string) *manifest.Manifest {
	if channel == "" || catalog == nil {
		return item
	}
	selected, err := catalog.ReleaseForPlatform(item.ID, item.Platform.OS, item.Platform.Arch, channel)
	if err != nil {
		// A corrupt/retired tracking channel must not silently switch to the
		// default channel. Keep the approved manifest view for diagnostics.
		return item
	}
	return selected
}

func (core *Core) progress(sink ProgressSink, appID string) install.SubjectProgress {
	return func(stage, subject string, current, total int64) {
		mapped := ProgressVerifying
		description := ""
		switch stage {
		case "downloading":
			mapped = ProgressDownloading
		case "verifying":
			mapped = ProgressVerifying
		case "validating-appimage":
			mapped, description = ProgressVerifying, "Validating AppImage"
		case "extracting", "extracting-preparing":
			mapped, description = ProgressExtracting, "Preparing extraction"
		case "installing":
			mapped = ProgressInstalling
		case "integrating":
			mapped = ProgressIntegrating
		case "activating":
			mapped = ProgressActivating
		case "cleaning":
			mapped = ProgressCleaning
		default:
			return
		}
		resource := ProgressSubjectPackageArtifact
		if subject == "remote-desktop-icon" {
			resource = ProgressSubjectRemoteDesktopIcon
		}
		core.emitDetailed(sink, mapped, appID, resource, description, current, total)
	}
}

func (core *Core) emit(sink ProgressSink, stage ProgressStage, appID string, current, total int64) {
	core.emitDetailed(sink, stage, appID, "", "", current, total)
}

func (core *Core) emitDetailed(sink ProgressSink, stage ProgressStage, appID string, subject ProgressSubject, description string, current, total int64) {
	if sink != nil {
		sink(Progress{Stage: stage, Subject: subject, Description: description, AppID: appID, BytesDone: current, BytesTotal: total})
	}
}

func classify(operation string, err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	code := ErrorCode("")
	switch {
	case errors.Is(err, install.ErrAlreadyInstalled):
		code = CodeAlreadyInstalled
	case errors.Is(err, install.ErrNotInstalled):
		code = CodeNotInstalled
	case errors.Is(err, install.ErrNoUpdate):
		code = CodeNoUpdate
	case errors.Is(err, install.ErrPinned):
		code = CodePinned
	case errors.Is(err, install.ErrNoPrevious):
		code = CodeNoUpdate
	case errors.Is(err, locking.ErrConflict):
		code = CodeLockConflict
	case errors.Is(err, state.ErrCorrupt):
		code = CodeStateCorrupt
	case errors.Is(err, download.ErrChecksumMismatch):
		code = CodeChecksum
	case errors.Is(err, download.ErrTooLarge):
		code = CodeNetwork
	case errors.Is(err, download.ErrNetwork):
		code = CodeNetwork
	case errors.Is(err, archive.ErrInvalidFormat), errors.Is(err, archive.ErrPath), errors.Is(err, archive.ErrLimit), errors.Is(err, archive.ErrEntryType), errors.Is(err, archive.ErrCollision), errors.Is(err, archive.ErrDestination):
		code = CodeArchive
	case errors.Is(err, integration.ErrConflict), errors.Is(err, install.ErrConflict):
		code = CodeConflict
	case errors.Is(err, registry.ErrUnavailable):
		code = CodeRegistry
	case errors.Is(err, upgrade.ErrNotOwned), errors.Is(err, upgrade.ErrDevelopment):
		code = CodeConflict
	case errors.Is(err, upgrade.ErrUnsupportedAsset):
		code = CodeUnsupportedPlatform
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		code = CodePermission
	case strings.Contains(operation, "registry"):
		code = CodeRegistry
	case strings.Contains(operation, "download"):
		code = CodeNetwork
	default:
		code = ""
	}
	return &Error{Code: code, Op: operation, Err: err}
}
