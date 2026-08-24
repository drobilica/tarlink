package app

import (
	"context"
	"errors"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
)

func (core *Core) Uninstall(ctx context.Context, appID string, sink ProgressSink) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return &Error{Code: CodeInvalidArguments, Op: "uninstall", Err: err}
	}
	if err := core.installer.Uninstall(ctx, appID, core.progress(sink, appID)); err != nil {
		var conflict *install.UninstallConflictError
		if errors.As(err, &conflict) {
			return &Error{Code: CodeConflict, Op: "uninstall " + appID, Err: &UninstallConflictError{Conflict: UninstallConflict{AppID: appID, Path: conflict.Path}, Err: conflict}}
		}
		return classify("uninstall "+appID, err)
	}
	return nil
}

func (core *Core) RemoveUninstallConflict(ctx context.Context, appID, path string) error {
	if err := filesystem.ValidateID(appID); err != nil {
		return &Error{Code: CodeInvalidArguments, Op: "remove uninstall conflict", Err: err}
	}
	if err := core.installer.RemoveUninstallConflict(ctx, appID, path); err != nil {
		return classify("remove uninstall conflict "+appID, err)
	}
	return nil
}

func (core *Core) UninstallAll(ctx context.Context, sink ProgressSink) error {
	err := core.installer.UninstallAll(ctx, func(appID string) install.Progress {
		return core.progress(sink, appID)
	})
	if err != nil {
		return classify("uninstall all", err)
	}
	return nil
}
