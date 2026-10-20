package app

import (
	"context"
	"os"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/state"
)

// Repair restores the installed identity through the official catalog. The
// installer rechecks that identity under lifecycle and application locks.
func (core *Core) Repair(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return Result{}, &Error{Code: CodeInvalidArguments, Op: "repair", Err: err}
	}
	installed, err := state.LoadForApp(core.layout, appID)
	if os.IsNotExist(err) {
		err = install.ErrNotInstalled
	}
	if err != nil {
		return Result{}, classify("repair "+appID, err)
	}
	if err := installed.ValidateForLayout(core.layout); err != nil {
		return Result{}, classify("repair "+appID, err)
	}
	core.emit(sink, ProgressResolving, appID, 0, 0)
	item, err := core.resolveExactLock(ctx, LockEntry{ID: appID, Channel: installed.Channel, Version: installed.Current, Fingerprint: installed.CurrentFingerprint})
	if err != nil {
		return Result{}, classify("resolve repair "+appID, err)
	}
	if err := core.checkManifestPlatform(item); err != nil {
		return Result{}, err
	}
	outcome, err := core.installer.RepairSubject(ctx, item, core.progress(sink, appID))
	if err != nil {
		return Result{}, classify("repair "+appID, err)
	}
	core.emit(sink, ProgressComplete, appID, 0, 0)
	return Result{AppID: appID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}, nil
}
