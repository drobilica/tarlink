// Package install implements TarLink's per-application transactional lifecycle.
package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/appimage"
	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
)

var (
	ErrAlreadyInstalled = errors.New("application is already installed")
	ErrNotInstalled     = errors.New("application is not installed")
	ErrNoUpdate         = errors.New("no update is available")
	ErrPinned           = errors.New("application is pinned")
	ErrNoPrevious       = errors.New("no previous version is retained")
	ErrConflict         = errors.New("unexpected filesystem conflict")
)

type UninstallConflictError struct {
	Path string
}

func (e *UninstallConflictError) Error() string {
	return fmt.Sprintf("integration path is occupied by a non-TarLink file: %s", e.Path)
}

func (e *UninstallConflictError) Unwrap() error { return integration.ErrConflict }

const (
	// remoteIconFile is the reserved relative path where a verified remote
	// desktop icon is retained inside each version payload so activation,
	// doctor checks, and rollback never need the network again.
	remoteIconFile = ".tarlink-icon.png"
	// maxRemoteIconBytes bounds remote desktop icon downloads. The manifest
	// URL must reference a PNG and the archive source limit is identical.
	maxRemoteIconBytes = 4 << 20
)

type Progress func(stage string, current, total int64)
type SubjectProgress func(stage, subject string, current, total int64)

type Outcome struct {
	State    state.State
	Warnings []string
}

// materializedArtifact is a validated application tree that remains private
// to the staging directory. Publishing it and changing activation/state are a
// separate transaction phase.
type materializedArtifact struct {
	stage           string
	applicationRoot string
	// iconSource is the relative source path of the desktop icon inside the
	// version payload. It is either the declared archive path or the reserved
	// retained remote-icon file.
	iconSource string
	// iconSize is the hicolor raster size of a remote PNG icon; zero for
	// archive-contained icons.
	iconSize int
}

// Options carries lifecycle metadata selected by the application service. The
// installer does not resolve channels or versions; it only persists the
// already-validated choice alongside the installation transaction.
type Options struct {
	Channel string
	// Explicit permits a deliberate target change while pinned. It is set by
	// the service only for an explicit channel/version selector.
	Explicit bool
	// Pinned is only meaningful for an existing installation. A fresh install
	// starts unpinned, while updates preserve the existing pin unless callers
	// explicitly change it through state management.
	Pinned *bool
}

type Manager struct {
	Layout      filesystem.Layout
	Client      *download.Client
	Limits      archive.Limits
	LockTimeout time.Duration
	fail        func(stage string) error
	writeState  func(filesystem.Layout, state.State) (bool, error)
}

func New(layout filesystem.Layout, client *download.Client) *Manager {
	if client == nil {
		client = download.NewClient()
	}
	return &Manager{Layout: layout, Client: client, Limits: archive.DefaultLimits()}
}

func (manager *Manager) Install(ctx context.Context, item *manifest.Manifest, progress Progress) (Outcome, error) {
	return manager.InstallWithOptions(ctx, item, Options{}, progress)
}

func (manager *Manager) InstallWithOptions(ctx context.Context, item *manifest.Manifest, options Options, progress Progress) (Outcome, error) {
	return manager.InstallWithOptionsSubject(ctx, item, options, func(stage, _ string, current, total int64) { manager.report(progress, stage, current, total) })
}

func (manager *Manager) InstallWithOptionsSubject(ctx context.Context, item *manifest.Manifest, options Options, progress SubjectProgress) (Outcome, error) {
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.installUnlocked(ctx, item, nil, options, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) installUnlocked(ctx context.Context, item *manifest.Manifest, installed *state.State, options Options, progress SubjectProgress) (Outcome, error) {
	if item == nil {
		return Outcome{}, errors.New("manifest is nil")
	}
	if err := item.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("invalid manifest: %w", err)
	}
	if err := manager.Layout.Ensure(); err != nil {
		return Outcome{}, err
	}
	lock, err := manager.lock(ctx, item.ID)
	if err != nil {
		return Outcome{}, err
	}
	defer lock.Release()
	statePath, err := manager.Layout.StatePath(item.ID)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := state.Load(statePath); err == nil {
		return Outcome{}, ErrAlreadyInstalled
	} else if !os.IsNotExist(err) {
		return Outcome{}, err
	}
	appRoot := filepath.Join(manager.Layout.Apps, item.ID)
	if _, err := os.Lstat(appRoot); err == nil {
		return Outcome{}, fmt.Errorf("%w: untracked application directory %s", ErrConflict, appRoot)
	} else if !os.IsNotExist(err) {
		return Outcome{}, err
	}
	return manager.installVersion(ctx, item, installed, options, progress)
}

func (manager *Manager) Update(ctx context.Context, item *manifest.Manifest, progress Progress) (Outcome, error) {
	return manager.UpdateWithOptions(ctx, item, Options{}, progress)
}

func (manager *Manager) UpdateWithOptions(ctx context.Context, item *manifest.Manifest, options Options, progress Progress) (Outcome, error) {
	return manager.UpdateWithOptionsSubject(ctx, item, options, func(stage, _ string, current, total int64) { manager.report(progress, stage, current, total) })
}

func (manager *Manager) UpdateWithOptionsSubject(ctx context.Context, item *manifest.Manifest, options Options, progress SubjectProgress) (Outcome, error) {
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.updateUnlocked(ctx, item, options, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) updateUnlocked(ctx context.Context, item *manifest.Manifest, options Options, progress SubjectProgress) (Outcome, error) {
	if item == nil {
		return Outcome{}, errors.New("manifest is nil")
	}
	if err := item.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("invalid manifest: %w", err)
	}
	lock, err := manager.lock(ctx, item.ID)
	if err != nil {
		return Outcome{}, err
	}
	defer lock.Release()
	if err := manager.validateManagedRoots(); err != nil {
		return Outcome{}, err
	}
	installed, err := state.LoadForApp(manager.Layout, item.ID)
	if os.IsNotExist(err) {
		return Outcome{}, ErrNotInstalled
	}
	if err != nil {
		return Outcome{}, err
	}
	if installed.Pinned && !options.Explicit {
		return Outcome{}, ErrPinned
	}
	if err := manager.validateManagedApp(item.ID); err != nil {
		return Outcome{}, err
	}
	itemFingerprint, err := item.ResolvedPackageFingerprint()
	if err != nil {
		return Outcome{}, fmt.Errorf("fingerprint manifest: %w", err)
	}
	if installed.Current == item.Release.Version && installed.CurrentFingerprint == itemFingerprint {
		return Outcome{}, ErrNoUpdate
	}
	if installed.Previous == item.Release.Version && installed.PreviousFingerprint == itemFingerprint {
		return manager.activateRetained(item.ID, installed, progress)
	}
	return manager.installVersion(ctx, item, &installed, options, progress)
}

func (manager *Manager) Rollback(ctx context.Context, appID string, progress Progress) (Outcome, error) {
	return manager.RollbackSubject(ctx, appID, func(stage, _ string, current, total int64) { manager.report(progress, stage, current, total) })
}

