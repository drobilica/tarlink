package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/manifest"
	taruntime "github.com/drobilica/tarlink/internal/runtime"
	"github.com/drobilica/tarlink/internal/state"
	"golang.org/x/sys/unix"
)

// RepairSubject restores an exact approved installed package. State is never
// rewritten: pinning, channel, runtime closure and rollback history stay intact.
func (manager *Manager) RepairSubject(ctx context.Context, item *manifest.Manifest, progress SubjectProgress) (Outcome, error) {
	var outcome Outcome
	err := manager.WithLifecycle(ctx, func() error {
		var err error
		outcome, err = manager.repairUnlocked(ctx, item, progress)
		return err
	})
	return outcome, err
}

func (manager *Manager) repairUnlocked(ctx context.Context, item *manifest.Manifest, progress SubjectProgress) (outcome Outcome, returnErr error) {
	if item == nil {
		return outcome, errors.New("manifest is nil")
	}
	fingerprint, err := item.ResolvedPackageFingerprint()
	if err != nil {
		return outcome, err
	}
	lock, err := manager.lock(ctx, item.ID)
	if err != nil {
		return outcome, err
	}
	defer lock.Release()
	if err := manager.validateManagedRoots(); err != nil {
		return outcome, err
	}
	installed, err := state.LoadForApp(manager.Layout, item.ID)
	if os.IsNotExist(err) {
		return outcome, ErrNotInstalled
	}
	if err != nil {
		return outcome, err
	}
	if err := installed.ValidateForLayout(manager.Layout); err != nil {
		return outcome, err
	}
	if installed.Current != item.Release.Version || installed.CurrentFingerprint != fingerprint || installed.Channel != item.Release.Channel {
		return outcome, errors.New("repair requires the exact installed package fingerprint and channel")
	}
	if installed.Artifact != item.Release.Archive || installed.DesktopEnabled != item.Desktop.Enabled ||
		!repairExecutablesMatch(installed.Executables, item.Application.Executables) ||
		!repairRuntimeMatches(installed.Runtime, item.Runtime) ||
		installed.Integration.DesktopExecutable != desktopExecutableName(item) ||
		installed.Integration.WorkingDirectory != (item.Desktop.WorkingDirectory == "application-root") {
		return outcome, errors.New("repair requires installed metadata matching the approved package")
	}
	finalPath, err := manager.Layout.PackagePath(item.ID, installed.Current, fingerprint)
	if err != nil {
		return outcome, err
	}
	// Missing owned directories can be recreated, but symlinks and other types
	// at any component are conflicts, including at the payload root itself.
	if err := filesystem.CheckOwnedDirectoryWithin(manager.Layout.Home, finalPath); err != nil && !os.IsNotExist(err) {
		return outcome, err
	}
	appRoot := filepath.Join(manager.Layout.Apps, item.ID)
	current := filepath.Join(appRoot, "current")
	target, err := manager.currentTarget(item.ID, installed.Current, fingerprint)
	if err != nil {
		return outcome, err
	}
	oldTarget := target
	if existing, err := os.Readlink(current); os.IsNotExist(err) {
		oldTarget = ""
	} else if err != nil || existing != target {
		return outcome, fmt.Errorf("%w: current is not the recorded symlink", ErrConflict)
	}
	spec, err := manager.integrationSpec(installed, item.Name, item.Desktop.Categories)
	if err != nil {
		return outcome, err
	}
	// The removal validator admits absent entries, while still proving every
	// existing integration by its recorded target or content digest.
	if err := integration.ValidateOwnedForRemoval(spec); err != nil {
		return outcome, err
	}
	manager.ensureArtifactCache()
	if err := manager.PreflightOffline(ctx, []*manifest.Manifest{item}); err != nil {
		return outcome, err
	}
	if item.Runtime != nil {
		if _, _, err := taruntime.Ensure(ctx, manager.Layout, manager.Client, item.Runtime, func(stage string, current, total int64) { manager.report(progress, stage, current, total, "runtime") }); err != nil {
			return outcome, fmt.Errorf("ensure repair runtime: %w", err)
		}
	}
	materialized, err := manager.materializeArtifact(ctx, item, progress)
	if err != nil {
		return outcome, err
	}
	removeStage := true
	defer func() {
		if removeStage {
			if err := filesystem.SafeRemove(manager.Layout.Apps, materialized.stage); err != nil {
				outcome.Warnings = append(outcome.Warnings, err.Error())
			}
		}
	}()
	spec.IconSourceRoot = materialized.applicationRoot
	if spec.Icon != materialized.iconSource || spec.IconSize != materialized.iconSize {
		return outcome, errors.New("repair icon metadata differs from installed state")
	}
	if spec.Icon != "" {
		digest, err := integration.IconDigest(spec.IconSourceRoot, spec.Icon)
		if err != nil || digest != spec.IconSHA256 {
			return outcome, errors.New("repair icon does not match installed ownership digest")
		}
	}
	if spec.DesktopEnabled && integration.DesktopDigest(spec, integration.ExpectedPaths(spec).Executables[0].Link) != spec.DesktopSHA256 {
		return outcome, errors.New("repair desktop entry does not match installed ownership digest")
	}
	if err := ctx.Err(); err != nil {
		return outcome, err
	}
	manager.report(progress, "integrating", 0, 0)
	_, undoIntegration, err := integration.Ensure(spec)
	if err != nil {
		return outcome, err
	}
	committed := false
	defer func() {
		if !committed {
			returnErr = errors.Join(returnErr, undoIntegration())
		}
	}()
	if err := manager.inject("after_integration"); err != nil {
		return outcome, err
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return outcome, err
	}
	manager.report(progress, "installing", 0, 0)
	_, err = os.Lstat(finalPath)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return outcome, err
	}
	// Exchange keeps the canonical payload continuously available. Never use a
	// remove-then-rename fallback on filesystems without atomic exchange.
	exchange := func() error {
		return unix.Renameat2(unix.AT_FDCWD, materialized.applicationRoot, unix.AT_FDCWD, finalPath, unix.RENAME_EXCHANGE)
	}
	if existed {
		err = exchange()
	} else {
		err = os.Rename(materialized.applicationRoot, finalPath)
	}
	if err != nil {
		return outcome, fmt.Errorf("publish repaired payload: %w", err)
	}
	defer func() {
		if committed {
			return
		}
		var err error
		if existed {
			err = exchange()
		} else {
			err = os.Rename(finalPath, materialized.applicationRoot)
		}
		if err != nil {
			removeStage = false // retain the original payload for manual recovery
			returnErr = errors.Join(returnErr, fmt.Errorf("restore payload (backup at %s): %w", materialized.applicationRoot, err))
		} else {
			returnErr = errors.Join(returnErr, syncDirectory(filepath.Dir(finalPath)))
		}
	}()
	if err := syncDirectory(filepath.Dir(finalPath)); err != nil {
		return outcome, err
	}
	if err := syncDirectory(filepath.Dir(materialized.applicationRoot)); err != nil {
		return outcome, err
	}
	manager.report(progress, "activating", 0, 0)
	undoCurrent, err := switchCurrent(appRoot, oldTarget, target)
	if err != nil {
		return outcome, err
	}
	defer func() {
		if !committed {
			returnErr = errors.Join(returnErr, undoCurrent())
		}
	}()
	if err := manager.inject("after_activation"); err != nil {
		return outcome, err
	}
	committed = true
	outcome.State = installed
	return outcome, nil
}

func repairExecutablesMatch(installed []state.Executable, approved []manifest.Executable) bool {
	if len(installed) != len(approved) {
		return false
	}
	for i, executable := range installed {
		if executable.Name != approved[i].Name || executable.Path != approved[i].Path || executable.WantsBinLink() != approved[i].WantsBinLink() {
			return false
		}
	}
	return true
}

func repairRuntimeMatches(installed, approved *manifest.Runtime) bool {
	if installed == nil || approved == nil {
		return installed == nil && approved == nil
	}
	installedFingerprint, installedErr := installed.Fingerprint()
	approvedFingerprint, approvedErr := approved.Fingerprint()
	return installedErr == nil && approvedErr == nil && installedFingerprint == approvedFingerprint
}
