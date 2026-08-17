package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
)

type Core struct {
	layout    filesystem.Layout
	installer *install.Manager
	syncer    *registry.Syncer
}

func NewCore(layout filesystem.Layout, client *download.Client) (*Core, error) {
	if err := CheckEnvironment(); err != nil {
		return nil, err
	}
	installer := install.New(layout, client)
	syncer := &registry.Syncer{
		CacheRoot: filepath.Join(layout.Cache, "registry"),
		LocksRoot: layout.Locks,
		Client:    client,
	}
	return &Core{layout: layout, installer: installer, syncer: syncer}, nil
}

func (core *Core) Install(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	item, catalog, err := core.resolve(appID)
	if err != nil {
		return Result{}, err
	}
	core.emit(sink, ProgressResolving, appID, 0, 0, "")
	outcome, err := core.installer.Install(ctx, item, core.allowed(catalog, appID), core.progress(sink, appID))
	if err != nil {
		return Result{}, classify("install "+appID, err)
	}
	return Result{AppID: appID, Version: outcome.State.Current, Previous: outcome.State.Previous, Warnings: outcome.Warnings}, nil
}

func (core *Core) Update(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	item, catalog, err := core.resolve(appID)
	if err != nil {
		return Result{}, err
	}
	core.emit(sink, ProgressResolving, appID, 0, 0, "")
	outcome, err := core.installer.Update(ctx, item, core.allowed(catalog, appID), core.progress(sink, appID))
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

func (core *Core) Remove(ctx context.Context, appID string, sink ProgressSink) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return &Error{Code: CodeInvalidArguments, Op: "remove", Err: err}
	}
	if err := core.installer.Remove(ctx, appID, core.progress(sink, appID)); err != nil {
		return classify("remove "+appID, err)
	}
	return nil
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
	catalog, catalogErr := core.catalog()
	result := make([]Application, 0, len(states))
	for _, installed := range states {
		value := Application{
			ID: installed.App, Name: installed.App, InstalledVersion: installed.Current,
			PreviousVersion: installed.Previous,
		}
		if catalogErr == nil {
			if item, ok := catalog.Manifests[installed.App]; ok {
				value = applicationFrom(item, &installed)
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func (core *Core) Info(_ context.Context, appID string) (Application, error) {
	item, _, err := core.resolve(appID)
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

func (core *Core) Search(_ context.Context, query string) ([]Application, error) {
	catalog, err := core.catalog()
	if err != nil {
		return nil, err
	}
	items := catalog.Search(query)
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
	core.syncer.Progress = func(stage string, current, total int64) {
		mapped := ProgressStage(stage)
		if stage == "validating" {
			mapped = ProgressVerifying
		}
		core.emit(sink, mapped, "", current, total, "")
	}
	if err := core.syncer.Sync(ctx); err != nil {
		return classify("registry sync", err)
	}
	return nil
}

func (core *Core) catalog() (*registry.Catalog, error) {
	catalog, err := registry.Open(filepath.Join(core.layout.Cache, "registry"))
	if err != nil {
		return nil, classify("open registry", err)
	}
	return catalog, nil
}

func (core *Core) resolve(appID string) (*manifest.Manifest, *registry.Catalog, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, nil, &Error{Code: CodeInvalidArguments, Op: "resolve application", Err: err}
	}
	catalog, err := core.catalog()
	if err != nil {
		return nil, nil, err
	}
	item, err := catalog.Manifest(appID)
	if err != nil {
		return nil, nil, &Error{Code: CodeNotFound, Op: "resolve application", Err: err}
	}
	return item, catalog, nil
}

func (core *Core) allowed(catalog *registry.Catalog, appID string) download.URLPolicy {
	return func(candidate *url.URL) bool { return catalog.Policy.Allows(appID, candidate) }
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
		core.emit(sink, ProgressStage(stage), appID, current, total, "")
	}
}

func (core *Core) emit(sink ProgressSink, stage ProgressStage, appID string, current, total int64, message string) {
	if sink != nil {
		sink(Progress{Stage: stage, AppID: appID, BytesDone: current, BytesTotal: total, Message: message})
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