func (manager *Manager) RollbackSubject(ctx context.Context, appID string, progress SubjectProgress) (Outcome, error) {
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.rollbackUnlocked(ctx, appID, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) rollbackUnlocked(ctx context.Context, appID string, progress SubjectProgress) (Outcome, error) {
	lock, err := manager.lock(ctx, appID)
	if err != nil {
		return Outcome{}, err
	}
	defer lock.Release()
	if err := manager.validateManagedRoots(); err != nil {
		return Outcome{}, err
	}
	installed, err := state.LoadForApp(manager.Layout, appID)
	if os.IsNotExist(err) {
		return Outcome{}, ErrNotInstalled
	}
	if err != nil {
		return Outcome{}, err
	}
	if err := manager.validateManagedApp(appID); err != nil {
		return Outcome{}, err
	}
	if installed.Previous == "" {
		return Outcome{}, ErrNoPrevious
	}
	return manager.activateRetained(appID, installed, progress)
}

func (manager *Manager) Uninstall(ctx context.Context, appID string, progress Progress) ([]string, error) {
	return manager.UninstallSubject(ctx, appID, func(stage, _ string, current, total int64) { manager.report(progress, stage, current, total) })
}

func (manager *Manager) UninstallSubject(ctx context.Context, appID string, progress SubjectProgress) ([]string, error) {
	var warnings []string
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		warnings, err = manager.uninstallUnlocked(ctx, appID, progress)
		return err
	})
	return warnings, err
}

func (manager *Manager) uninstallUnlocked(ctx context.Context, appID string, progress SubjectProgress) ([]string, error) {
	lock, err := manager.lock(ctx, appID)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	for _, root := range []string{manager.Layout.States, manager.Layout.Apps} {
		if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, root); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	installed, err := state.LoadForApp(manager.Layout, appID)
	if os.IsNotExist(err) {
		return nil, ErrNotInstalled
	}
	if err != nil {
		if errors.Is(err, state.ErrCorrupt) {
			return manager.uninstallDegradedLocked(ctx, appID, progress)
		}
		return nil, err
	}
	if err := installed.ValidateForLayout(manager.Layout); err != nil {
		return manager.uninstallDegradedLocked(ctx, appID, progress)
	}
	spec, err := manager.integrationSpec(installed, "", nil)
	if err != nil {
		return nil, err
	}
	conflicts, err := integration.RemovalConflicts(spec)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		return nil, &UninstallConflictError{Path: conflicts[0]}
	}
	if err := integration.ValidateOwnedForRemoval(spec); err != nil {
		return nil, err
	}
	manager.report(progress, "cleaning", 0, 0)
	if err := manager.inject("before_uninstall"); err != nil {
		return nil, err
	}
	if err := integration.RemoveOwned(spec); err != nil {
		return nil, err
	}
	appRoot := filepath.Join(manager.Layout.Apps, appID)
	if err := filesystem.SafeRemoveIfExists(manager.Layout.Apps, appRoot); err != nil {
		return nil, err
	}
	statePath, err := manager.Layout.StatePath(appID)
	if err != nil {
		return nil, err
	}
	if err := removeStateFile(statePath); err != nil {
		return nil, err
	}
	return nil, nil
}

// UninstallDegradedLocked removes an application whose state file is corrupt
// under the per-application lock. It derives everything from the canonical
// layout and the application ID and never trusts the corrupt state file.
func (manager *Manager) UninstallDegradedLocked(ctx context.Context, appID string, progress SubjectProgress) ([]string, error) {
	lock, err := manager.lock(ctx, appID)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	return manager.uninstallDegradedLocked(ctx, appID, progress)
}

// uninstallDegradedLocked blasts through a corrupt state file using only the
// layout and appID. Integrations are removed first, then the payload, then the
// state file; every error is a hard failure so a retry stays possible. Paths
// whose TarLink ownership cannot be proven are left in place and reported.
func (manager *Manager) uninstallDegradedLocked(ctx context.Context, appID string, progress SubjectProgress) ([]string, error) {
	var warnings []string
	manager.report(progress, "cleaning", 0, 0)
	payloadRoot := filepath.Join(manager.Layout.Apps, appID)

	// Canonical desktop entry: remove it only when its ownership marker and
	// its Exec/TryExec payload reference both prove TarLink ownership. It is
	// checked before the bin link sweep so entries whose Exec value is the
	// executable link remain provable through that link.
	desktopPath := filepath.Join(manager.Layout.Desktop, "tarlink-"+appID+".desktop")
	info, err := os.Lstat(desktopPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case info.Mode()&os.ModeSymlink != 0:
		warnings = append(warnings, fmt.Sprintf("desktop entry %s is a symlink; left in place", desktopPath))
	case !info.Mode().IsRegular():
		warnings = append(warnings, fmt.Sprintf("desktop entry %s is not a regular file; left in place", desktopPath))
	case info.Size() > 64<<10:
		warnings = append(warnings, fmt.Sprintf("desktop entry %s exceeds the size limit; left in place", desktopPath))
	default:
		file, err := os.Open(desktopPath)
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(file, 64<<10+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(content) > 64<<10 {
			warnings = append(warnings, fmt.Sprintf("desktop entry %s exceeds the size limit; left in place", desktopPath))
			break
		}
		lines := strings.Split(string(content), "\n")
		if !hasDesktopMarker(lines, appID) {
			warnings = append(warnings, fmt.Sprintf("desktop entry %s has no TarLink marker; left in place", desktopPath))
		} else if value, ok := desktopPayloadValue(lines); !ok || !manager.desktopValueReferencesPayload(payloadRoot, value) {
			warnings = append(warnings, fmt.Sprintf("desktop entry %s does not reference the application payload; left in place", desktopPath))
		} else if err := rejectSymlinkParents(manager.Layout.Desktop, desktopPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("desktop entry %s parent is unsafe; left in place", desktopPath))
		} else if err := os.Remove(desktopPath); err != nil {
			return nil, err
		}
	}

	// Bin link sweep: remove symlinks under Bin that resolve inside the
	// payload. Non-symlink entries are never touched.
	if binInfo, err := os.Lstat(manager.Layout.Bin); err == nil {
		if binInfo.Mode()&os.ModeSymlink == 0 && binInfo.IsDir() {
			entries, err := os.ReadDir(manager.Layout.Bin)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if entry.Type()&os.ModeSymlink == 0 {
					continue
				}
				linkPath := filepath.Join(manager.Layout.Bin, entry.Name())
				target, err := os.Readlink(linkPath)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("bin link %s: %v", linkPath, err))
					continue
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(manager.Layout.Bin, target)
				}
				if !manager.payloadContained(payloadRoot, filepath.Clean(target)) {
					continue
				}
				if err := os.Remove(linkPath); err != nil {
					return nil, err
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Icon leftovers are reported but never removed.
	if iconInfo, err := os.Lstat(manager.Layout.Icons); err == nil {
		if iconInfo.Mode()&os.ModeSymlink == 0 && iconInfo.IsDir() {
			leftovers, err := scanIconLeftovers(manager.Layout.Icons, appID)
			if err != nil {
				return nil, err
			}
			for _, path := range leftovers {
				warnings = append(warnings, "leftover desktop icon (not removed): "+path)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	// Payload removal is anchored to the apps root and refuses a top-level
	// symlink. The state file is removed last so a retry stays possible.
	if err := filesystem.SafeRemoveIfExists(manager.Layout.Apps, payloadRoot); err != nil {
		return nil, err
	}
	statePath, err := manager.Layout.StatePath(appID)
	if err != nil {
		return nil, err
	}
	if err := removeStateFile(statePath); err != nil {
		return nil, err
	}
	return warnings, nil
}

// payloadContained reports whether path resolves inside the payload rooted at
// payloadRoot. Real paths are compared after resolving every symlink; a
// dangling target falls back to textual containment against the payload root.
func (manager *Manager) payloadContained(payloadRoot, path string) bool {
	payloadReal, payloadErr := filepath.EvalSymlinks(payloadRoot)
	targetReal, targetErr := filepath.EvalSymlinks(path)
	if payloadErr == nil && targetErr == nil {
		return containedWithin(payloadRoot, targetReal) || containedWithin(payloadReal, targetReal)
	}
	return containedWithin(payloadRoot, path)
}

func containedWithin(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// hasDesktopMarker reports whether the desktop entry contains the exact
// TarLink ownership marker line for the application.
func hasDesktopMarker(lines []string, appID string) bool {
	marker := "X-TarLink-AppID=" + appID
	for _, line := range lines {
		if line == marker {
			return true
		}
	}
	return false
}

// desktopPayloadValue returns the unescaped Exec or TryExec value of a
// desktop entry. Exec is written by desktopExec and may be quoted with
// backslash escapes; TryExec is written by desktopText with doubled
// backslashes.
func desktopPayloadValue(lines []string) (string, bool) {
	for _, key := range []string{"Exec", "TryExec"} {
		prefix := key + "="
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				value := unescapeDesktopValue(line[len(prefix):])
				if value != "" {
					return value, true
				}
			}
		}
	}
	return "", false
}

// unescapeDesktopValue reverses the desktopText/desktopExec encodings: a
// quoted Exec value carries backslash escapes for \ " ` $ and doubled percent
// signs, while a raw TryExec value doubles backslashes.
func unescapeDesktopValue(value string) string {
	quoted := len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"'
	if quoted {
		value = value[1 : len(value)-1]
	}
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '%' && i+1 < len(value) && value[i+1] == '%':
			out.WriteByte('%')
			i++
		case value[i] == '\\' && i+1 < len(value):
			out.WriteByte(value[i+1])
			i++
		default:
			out.WriteByte(value[i])
		}
	}
	return out.String()
}

// desktopValueReferencesPayload reports whether an unescaped desktop Exec or
// TryExec value points at the application payload, either directly or through
// a symlink whose target resolves into the payload.
func (manager *Manager) desktopValueReferencesPayload(payloadRoot, value string) bool {
	if manager.payloadContained(payloadRoot, value) {
		return true
	}
	info, err := os.Lstat(value)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	target, err := os.Readlink(value)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(value), target)
	}
	return manager.payloadContained(payloadRoot, filepath.Clean(target))
}

