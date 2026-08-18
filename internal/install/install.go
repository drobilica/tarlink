// Package install implements TarLink's per-application transactional lifecycle.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	ErrNoPrevious       = errors.New("no previous version is retained")
	ErrConflict         = errors.New("unexpected filesystem conflict")
)

type Progress func(stage string, current, total int64)

type Outcome struct {
	State    state.State
	Warnings []string
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
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.installUnlocked(ctx, item, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) installUnlocked(ctx context.Context, item *manifest.Manifest, progress Progress) (Outcome, error) {
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
	return manager.installVersion(ctx, item, nil, progress)
}

func (manager *Manager) Update(ctx context.Context, item *manifest.Manifest, progress Progress) (Outcome, error) {
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.updateUnlocked(ctx, item, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) updateUnlocked(ctx context.Context, item *manifest.Manifest, progress Progress) (Outcome, error) {
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
	if err := manager.validateManagedApp(item.ID); err != nil {
		return Outcome{}, err
	}
	if installed.Current == item.Release.Version {
		return Outcome{}, ErrNoUpdate
	}
	if installed.Executable != item.Application.Executable {
		return Outcome{}, fmt.Errorf("%w: executable path changed from %q to %q", ErrConflict, installed.Executable, item.Application.Executable)
	}
	if installed.DesktopEnabled != item.Desktop.Enabled {
		return Outcome{}, fmt.Errorf("%w: desktop integration setting cannot change during update", ErrConflict)
	}
	if installed.Previous == item.Release.Version {
		return manager.activateRetained(item.ID, installed, progress)
	}
	return manager.installVersion(ctx, item, &installed, progress)
}

func (manager *Manager) Rollback(ctx context.Context, appID string, progress Progress) (Outcome, error) {
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.rollbackUnlocked(ctx, appID, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) rollbackUnlocked(ctx context.Context, appID string, progress Progress) (Outcome, error) {
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

func (manager *Manager) Uninstall(ctx context.Context, appID string, progress Progress) error {
	return manager.WithLifecycle(ctx, func() error { return manager.uninstallUnlocked(ctx, appID, progress) })
}

func (manager *Manager) uninstallUnlocked(ctx context.Context, appID string, progress Progress) error {
	lock, err := manager.lock(ctx, appID)
	if err != nil {
		return err
	}
	defer lock.Release()
	for _, root := range []string{manager.Layout.States, manager.Layout.Apps} {
		if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, root); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
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
	if err := integration.ValidateOwnedForRemoval(spec); err != nil {
		return err
	}
	manager.report(progress, "cleaning", 0, 0)
	if err := manager.inject("before_uninstall"); err != nil {
		return err
	}
	if err := integration.RemoveOwned(spec); err != nil {
		return err
	}
	appRoot := filepath.Join(manager.Layout.Apps, appID)
	if err := filesystem.SafeRemoveIfExists(manager.Layout.Apps, appRoot); err != nil {
		return err
	}
	statePath, err := manager.Layout.StatePath(appID)
	if err != nil {
		return err
	}
	if err := removeStateFile(statePath); err != nil {
		return err
	}
	manager.report(progress, "complete", 0, 0)
	return nil
}

func (manager *Manager) UninstallLocked(ctx context.Context, appID string, progress Progress) error {
	return manager.uninstallUnlocked(ctx, appID, progress)
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

func (manager *Manager) installVersion(ctx context.Context, item *manifest.Manifest, installed *state.State, progress Progress) (outcome Outcome, returnErr error) {
	manager.report(progress, "downloading", 0, 0)
	artifacts := filepath.Join(manager.Layout.Cache, "artifacts")
	if err := filesystem.SecureMkdirAll(artifacts, 0o700); err != nil {
		return Outcome{}, err
	}
	verification := item.Release.Verification
	artifactPath := filepath.Join(artifacts, verification.Algorithm+"-"+verification.Digest+"."+strings.ReplaceAll(item.Release.Archive, ".", "-"))
	_, err := manager.Client.FetchArtifact(ctx, download.ArtifactRequest{
		URL: item.Release.URL, Algorithm: verification.Algorithm, Digest: verification.Digest,
		Destination:    artifactPath,
		ReportProgress: func(current, total int64) { manager.report(progress, "downloading", current, total) },
	})
	if err != nil {
		return Outcome{}, err
	}
	manager.report(progress, "verifying", 0, 0)
	if err := manager.inject("after_download"); err != nil {
		return Outcome{}, err
	}

	stage, err := os.MkdirTemp(manager.Layout.Apps, ".staging-"+item.ID+"-*")
	if err != nil {
		return Outcome{}, err
	}
	defer func() {
		if cleanupErr := filesystem.SafeRemove(manager.Layout.Apps, stage); cleanupErr != nil && returnErr == nil {
			returnErr = cleanupErr
		}
	}()
	extracted := filepath.Join(stage, "extracted")
	if err := os.Mkdir(extracted, 0o700); err != nil {
		return Outcome{}, err
	}
	manager.report(progress, "extracting", 0, 0)
	if err := archive.ExtractPath(ctx, artifactPath, extracted, archive.Format(item.Release.Archive), manager.Limits); err != nil {
		return Outcome{}, err
	}
	applicationRoot, err := normalizedApplicationRoot(extracted)
	if err != nil {
		return Outcome{}, err
	}
	if err := validateExecutable(applicationRoot, item.Application.Executable); err != nil {
		return Outcome{}, err
	}
	if item.Desktop.Icon != "" {
		if _, err := integration.IconDigest(applicationRoot, item.Desktop.Icon); err != nil {
			return Outcome{}, fmt.Errorf("validate desktop icon: %w", err)
		}
	}
	if err := manager.inject("after_extract"); err != nil {
		return Outcome{}, err
	}

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
	finalPath, err := manager.Layout.AppPath(item.ID, item.Release.Version)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := os.Lstat(finalPath); err == nil {
		return Outcome{}, fmt.Errorf("%w: version directory already exists", ErrConflict)
	} else if !os.IsNotExist(err) {
		return Outcome{}, err
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
		if appRootCreated {
			_ = os.Remove(appRoot)
		}
	}()
	if err := syncDirectory(appRoot); err != nil {
		return Outcome{}, err
	}

	manager.report(progress, "integrating", 0, 0)
	spec := integration.Spec{
		ID: item.ID, Name: item.Name, Executable: item.Application.Executable,
		ApplicationRoot: appRoot, LocalBinDirectory: manager.Layout.Bin,
		DesktopDirectory: manager.Layout.Desktop, IconDirectory: manager.Layout.Icons,
		Icon: item.Desktop.Icon, IconSourceRoot: finalPath, DesktopEnabled: item.Desktop.Enabled,
		DesktopCategories: item.Desktop.Categories,
	}
	if spec.DesktopEnabled {
		spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).ExecutableLink)
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
		spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).ExecutableLink)
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
	restore, err := switchCurrent(appRoot, oldVersion, item.Release.Version)
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
	if installed != nil {
		desktopEnabled = installed.DesktopEnabled
	}
	desktopPath := ""
	if desktopEnabled {
		desktopPath = paths.DesktopEntry
	}
	newState := state.State{
		Schema: state.Schema, App: item.ID, Current: item.Release.Version, Previous: oldVersion,
		Executable: item.Application.Executable, DesktopEnabled: desktopEnabled,
		Integration: state.Integration{
			ExecutableLink:   paths.ExecutableLink,
			ExecutableTarget: filepath.Join(appRoot, "current", filepath.FromSlash(item.Application.Executable)),
			DesktopEntry:     desktopPath,
			DesktopSHA256:    spec.DesktopSHA256,
			IconFile:         paths.IconFile, IconSHA256: spec.IconSHA256,
			IconSource: item.Desktop.Icon,
		},
	}
	if installed != nil {
		newState.Integration.PreviousIconFile = installed.Integration.IconFile
		newState.Integration.PreviousIconSHA256 = installed.Integration.IconSHA256
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

	if previousVersion != "" && previousVersion != newState.Current && previousVersion != newState.Previous {
		manager.report(progress, "cleaning", 0, 0)
		oldPath, pathErr := manager.Layout.AppPath(item.ID, previousVersion)
		if pathErr != nil {
			outcome.Warnings = append(outcome.Warnings, pathErr.Error())
		} else if cleanupErr := filesystem.SafeRemove(appRoot, oldPath); cleanupErr != nil {
			outcome.Warnings = append(outcome.Warnings, cleanupErr.Error())
		}
	}
	manager.report(progress, "complete", 0, 0)
	return outcome, nil
}

func (manager *Manager) activateRetained(appID string, installed state.State, progress Progress) (Outcome, error) {
	appRoot := filepath.Join(manager.Layout.Apps, appID)
	if err := manager.validateManagedApp(appID, installed.Current, installed.Previous); err != nil {
		return Outcome{}, err
	}
	retained, err := manager.Layout.AppPath(appID, installed.Previous)
	if err != nil {
		return Outcome{}, err
	}
	if err := validateExecutable(retained, installed.Executable); err != nil {
		return Outcome{}, err
	}
	spec, err := manager.integrationSpec(installed, "", nil)
	if err != nil {
		return Outcome{}, err
	}
	if err := integration.ValidateOwned(spec); err != nil {
		return Outcome{}, err
	}
	manager.report(progress, "activating", 0, 0)
	restore, err := switchCurrent(appRoot, installed.Current, installed.Previous)
	if err != nil {
		return Outcome{}, err
	}
	nextSpec := spec
	nextSpec.Icon = installed.Integration.PreviousIconSource
	if nextSpec.Icon == "" && installed.Integration.PreviousIconFile != "" {
		nextSpec.Icon = "icon" + filepath.Ext(installed.Integration.PreviousIconFile)
	}
	nextSpec.IconSHA256 = installed.Integration.PreviousIconSHA256
	nextSpec.IconSourceRoot = filepath.Join(appRoot, "current")
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
	installed.Integration.IconFile, installed.Integration.PreviousIconFile = installed.Integration.PreviousIconFile, installed.Integration.IconFile
	installed.Integration.IconSHA256, installed.Integration.PreviousIconSHA256 = installed.Integration.PreviousIconSHA256, installed.Integration.IconSHA256
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
	manager.report(progress, "complete", 0, 0)
	return Outcome{State: installed}, nil
}

func (manager *Manager) validateManagedApp(appID string, versions ...string) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return err
	}
	if err := manager.validateManagedRoots(); err != nil {
		return err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, filepath.Join(manager.Layout.Apps, appID)); err != nil {
		return err
	}
	for _, version := range versions {
		if version == "" {
			continue
		}
		path, err := manager.Layout.AppPath(appID, version)
		if err != nil {
			return err
		}
		if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, path); err != nil {
			return err
		}
	}
	return nil
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
		ID: installed.App, Name: name, Executable: installed.Executable, ApplicationRoot: appRoot,
		LocalBinDirectory: manager.Layout.Bin, DesktopDirectory: manager.Layout.Desktop, IconDirectory: manager.Layout.Icons,
		DesktopEnabled: installed.DesktopEnabled, DesktopCategories: categories,
		DesktopSHA256:  installed.Integration.DesktopSHA256,
		IconSHA256:     installed.Integration.IconSHA256,
		IconSourceRoot: filepath.Join(appRoot, "current"), Icon: installed.Integration.IconSource,
	}
	if spec.Icon == "" && installed.Integration.IconFile != "" {
		spec.Icon = "icon" + filepath.Ext(installed.Integration.IconFile)
	}
	expected := integration.ExpectedPaths(spec)
	expectedTarget := filepath.Join(appRoot, "current", filepath.FromSlash(installed.Executable))
	expectedDesktop := ""
	if installed.DesktopEnabled {
		expectedDesktop = expected.DesktopEntry
	}
	if installed.Integration.ExecutableLink != expected.ExecutableLink || installed.Integration.ExecutableTarget != expectedTarget || installed.Integration.DesktopEntry != expectedDesktop || installed.Integration.IconFile != expected.IconFile {
		return integration.Spec{}, fmt.Errorf("%w: state integration paths do not match the canonical layout", ErrConflict)
	}
	return spec, nil
}

func (manager *Manager) lock(ctx context.Context, appID string) (*locking.Lock, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, err
	}
	return locking.AcquireAppWithTimeout(ctx, manager.Layout.Locks, appID, manager.LockTimeout)
}

func (manager *Manager) report(progress Progress, stage string, current, total int64) {
	if progress != nil {
		progress(stage, current, total)
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
