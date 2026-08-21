package app

import (
	"context"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
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
	err := core.installer.UninstallAll(ctx, func(appID string) install.Progress {
		return core.progress(sink, appID)
	})
	if err != nil {
		return classify("uninstall all", err)
	}
	return nil
}