// rejectSymlinkParents rejects a removal path when root itself or any
// component from root through path is a symlink.
func rejectSymlinkParents(root, path string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return filesystem.ErrOutsideRoot
	}
	if info, err := os.Lstat(root); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return filesystem.ErrSymlink
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filesystem.ErrOutsideRoot
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return filesystem.ErrSymlink
		}
	}
	return nil
}

// scanIconLeftovers collects regular icon files under root whose base name
// carries the application prefix. The walk is depth-bounded and never follows
// symlinks; icons are reported but never removed.
func scanIconLeftovers(root, appID string) ([]string, error) {
	const maxDepth = 4
	prefix := "tarlink-" + appID + "."
	var result []string
	var walk func(string, int) error
	walk = func(current string, depth int) error {
		if depth > maxDepth {
			return nil
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := filepath.Join(current, entry.Name())
			if entry.IsDir() {
				if err := walk(child, depth+1); err != nil {
					return err
				}
				continue
			}
			if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), prefix) {
				result = append(result, child)
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	return result, nil
}

func (manager *Manager) RemoveUninstallConflict(ctx context.Context, appID, path string) error {
	return manager.WithLifecycle(ctx, func() error {
		lock, err := manager.lock(ctx, appID)
		if err != nil {
			return err
		}
		defer lock.Release()
		installed, err := state.LoadForApp(manager.Layout, appID)
		if os.IsNotExist(err) {
			return ErrNotInstalled
		}
		if err != nil {
			return err
		}
		if err := installed.ValidateForLayout(manager.Layout); err != nil {
			return err
		}
		spec, err := manager.integrationSpec(installed, "", nil)
		if err != nil {
			return err
		}
		return integration.RemoveConflict(spec, path)
	})
}

func (manager *Manager) UninstallLocked(ctx context.Context, appID string, progress SubjectProgress) ([]string, error) {
	return manager.uninstallUnlocked(ctx, appID, progress)
}

// UninstallAll removes every installed application after validating the full
// managed tree. The lifecycle lock is held across enumeration, preflight,
// per-application removal, and root cleanup so bulk removal has the same
// serialization and ownership policy as individual removal. Applications with
// corrupt state files are removed through the degraded path. Integration
// conflicts still abort all removal before any mutation.
func (manager *Manager) UninstallAll(ctx context.Context, progress func(string) Progress) ([]string, error) {
	var warnings []string
	err := manager.WithLifecycle(ctx, func() error {
		states, degraded, stateWarnings, err := manager.uninstallStates()
		warnings = append(warnings, stateWarnings...)
		if err != nil {
			return err
		}
		rootWarnings, err := manager.validateUninstallRoots(states)
		warnings = append(warnings, rootWarnings...)
		if err != nil {
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
			var appProgress Progress
			if progress != nil {
				appProgress = progress(installed.App)
			}
			appWarnings, err := manager.UninstallLocked(ctx, installed.App, func(stage, _ string, current, total int64) { manager.report(appProgress, stage, current, total) })
			warnings = append(warnings, appWarnings...)
			if err != nil {
				uninstallErrs = append(uninstallErrs, err)
			}
			if ctx != nil && ctx.Err() != nil {
				break
			}
		}
		for _, appID := range degraded {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					uninstallErrs = append(uninstallErrs, err)
					break
				}
			}
			var appProgress Progress
			if progress != nil {
				appProgress = progress(appID)
			}
			appWarnings, err := manager.UninstallDegradedLocked(ctx, appID, func(stage, _ string, current, total int64) { manager.report(appProgress, stage, current, total) })
			warnings = append(warnings, appWarnings...)
			if err != nil {
				uninstallErrs = append(uninstallErrs, err)
			}
			if ctx != nil && ctx.Err() != nil {
				break
			}
		}
		if err := errors.Join(uninstallErrs...); err != nil {
			return err
		}
		return manager.removeUninstallRoots()
	})
	return warnings, err
}

