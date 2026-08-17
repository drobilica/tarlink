package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/state"
)

func (core *Core) Uninstall(ctx context.Context, appID string, sink ProgressSink) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return &Error{Code: CodeInvalidArguments, Op: "uninstall", Err: err}
	}
	if err := core.installer.Uninstall(ctx, appID, core.progress(sink, appID)); err != nil {
		return classify("uninstall "+appID, err)
	}
	return nil
}

func (core *Core) UninstallAll(ctx context.Context, sink ProgressSink) error {
	err := core.installer.WithLifecycle(ctx, func() error {
		states, err := core.uninstallStates()
		if err != nil {
			return err
		}
		if err := core.validateUninstallRoots(states); err != nil {
			return err
		}
		var uninstallErrs []error
		for _, installed := range states {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					uninstallErrs = append(uninstallErrs, err)
					break
				}
			}
			if err := core.installer.UninstallLocked(ctx, installed.App, core.progress(sink, installed.App)); err != nil {
				uninstallErrs = append(uninstallErrs, classify("uninstall "+installed.App, err))
			}
			if ctx != nil && ctx.Err() != nil {
				break
			}
		}
		if err := errors.Join(uninstallErrs...); err != nil {
			return err
		}
		return core.removeUninstallRoots()
	})
	if err != nil {
		return classify("uninstall all", err)
	}
	return nil
}

func (core *Core) uninstallStates() ([]state.State, error) {
	if err := core.checkUninstallAnchor(core.layout.StateHome, core.layout.States); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(core.layout.States)
	if errors.Is(err, os.ErrNotExist) {
		return []state.State{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]state.State, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: unexpected state symlink %q", state.ErrCorrupt, entry.Name())
		}
		if strings.HasPrefix(entry.Name(), ".state-") {
			if !strings.HasSuffix(entry.Name(), ".tmp") || !entry.Type().IsRegular() {
				return nil, fmt.Errorf("%w: unexpected state entry %q", state.ErrCorrupt, entry.Name())
			}
			continue
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, fmt.Errorf("%w: unexpected state entry %q", state.ErrCorrupt, entry.Name())
		}
		appID := strings.TrimSuffix(entry.Name(), ".json")
		if err := filesystem.ValidateID(appID); err != nil {
			return nil, fmt.Errorf("%w: state filename %q", state.ErrCorrupt, entry.Name())
		}
		installed, err := state.LoadForApp(core.layout, appID)
		if err != nil {
			return nil, err
		}
		if installed.App != appID {
			return nil, fmt.Errorf("%w: state app does not match filename", state.ErrCorrupt)
		}
		if err := installed.ValidateForLayout(core.layout); err != nil {
			return nil, err
		}
		result = append(result, installed)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].App < result[j].App })
	return result, nil
}

func (core *Core) validateUninstallRoots(states []state.State) error {
	if err := core.checkUninstallAnchor(core.layout.DataHome, core.layout.Apps); err != nil {
		return err
	}
	if err := core.checkUninstallAnchor(core.layout.CacheHome, core.layout.Cache); err != nil {
		return err
	}
	if err := core.checkUninstallAnchor(core.layout.StateHome, core.layout.Locks); err != nil {
		return err
	}
	known := make(map[string]struct{}, len(states))
	for _, installed := range states {
		known[installed.App] = struct{}{}
		spec := integration.Spec{
			ID: installed.App, Executable: installed.Executable,
			ApplicationRoot:   filepath.Join(core.layout.Apps, installed.App),
			LocalBinDirectory: core.layout.Bin, DesktopDirectory: core.layout.Desktop,
			DesktopEnabled: installed.DesktopEnabled, DesktopSHA256: installed.Integration.DesktopSHA256,
		}
		if err := integration.ValidateOwnedForRemoval(spec); err != nil {
			return fmt.Errorf("%s integration: %w", installed.App, err)
		}
	}
	if _, err := os.Lstat(core.layout.Apps); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(core.layout.Apps)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unexpected application symlink %q", state.ErrCorrupt, entry.Name())
		}
		if strings.HasPrefix(entry.Name(), ".staging-") {
			if !entry.IsDir() {
				return fmt.Errorf("%w: unexpected staging entry %q", state.ErrCorrupt, entry.Name())
			}
			continue
		}
		if !entry.IsDir() {
			return fmt.Errorf("%w: unexpected application entry %q", state.ErrCorrupt, entry.Name())
		}
		if _, ok := known[entry.Name()]; !ok {
			return fmt.Errorf("%w: untracked application directory %q", state.ErrCorrupt, entry.Name())
		}
	}
	return nil
}

func (core *Core) checkUninstallAnchor(anchor, path string) error {
	if !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return filesystem.ErrOutsideRoot
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(core.layout.Home, anchor); err != nil {
		return err
	}
	return filesystem.CheckOwnedDirectoryWithin(core.layout.Home, path)
}

func (core *Core) removeUninstallRoots() error {
	for _, root := range []struct {
		anchor string
		path   string
	}{
		{anchor: core.layout.DataHome, path: core.layout.Apps},
		{anchor: core.layout.StateHome, path: core.layout.States},
		{anchor: core.layout.StateHome, path: core.layout.Locks},
		{anchor: core.layout.CacheHome, path: core.layout.Cache},
	} {
		if err := filesystem.SafeRemoveIfExists(root.anchor, root.path); err != nil {
			return err
		}
	}
	for _, parent := range []struct {
		anchor string
		path   string
	}{
		{anchor: core.layout.DataHome, path: filepath.Dir(core.layout.Apps)},
		{anchor: core.layout.StateHome, path: filepath.Dir(core.layout.States)},
	} {
		if err := core.removeEmptyUninstallParent(parent.anchor, parent.path); err != nil {
			return err
		}
	}
	return nil
}

func (core *Core) removeEmptyUninstallParent(anchor, path string) error {
	if !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return filesystem.ErrOutsideRoot
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return filesystem.ErrSymlink
	}
	if !info.IsDir() {
		return fmt.Errorf("TarLink product root is not a directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	return filesystem.SafeRemoveIfExists(anchor, path)
}
