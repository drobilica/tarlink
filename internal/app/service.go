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
				mapped = ProgressComplete
			}
			core.emit(sink, mapped, "", done, total)
		})
		return upgradeErr
	})
	if err != nil {
		err = classify("upgrade TarLink", err)
	}
	return TarLinkVersion{Current: value.Current, Latest: value.Latest, UpgradeAvailable: upgrade.IsNewer(value.Current, value.Latest)}, err
}

func (core *Core) Install(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	item, _, err := core.resolve(ctx, appID, sink)
	if err != nil {
		return Result{}, err
	}
	if err := core.checkManifestPlatform(item); err != nil {
		return Result{}, err
	}
	core.emit(sink, ProgressResolving, appID, 0, 0)
	outcome, err := core.installer.Install(ctx, item, core.progress(sink, appID))
	if err != nil {
		return Result{}, classify("install "+appID, err)
	}
	return Result{AppID: appID, Version: outcome.State.Current, Previous: outcome.State.Previous, Warnings: outcome.Warnings}, nil
}

func (core *Core) Update(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	item, _, err := core.resolve(ctx, appID, sink)
	if err != nil {
		return Result{}, err
	}
	if err := core.checkManifestPlatform(item); err != nil {
		return Result{}, err
	}
	core.emit(sink, ProgressResolving, appID, 0, 0)
	outcome, err := core.installer.Update(ctx, item, core.progress(sink, appID))
	if err != nil {
		return Result{}, classify("update "+appID, err)
	}
	return Result{AppID: appID, Version: outcome.State.Current, Previous: outcome.State.Previous, Warnings: outcome.Warnings}, nil
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
	outcome, err := core.installer.Rollback(ctx, appID, core.progress(sink, appID))
	if err != nil {
		return Result{}, classify("rollback "+appID, err)
	}
	return Result{AppID: appID, Version: outcome.State.Current, Previous: outcome.State.Previous, Warnings: outcome.Warnings}, nil
}

func (core *Core) List(context.Context) ([]Application, error) {
	states, err := core.installedStates()
	if err != nil {
		return nil, err
	}
	catalog, catalogErr := registry.Open(filepath.Join(core.layout.Cache, "registry"))
	goos, goarch := core.platform()
	result := make([]Application, 0, len(states))
	for _, installed := range states {
		value := Application{
			ID: installed.App, Name: installed.App, InstalledVersion: installed.Current,
			PreviousVersion: installed.Previous,
		}
		if catalogErr == nil {
			if item := catalog.Variants[installed.App][manifest.Platform{OS: goos, Arch: goarch}]; item != nil {
				value = applicationFrom(item, &installed)
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func (core *Core) Info(ctx context.Context, appID string) (Application, error) {
	item, _, err := core.resolve(ctx, appID, nil)
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
		result = append(result, applicationFrom(item, &installed))
	}
	return result, nil
}

func (core *Core) Versions(_ context.Context, appID string) ([]Version, error) {
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
	result := []Version{{Version: installed.Current, Status: "current"}}
	if installed.Previous != "" {
		result = append(result, Version{Version: installed.Previous, Status: "previous"})
	}
	return result, nil
}

func (core *Core) SyncRegistry(ctx context.Context, sink ProgressSink) error {
	if err := core.installer.WithLifecycle(ctx, func() error { return core.syncRegistry(ctx, sink) }); err != nil {
		return classify("registry sync", err)
	}
	return nil
}

func (core *Core) ValidateRegistry(_ context.Context, root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return classify("registry validate", err)
	}
	if _, err := registry.ValidateTree(absolute); err != nil {
		return classify("registry validate", err)
	}
	return nil
}

func (core *Core) syncRegistry(ctx context.Context, sink ProgressSink) error {
	core.syncer.Progress = func(stage string, current, total int64) {
		mapped := ProgressStage(stage)
		if stage == "validating" {
			mapped = ProgressVerifying
		}
		core.emit(sink, mapped, "", current, total)
	}
	return core.syncer.Sync(ctx)
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
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, nil, &Error{Code: CodeInvalidArguments, Op: "resolve application", Err: err}
	}
	catalog, err := core.catalog(ctx, sink)
	if err != nil {
		return nil, nil, err
	}
	goos, goarch := core.platform()
	item, err := catalog.ManifestForPlatform(appID, goos, goarch)
	if err != nil {
		if errors.Is(err, registry.ErrUnavailableForPlatform) {
			return nil, nil, &Error{Code: CodeUnsupportedPlatform, Op: "resolve application", Err: err}
		}
		return nil, nil, &Error{Code: CodeNotFound, Op: "resolve application", Err: err}
	}
	return item, catalog, nil
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
		Categories: append([]string(nil), item.Categories...), RegistryVersion: item.Release.Version,
	}
	if installed != nil {
		value.InstalledVersion = installed.Current
		value.PreviousVersion = installed.Previous
		value.UpdateAvailable = installed.Current != item.Release.Version
	}
	return value
}

func (core *Core) progress(sink ProgressSink, appID string) install.Progress {
	return func(stage string, current, total int64) {
		core.emit(sink, ProgressStage(stage), appID, current, total)
	}
}

func (core *Core) emit(sink ProgressSink, stage ProgressStage, appID string, current, total int64) {
	if sink != nil {
		sink(Progress{Stage: stage, AppID: appID, BytesDone: current, BytesTotal: total})
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