// uninstallStates enumerates the state directory. Corrupt state files and
// entries that cannot be attributed to an installed application become degraded
// or warning entries so bulk removal can blast through; every other unexpected
// file system state is a hard error.
func (manager *Manager) uninstallStates() ([]state.State, []string, []string, error) {
	var warnings []string
	if err := manager.checkUninstallAnchor(manager.Layout.StateHome, manager.Layout.States); err != nil {
		return nil, nil, nil, err
	}
	entries, err := os.ReadDir(manager.Layout.States)
	if errors.Is(err, os.ErrNotExist) {
		return []state.State{}, nil, warnings, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	result := make([]state.State, 0, len(entries))
	var degraded []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			warnings = append(warnings, fmt.Sprintf("unexpected state entry %s left for root cleanup", entry.Name()))
			continue
		}
		if strings.HasPrefix(entry.Name(), ".state-") {
			if !strings.HasSuffix(entry.Name(), ".tmp") || !entry.Type().IsRegular() {
				warnings = append(warnings, fmt.Sprintf("unexpected state entry %s left for root cleanup", entry.Name()))
			}
			continue
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			warnings = append(warnings, fmt.Sprintf("unexpected state entry %s left for root cleanup", entry.Name()))
			continue
		}
		appID := strings.TrimSuffix(entry.Name(), ".json")
		if err := filesystem.ValidateID(appID); err != nil {
			warnings = append(warnings, fmt.Sprintf("unexpected state entry %s left for root cleanup", entry.Name()))
			continue
		}
		installed, err := state.LoadForApp(manager.Layout, appID)
		if os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("state entry %s disappeared; left for root cleanup", entry.Name()))
			continue
		}
		if err != nil {
			if errors.Is(err, state.ErrCorrupt) {
				degraded = append(degraded, appID)
				continue
			}
			return nil, nil, nil, err
		}
		if installed.App != appID || installed.ValidateForLayout(manager.Layout) != nil {
			degraded = append(degraded, appID)
			continue
		}
		result = append(result, installed)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].App < result[j].App })
	sort.Strings(degraded)
	return result, degraded, warnings, nil
}

func (manager *Manager) validateUninstallRoots(states []state.State) ([]string, error) {
	var warnings []string
	if err := manager.checkUninstallAnchor(manager.Layout.DataHome, manager.Layout.Apps); err != nil {
		return nil, err
	}
	if err := manager.checkUninstallAnchor(manager.Layout.CacheHome, manager.Layout.Cache); err != nil {
		return nil, err
	}
	if err := manager.checkUninstallAnchor(manager.Layout.StateHome, manager.Layout.Locks); err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(states))
	for _, installed := range states {
		known[installed.App] = struct{}{}
		spec, err := manager.integrationSpec(installed, "", nil)
		if err != nil {
			return nil, fmt.Errorf("%s integration: %w", installed.App, err)
		}
		if err := integration.ValidateOwnedForRemoval(spec); err != nil {
			return nil, fmt.Errorf("%s integration: %w", installed.App, err)
		}
	}
	if _, err := os.Lstat(manager.Layout.Apps); errors.Is(err, os.ErrNotExist) {
		return warnings, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(manager.Layout.Apps)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			warnings = append(warnings, fmt.Sprintf("unexpected application symlink %q removed with the apps root", entry.Name()))
			continue
		}
		if strings.HasPrefix(entry.Name(), ".staging-") {
			if !entry.IsDir() {
				warnings = append(warnings, fmt.Sprintf("unexpected staging entry %q removed with the apps root", entry.Name()))
			}
			continue
		}
		if !entry.IsDir() {
			warnings = append(warnings, fmt.Sprintf("unexpected application entry %q removed with the apps root", entry.Name()))
			continue
		}
		if _, ok := known[entry.Name()]; !ok {
			warnings = append(warnings, fmt.Sprintf("untracked application directory %q removed with the apps root", entry.Name()))
		}
	}
	return warnings, nil
}

func (manager *Manager) checkUninstallAnchor(anchor, path string) error {
	if !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return filesystem.ErrOutsideRoot
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, anchor); err != nil {
		return err
	}
	return filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, path)
}

func (manager *Manager) removeUninstallRoots() error {
	for _, root := range []struct{ anchor, path string }{
		{manager.Layout.DataHome, manager.Layout.Apps}, {manager.Layout.StateHome, manager.Layout.States},
		{manager.Layout.StateHome, manager.Layout.Locks}, {manager.Layout.CacheHome, manager.Layout.Cache},
	} {
		if err := filesystem.SafeRemoveIfExists(root.anchor, root.path); err != nil {
			return err
		}
	}
	for _, parent := range []struct{ anchor, path string }{
		{manager.Layout.DataHome, filepath.Dir(manager.Layout.Apps)},
		{manager.Layout.StateHome, filepath.Dir(manager.Layout.States)},
	} {
		if err := manager.removeEmptyUninstallParent(parent.anchor, parent.path); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) removeEmptyUninstallParent(anchor, path string) error {
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

// SetPinned updates only the pin bit under the same lifecycle and per-app
// locks used by install/update, after validating the complete managed state.
func (manager *Manager) SetPinned(ctx context.Context, appID string, pinned bool) error {
	return manager.WithLifecycle(ctx, func() error {
		lock, err := manager.lock(ctx, appID)
		if err != nil {
			return err
		}
		defer lock.Release()
		installed, err := state.LoadForApp(manager.Layout, appID)
		if err != nil {
			return err
		}
		if err := installed.ValidateForLayout(manager.Layout); err != nil {
			return err
		}
		if err := manager.validateManagedApp(appID); err != nil {
			return err
		}
		installed.Pinned = pinned
		_, err = manager.persistState(installed)
		return err
	})
}

func (manager *Manager) WithLifecycle(ctx context.Context, operation func() error) error {
	if operation == nil {
		return errors.New("lifecycle operation is nil")
	}
	lock, err := locking.AcquireDirectoryWithTimeout(ctx, manager.Layout.Home, manager.LockTimeout)
	if err != nil {
		return err
	}
	operationErr := operation()
	if releaseErr := lock.Release(); releaseErr != nil {
		return errors.Join(operationErr, releaseErr)
	}
	return operationErr
}

func removeStateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("state path is not a regular file")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (manager *Manager) installVersion(ctx context.Context, item *manifest.Manifest, installed *state.State, options Options, progress SubjectProgress) (outcome Outcome, returnErr error) {
	materialized, err := manager.materializeArtifact(ctx, item, progress)
	if err != nil {
		return Outcome{}, err
	}
	defer func() {
		if cleanupErr := filesystem.SafeRemove(manager.Layout.Apps, materialized.stage); cleanupErr != nil && returnErr == nil {
			returnErr = cleanupErr
		}
	}()
	return manager.activateMaterialized(item, installed, options, progress, materialized)
}

// materializeArtifact acquires, verifies, extracts, and validates an artifact
// into a private staging directory. It never publishes application files or
// changes integration, activation, or state.
func (manager *Manager) materializeArtifact(ctx context.Context, item *manifest.Manifest, progress SubjectProgress) (materializedArtifact, error) {
	manager.report(progress, "downloading", 0, 0)
	artifacts := filepath.Join(manager.Layout.Cache, "artifacts")
	if err := filesystem.SecureMkdirAll(artifacts, 0o700); err != nil {
		return materializedArtifact{}, err
	}
	verification := item.Release.Verification
	artifactPath := filepath.Join(artifacts, verification.Algorithm+"-"+verification.Digest+"."+strings.ReplaceAll(item.Release.Archive, ".", "-"))
	_, err := manager.Client.FetchArtifact(ctx, download.ArtifactRequest{
		URL: item.Release.URL, Algorithm: verification.Algorithm, Digest: verification.Digest,
		Destination:    artifactPath,
		ReportProgress: func(current, total int64) { manager.report(progress, "downloading", current, total) },
	})
	if err != nil {
		return materializedArtifact{}, err
	}
	manager.report(progress, "verifying", 0, 0)
	if err := manager.inject("after_download"); err != nil {
		return materializedArtifact{}, err
	}

	stage, err := os.MkdirTemp(manager.Layout.Apps, ".staging-"+item.ID+"-*")
	if err != nil {
		return materializedArtifact{}, err
	}
	materialized := materializedArtifact{stage: stage}
	cleanup := true
	defer func() {
		if cleanup {
			_ = filesystem.SafeRemove(manager.Layout.Apps, stage)
		}
	}()
	var applicationRoot string
	if item.Release.Archive == "appimage" {
		manager.report(progress, "validating-appimage", 0, 0)
		if err := appimage.ValidatePath(artifactPath, item.Platform.Arch); err != nil {
			return materializedArtifact{}, err
		}
		applicationRoot = stage
		file, openErr := os.Open(artifactPath)
		if openErr != nil {
			return materializedArtifact{}, openErr
		}
		destination := filepath.Join(stage, "appimage")
		out, createErr := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if createErr == nil {
			_, createErr = io.Copy(out, file)
			closeErr := out.Close()
			if createErr == nil {
				createErr = closeErr
			}
		}
		fileCloseErr := file.Close()
		if createErr == nil {
			createErr = fileCloseErr
		}
		if createErr != nil {
			return materializedArtifact{}, fmt.Errorf("stage AppImage: %w", createErr)
		}
		if err := os.Chmod(destination, 0o755); err != nil {
			return materializedArtifact{}, fmt.Errorf("set AppImage permission: %w", err)
		}
	} else {
		outer := filepath.Join(stage, "outer")
		if err := os.Mkdir(outer, 0o700); err != nil {
			return materializedArtifact{}, err
		}
		extracted := outer
		manager.report(progress, "extracting", 0, 0)
		if !item.Release.NestedArchive.IsZero() {
			final := filepath.Join(stage, "final")
			if err := os.Mkdir(final, 0o700); err != nil {
				return materializedArtifact{}, err
			}
			if err := archive.ExtractNestedPath(ctx, artifactPath, outer, final, archive.Format(item.Release.Archive), item.Release.NestedArchive.Path, archive.Format(item.Release.NestedArchive.Archive), manager.Limits, func(stage string, current, total int64) {
				progressStage := "extracting"
				if stage == archive.ProgressPreparing {
					progressStage = "extracting-preparing"
				}
				manager.report(progress, progressStage, current, total)
			}); err != nil {
				return materializedArtifact{}, err
			}
			extracted = final
		} else if err := archive.ExtractPathWithProgress(ctx, artifactPath, outer, archive.Format(item.Release.Archive), manager.Limits, func(stage string, current, total int64) {
			progressStage := "extracting"
			if stage == archive.ProgressPreparing {
				progressStage = "extracting-preparing"
			}
			manager.report(progress, progressStage, current, total)
		}); err != nil {
			return materializedArtifact{}, err
		}
		applicationRoot, err = normalizedApplicationRoot(extracted)
		if err != nil {
			return materializedArtifact{}, err
		}
	}
	for _, executable := range item.Application.Executables {
		if err := validateExecutable(applicationRoot, executable.Path); err != nil {
			return materializedArtifact{}, err
		}
	}
	if !item.Desktop.Icon.IsZero() {
		iconSource, iconSize, err := manager.materializeIcon(ctx, item, applicationRoot, progress)
		if err != nil {
			return materializedArtifact{}, err
		}
		materialized.iconSource = iconSource
		materialized.iconSize = iconSize
		if _, err := integration.IconDigest(applicationRoot, iconSource); err != nil {
			return materializedArtifact{}, fmt.Errorf("validate desktop icon: %w", err)
		}
	}
	if err := manager.inject("after_extract"); err != nil {
		return materializedArtifact{}, err
	}
	materialized.applicationRoot = applicationRoot
	cleanup = false
	return materialized, nil
}

// materializeIcon returns the relative icon source path inside the staged
// application root. Archive-contained icons are returned unchanged; remote
// PNG icons are downloaded through the same bounded artifact client, retained
// at the reserved path inside the version payload, and their actual PNG
// dimensions (validated from the signature and IHDR header) determine the
// hicolor raster size.
func (manager *Manager) materializeIcon(ctx context.Context, item *manifest.Manifest, applicationRoot string, progress SubjectProgress) (string, int, error) {
	if !item.Desktop.Icon.Remote() {
		return item.Desktop.Icon.Path, 0, nil
	}
	destination := filepath.Join(applicationRoot, remoteIconFile)
	if _, err := os.Lstat(destination); err == nil {
		return "", 0, fmt.Errorf("%w: reserved icon path %q is occupied", ErrConflict, remoteIconFile)
	} else if !os.IsNotExist(err) {
		return "", 0, err
	}
	manager.report(progress, "downloading", 0, 0, "remote-desktop-icon")
	_, err := manager.Client.FetchArtifact(ctx, download.ArtifactRequest{
		URL: item.Desktop.Icon.URL, Algorithm: "sha256", Digest: item.Desktop.Icon.SHA256,
		Destination: destination, MaxBytes: maxRemoteIconBytes,
		ReportProgress: func(current, total int64) {
			manager.report(progress, "downloading", current, total, "remote-desktop-icon")
		},
	})
	if err != nil {
		return "", 0, fmt.Errorf("download desktop icon: %w", err)
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxRemoteIconBytes {
		return "", 0, fmt.Errorf("%w: retained icon is not a bounded regular file", ErrConflict)
	}
	file, err := os.Open(destination)
	if err != nil {
		return "", 0, err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxRemoteIconBytes+1))
	closeErr := file.Close()
	if err != nil {
		return "", 0, err
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	if int64(len(content)) > maxRemoteIconBytes {
		return "", 0, fmt.Errorf("%w: retained icon exceeds size limit", ErrConflict)
	}
	size, err := manifest.IconSizeFromPNG(content)
	if err != nil {
		return "", 0, fmt.Errorf("desktop icon: %w", err)
	}
	return remoteIconFile, size, nil
}

// activateMaterialized publishes a validated staged application and commits
// its integration, active version, state, and retention policy atomically.
func (manager *Manager) activateMaterialized(item *manifest.Manifest, installed *state.State, options Options, progress SubjectProgress, materialized materializedArtifact) (outcome Outcome, returnErr error) {
	itemFingerprint, err := item.ResolvedPackageFingerprint()
	if err != nil {
		return Outcome{}, fmt.Errorf("fingerprint manifest: %w", err)
	}
	applicationRoot := materialized.applicationRoot
	iconSource := materialized.iconSource
	iconSize := materialized.iconSize

	appRoot := filepath.Join(manager.Layout.Apps, item.ID)
	appRootCreated := false
	if _, err := os.Lstat(appRoot); os.IsNotExist(err) {
		if err := os.Mkdir(appRoot, 0o700); err != nil {
			return Outcome{}, err
		}
		appRootCreated = true
	} else if err != nil {
		return Outcome{}, err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, appRoot); err != nil {
		return Outcome{}, err
	}
	keepAppRoot := false
	defer func() {
		if appRootCreated && !keepAppRoot {
			_ = os.Remove(appRoot)
		}
	}()
	finalPath, err := manager.Layout.PackagePath(item.ID, item.Release.Version, itemFingerprint)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := os.Lstat(finalPath); err == nil {
		return Outcome{}, fmt.Errorf("%w: package directory already exists", ErrConflict)
	} else if !os.IsNotExist(err) {
		return Outcome{}, err
	}
	if parent := filepath.Dir(finalPath); parent != appRoot {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return Outcome{}, err
		}
	}
	manager.report(progress, "installing", 0, 0)
	if err := os.Rename(applicationRoot, finalPath); err != nil {
		return Outcome{}, fmt.Errorf("publish version: %w", err)
	}
	if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, finalPath); err != nil {
		return Outcome{}, err
	}
	keepFinal := false
	defer func() {
		if keepFinal {
			return
		}
		_ = filesystem.SafeRemove(appRoot, finalPath)
		if filepath.Dir(finalPath) != appRoot {
			_ = os.Remove(filepath.Dir(finalPath))
		}
		if appRootCreated {
			_ = os.Remove(appRoot)
		}
	}()
	if err := syncDirectory(appRoot); err != nil {
		return Outcome{}, err
	}

	manager.report(progress, "integrating", 0, 0)
	spec := integration.Spec{
		ID: item.ID, Name: item.Name,
		ApplicationRoot: appRoot, LocalBinDirectory: manager.Layout.Bin,
		DesktopDirectory: manager.Layout.Desktop, IconDirectory: manager.Layout.Icons,
		Icon: iconSource, IconSize: iconSize, IconSourceRoot: finalPath, DesktopEnabled: item.Desktop.Enabled,
		DesktopCategories: item.Desktop.Categories, WorkingDirectory: item.Desktop.WorkingDirectory == "application-root",
	}
	for _, executable := range item.Application.Executables {
		spec.Executables = append(spec.Executables, integration.ExecutableSpec{Name: executable.Name, Path: executable.Path, CreateBinLink: executable.CreateBinLink})
	}
	spec.DesktopExecutable = filepath.Join(manager.Layout.Apps, item.ID, "current", desktopExecutablePath(item))
	if spec.DesktopEnabled {
		spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).Executables[0].Link)
	}
	if spec.Icon != "" {
		spec.IconSHA256, err = integration.IconDigest(spec.IconSourceRoot, spec.Icon)
		if err != nil {
			return Outcome{}, err
		}
	}
	var paths integration.Paths
	cleanupIntegration := func() error { return nil }
	if installed == nil {
		paths, cleanupIntegration, err = integration.Ensure(spec)
		if err != nil {
			return Outcome{}, err
		}
	} else {
		existingSpec, specErr := manager.integrationSpec(*installed, "", nil)
		if specErr != nil {
			return Outcome{}, specErr
		}
		if err := integration.ValidateOwned(existingSpec); err != nil {
			return Outcome{}, err
		}
		if spec.DesktopEnabled {
			spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).Executables[0].Link)
		} else {
			spec.DesktopSHA256 = ""
		}
		paths, cleanupIntegration, err = integration.Update(spec, existingSpec)
		if err != nil {
			return Outcome{}, err
		}
		spec.DesktopEnabled = installed.DesktopEnabled
	}
	keepIntegration := false
	defer func() {
		if !keepIntegration {
			_ = cleanupIntegration()
		}
	}()
	if err := manager.inject("after_integration"); err != nil {
		return Outcome{}, err
	}

	oldVersion := ""
	previousVersion := ""
	if installed != nil {
		oldVersion = installed.Current
		previousVersion = installed.Previous
	}
	manager.report(progress, "activating", 0, 0)
	oldTarget := ""
	if installed != nil {
		oldTarget, err = manager.currentTarget(item.ID, installed.Current, installed.CurrentFingerprint)
		if err != nil {
			return Outcome{}, err
		}
	}
	newTarget, err := manager.currentTarget(item.ID, item.Release.Version, itemFingerprint)
	if err != nil {
		return Outcome{}, err
	}
	restore, err := switchCurrent(appRoot, oldTarget, newTarget)
	if err != nil {
		return Outcome{}, err
	}
	keepActivation := false
	defer func() {
		if keepActivation {
			return
		}
		_ = restore()
	}()
	if err := manager.inject("after_activation"); err != nil {
		return Outcome{}, err
	}

	desktopEnabled := item.Desktop.Enabled
	desktopPath := ""
	if desktopEnabled {
		desktopPath = paths.DesktopEntry
	}
	channel := options.Channel
	if channel == "" && installed != nil {
		channel = installed.Channel
	}
	pinned := false
	if installed != nil {
		pinned = installed.Pinned
	}
	if options.Pinned != nil {
		pinned = *options.Pinned
	}
	newState := state.State{
		Schema: state.Schema, App: item.ID, Current: item.Release.Version, CurrentFingerprint: itemFingerprint, Previous: oldVersion,
		PreviousFingerprint: func() string {
			if installed != nil {
				return installed.CurrentFingerprint
			}
			return ""
		}(),
		PreviousArtifact: func() string {
			if installed != nil {
				return installed.Artifact
			}
			return ""
		}(),
		Channel: channel, PreviousChannel: func() string {
			if installed != nil {
				return installed.Channel
			}
			return ""
		}(), Pinned: pinned,
		Executables: manifestExecutables(item.Application.Executables), Artifact: item.Release.Archive, DesktopEnabled: desktopEnabled,
		Integration: state.Integration{
			DesktopEntry:      desktopPath,
			DesktopSHA256:     spec.DesktopSHA256,
			DesktopExecutable: desktopExecutableName(item),
			WorkingDirectory:  spec.WorkingDirectory,
			IconFile:          paths.IconFile, IconSHA256: spec.IconSHA256,
			IconSize: spec.IconSize, IconSource: iconSource,
		},
	}
	for _, executable := range paths.Executables {
		value := findManifestExecutable(item.Application.Executables, executable.Name)
		newState.Integration.Executables = append(newState.Integration.Executables, state.ExecutableIntegration{Name: executable.Name, Path: executablePath(item.Application.Executables, executable.Name), Link: executable.Link, Target: executable.Target, CreateBinLink: value.CreateBinLink})
	}
	if installed != nil && desktopEnabled {
		newState.Integration.PreviousIconFile = installed.Integration.IconFile
		newState.Integration.PreviousIconSHA256 = installed.Integration.IconSHA256
		newState.Integration.PreviousIconSize = installed.Integration.IconSize
		newState.Integration.PreviousIconSource = installed.Integration.IconSource
	}
	if err := manager.inject("before_state"); err != nil {
		return Outcome{}, err
	}
	stateCommitted, stateErr := manager.persistState(newState)
	if stateCommitted {
		keepActivation = true
		keepFinal = true
		keepAppRoot = true
		keepIntegration = true
		outcome.State = newState
	}
	if stateErr != nil {
		return outcome, stateErr
	}

	if installed != nil && previousVersion != "" &&
		(previousVersion != newState.Current || installed.PreviousFingerprint != newState.CurrentFingerprint) &&
		(previousVersion != newState.Previous || installed.PreviousFingerprint != newState.PreviousFingerprint) {
		manager.report(progress, "cleaning", 0, 0)
		oldPath, pathErr := manager.Layout.PackagePath(item.ID, previousVersion, installed.PreviousFingerprint)
		if pathErr != nil {
			outcome.Warnings = append(outcome.Warnings, pathErr.Error())
		} else if cleanupErr := filesystem.SafeRemove(appRoot, oldPath); cleanupErr != nil {
			outcome.Warnings = append(outcome.Warnings, cleanupErr.Error())
		}
	}
	return outcome, nil
}

func (manager *Manager) activateRetained(appID string, installed state.State, progress SubjectProgress) (Outcome, error) {
	appRoot := filepath.Join(manager.Layout.Apps, appID)
	if err := manager.validateManagedApp(appID); err != nil {
		return Outcome{}, err
	}
	for _, packageRef := range []struct{ version, fingerprint string }{{installed.Current, installed.CurrentFingerprint}, {installed.Previous, installed.PreviousFingerprint}} {
		if packageRef.version == "" {
			continue
		}
		if err := manager.validateManagedPackage(appID, packageRef.version, packageRef.fingerprint); err != nil {
			return Outcome{}, err
		}
	}
	retained, err := manager.Layout.PackagePath(appID, installed.Previous, installed.PreviousFingerprint)
	if err != nil {
		return Outcome{}, err
	}
	for _, executable := range installed.Executables {
		if err := validateExecutable(retained, executable.Path); err != nil {
			return Outcome{}, err
		}
	}
	spec, err := manager.integrationSpec(installed, "", nil)
	if err != nil {
		return Outcome{}, err
	}
	if err := integration.ValidateOwned(spec); err != nil {
		return Outcome{}, err
	}
	manager.report(progress, "activating", 0, 0)
	currentTarget, err := manager.currentTarget(appID, installed.Current, installed.CurrentFingerprint)
	if err != nil {
		return Outcome{}, err
	}
	previousTarget, err := manager.currentTarget(appID, installed.Previous, installed.PreviousFingerprint)
	if err != nil {
		return Outcome{}, err
	}
	restore, err := switchCurrent(appRoot, currentTarget, previousTarget)
	if err != nil {
		return Outcome{}, err
	}
	nextSpec := spec
	nextSpec.Icon = installed.Integration.PreviousIconSource
	if nextSpec.Icon == "" && installed.Integration.PreviousIconFile != "" {
		nextSpec.Icon = "icon" + filepath.Ext(installed.Integration.PreviousIconFile)
	}
	nextSpec.IconSHA256 = installed.Integration.PreviousIconSHA256
	nextSpec.IconSize = installed.Integration.PreviousIconSize
	// The previous version's payload is a real directory; the retained icon
	// bytes live inside it so rollback needs no network. The switched current
	// link itself is a symlink and is never used as an icon source root.
	nextSpec.IconSourceRoot, err = manager.Layout.PackagePath(appID, installed.Previous, installed.PreviousFingerprint)
	if err != nil {
		_ = restore()
		return Outcome{}, err
	}
	iconRestore, err := integration.SwitchIcon(spec, nextSpec)
	if err != nil {
		_ = restore()
		return Outcome{}, err
	}
	iconCommitted := false
	committed := false
	defer func() {
		if !committed {
			if !iconCommitted {
				_ = iconRestore()
			}
			_ = restore()
		}
	}()
	installed.Current, installed.Previous = installed.Previous, installed.Current
	installed.CurrentFingerprint, installed.PreviousFingerprint = installed.PreviousFingerprint, installed.CurrentFingerprint
	installed.Artifact, installed.PreviousArtifact = installed.PreviousArtifact, installed.Artifact
	installed.Channel, installed.PreviousChannel = installed.PreviousChannel, installed.Channel
	installed.Integration.IconFile, installed.Integration.PreviousIconFile = installed.Integration.PreviousIconFile, installed.Integration.IconFile
	installed.Integration.IconSHA256, installed.Integration.PreviousIconSHA256 = installed.Integration.PreviousIconSHA256, installed.Integration.IconSHA256
	installed.Integration.IconSize, installed.Integration.PreviousIconSize = installed.Integration.PreviousIconSize, installed.Integration.IconSize
	installed.Integration.IconSource, installed.Integration.PreviousIconSource = installed.Integration.PreviousIconSource, installed.Integration.IconSource
	if err := manager.inject("before_state"); err != nil {
		return Outcome{}, err
	}
	stateCommitted, stateErr := manager.persistState(installed)
	if stateCommitted {
		committed = true
		iconCommitted = true
	}
	if stateErr != nil {
		return Outcome{State: installed}, stateErr
	}
	return Outcome{State: installed}, nil
}

func (manager *Manager) validateManagedApp(appID string) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return err
	}
	if err := manager.validateManagedRoots(); err != nil {
		return err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, filepath.Join(manager.Layout.Apps, appID)); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) validateManagedPackage(appID, version, fingerprint string) error {
	path, err := manager.Layout.PackagePath(appID, version, fingerprint)
	if err != nil {
		return err
	}
	return filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, path)
}

func (manager *Manager) validateManagedRoots() error {
	for _, path := range []string{manager.Layout.States, manager.Layout.Apps} {
		if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, path); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) persistState(installed state.State) (bool, error) {
	if manager.writeState != nil {
		return manager.writeState(manager.Layout, installed)
	}
	return state.WriteForAppWithCommit(manager.Layout, installed)
}

func (manager *Manager) integrationSpec(installed state.State, name string, categories []string) (integration.Spec, error) {
	appRoot := filepath.Join(manager.Layout.Apps, installed.App)
	spec := integration.Spec{
		ID: installed.App, Name: name, ApplicationRoot: appRoot,
		LocalBinDirectory: manager.Layout.Bin, DesktopDirectory: manager.Layout.Desktop, IconDirectory: manager.Layout.Icons,
		DesktopEnabled: installed.DesktopEnabled, DesktopCategories: categories, WorkingDirectory: installed.Integration.WorkingDirectory,
		DesktopSHA256:  installed.Integration.DesktopSHA256,
		IconSHA256:     installed.Integration.IconSHA256,
		IconSize:       installed.Integration.IconSize,
		IconSourceRoot: filepath.Join(appRoot, "current"), Icon: installed.Integration.IconSource,
	}
	for _, executable := range installed.Executables {
		spec.Executables = append(spec.Executables, integration.ExecutableSpec{Name: executable.Name, Path: executable.Path, CreateBinLink: executable.CreateBinLink})
	}
	expected := integration.ExpectedPaths(spec)
	if installed.Integration.DesktopExecutable != "" {
		spec.DesktopExecutable = filepath.Join(appRoot, "current", executablePathState(installed.Executables, installed.Integration.DesktopExecutable))
	} else if len(expected.Executables) > 0 {
		// State written before direct desktop targets used the bin link. Keep
		// that historical ownership digest valid long enough for the update
		// transaction to replace it with the new direct-target entry.
		spec.DesktopExecutable = expected.Executables[0].Link
	}
	if spec.Icon == "" && installed.Integration.IconFile != "" {
		spec.Icon = "icon" + filepath.Ext(installed.Integration.IconFile)
	}
	expected = integration.ExpectedPaths(spec)
	expectedDesktop := ""
	if installed.DesktopEnabled {
		expectedDesktop = expected.DesktopEntry
	}
	if installed.Integration.DesktopEntry != expectedDesktop || installed.Integration.IconFile != expected.IconFile || len(installed.Integration.Executables) != len(expected.Executables) {
		return integration.Spec{}, fmt.Errorf("%w: state integration paths do not match the canonical layout", ErrConflict)
	}
	for index, executable := range expected.Executables {
		recorded := installed.Integration.Executables[index]
		if recorded.Link != executable.Link || recorded.Target != executable.Target {
			return integration.Spec{}, fmt.Errorf("%w: executable integration paths do not match", ErrConflict)
		}
	}
	return spec, nil
}

func (manager *Manager) currentTarget(appID, version, fingerprint string) (string, error) {
	packagePath, err := manager.Layout.PackagePath(appID, version, fingerprint)
	if err != nil {
		return "", err
	}
	return filepath.Rel(filepath.Join(manager.Layout.Apps, appID), packagePath)
}

func manifestExecutables(values []manifest.Executable) []state.Executable {
	result := make([]state.Executable, 0, len(values))
	for _, value := range values {
		result = append(result, state.Executable{Name: value.Name, Path: value.Path, CreateBinLink: value.CreateBinLink})
	}
	return result
}

func findManifestExecutable(values []manifest.Executable, name string) manifest.Executable {
	for _, value := range values {
		if value.Name == name {
			return value
		}
	}
	return manifest.Executable{}
}

func desktopExecutableName(item *manifest.Manifest) string {
	if item.Desktop.Executable != "" {
		return item.Desktop.Executable
	}
	return item.Application.Executables[0].Name
}

func desktopExecutablePath(item *manifest.Manifest) string {
	return executablePath(item.Application.Executables, desktopExecutableName(item))
}

func executablePathState(values []state.Executable, name string) string {
	for _, value := range values {
		if value.Name == name {
			return value.Path
		}
	}
	return ""
}
func executablePath(values []manifest.Executable, name string) string {
	for _, value := range values {
		if value.Name == name {
			return value.Path
		}
	}
	return ""
}
func (manager *Manager) lock(ctx context.Context, appID string) (*locking.Lock, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, err
	}
	return locking.AcquireAppWithTimeout(ctx, manager.Layout.Locks, appID, manager.LockTimeout)
}

func (manager *Manager) report(progress any, stage string, current, total int64, subject ...string) {
	resource := "package-artifact"
	if len(subject) != 0 {
		resource = subject[0]
	}
	switch callback := progress.(type) {
	case SubjectProgress:
		if callback != nil {
			callback(stage, resource, current, total)
		}
	case Progress:
		if callback != nil {
			callback(stage, current, total)
		}
	}
}

func (manager *Manager) inject(stage string) error {
	if manager.fail != nil {
		return manager.fail(stage)
	}
	return nil
}

func normalizedApplicationRoot(extracted string) (string, error) {
	entries, err := os.ReadDir(extracted)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", errors.New("archive extracted no files")
	}
	if len(entries) == 1 && entries[0].IsDir() && entries[0].Type()&os.ModeSymlink == 0 {
		return filepath.Join(extracted, entries[0].Name()), nil
	}
	return extracted, nil
}

func validateExecutable(root, executable string) error {
	if err := manifest.ValidateRelativePath(executable); err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(executable))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("declared executable escapes extracted application")
	}
	current := root
	rootInfo, err := os.Lstat(current)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("application root is not a real directory")
	}
	parts := strings.Split(filepath.FromSlash(executable), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		parentInfo, parentErr := os.Lstat(current)
		if parentErr != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
			return errors.New("declared executable has an unsafe parent directory")
		}
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("declared executable does not exist: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("declared executable is not a regular file")
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("set declared executable permission: %w", err)
	}
	return nil
}

func switchCurrent(appRoot, expectedOld, next string) (func() error, error) {
	current := filepath.Join(appRoot, "current")
	if expectedOld == "" {
		if _, err := os.Lstat(current); err == nil {
			return nil, fmt.Errorf("%w: current already exists", ErrConflict)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("%w: current is not the expected symlink", ErrConflict)
		}
		target, err := os.Readlink(current)
		if err != nil || target != expectedOld {
			return nil, fmt.Errorf("%w: current points to an unexpected version", ErrConflict)
		}
	}
	nextInfo, err := os.Lstat(filepath.Join(appRoot, next))
	if err != nil {
		return nil, fmt.Errorf("activate version: %w", err)
	}
	if nextInfo.Mode()&os.ModeSymlink != 0 || !nextInfo.IsDir() {
		return nil, fmt.Errorf("%w: activated version is not a real directory", ErrConflict)
	}
	temporary, err := os.CreateTemp(appRoot, ".current-*")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return nil, err
	}
	if err := os.Symlink(next, temporaryPath); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, current); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, err
	}
	restore := func() error {
		if expectedOld == "" {
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink == 0 {
				return ErrConflict
			}
			target, err := os.Readlink(current)
			if err != nil || target != next {
				return ErrConflict
			}
			if err := os.Remove(current); err != nil {
				return err
			}
			return syncDirectory(appRoot)
		}
		_, err := switchCurrent(appRoot, next, expectedOld)
		return err
	}
	if err := syncDirectory(appRoot); err != nil {
		return nil, errors.Join(err, restore())
	}
	return restore, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
