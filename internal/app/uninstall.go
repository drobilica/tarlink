package app

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/state"
)

func (core *Core) Uninstall(ctx context.Context, appID string, sink ProgressSink) (Result, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return Result{}, &Error{Code: CodeInvalidArguments, Op: "uninstall", Err: err}
	}
	installed, stateErr := state.LoadForApp(core.layout, appID)
	warnings, err := core.installer.UninstallSubject(ctx, appID, core.progress(sink, appID))
	if err != nil {
		var conflict *install.UninstallConflictError
		if errors.As(err, &conflict) {
			return Result{}, &Error{Code: CodeConflict, Op: "uninstall " + appID, Err: &UninstallConflictError{Conflict: UninstallConflict{AppID: appID, Path: conflict.Path}, Err: conflict}}
		}
		return Result{}, classify("uninstall "+appID, err)
	}
	core.emit(sink, ProgressComplete, appID, 0, 0)
	result := Result{AppID: appID, Warnings: warnings}
	if stateErr == nil {
		result.Version = installed.Current
		result.Fingerprint = installed.CurrentFingerprint
		result.Channel = installed.Channel
		result.Pinned = installed.Pinned
	}
	return result, nil
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

func (core *Core) UninstallAll(ctx context.Context, sink ProgressSink) (UninstallAllResult, error) {
	// Capture the managed set before the destructive operation so callers get
	// one stable outcome per application, including when a later removal fails.
	known := make(map[string]Result)
	if states, stateErr := core.installedStates(); stateErr == nil {
		for _, value := range states {
			known[value.App] = Result{AppID: value.App, Version: value.Current, Fingerprint: value.CurrentFingerprint, Channel: value.Channel, Pinned: value.Pinned}
		}
	} else if entries, readErr := os.ReadDir(core.layout.States); readErr == nil {
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
				id := strings.TrimSuffix(entry.Name(), ".json")
				if filesystem.ValidateID(id) == nil {
					known[id] = Result{AppID: id}
				}
			}
		}
	}
	warnings, err := core.installer.UninstallAll(ctx, func(appID string) install.Progress {
		return func(stage string, current, total int64) {
			core.progress(sink, appID)(stage, "package-artifact", current, total)
		}
	})
	result := UninstallAllResult{Warnings: warnings, Failed: map[string]string{}, FailureCodes: map[string]ErrorCode{}}
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		value := known[id]
		if _, stateErr := state.LoadForApp(core.layout, id); errors.Is(stateErr, os.ErrNotExist) {
			result.Completed = append(result.Completed, value)
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: id, Status: "completed", Result: &result.Completed[len(result.Completed)-1]})
		}
	}
	if err != nil {
		classified := classify("uninstall all", err)
		for _, id := range ids {
			if _, stateErr := state.LoadForApp(core.layout, id); stateErr == nil {
				result.Failed[id] = classified.Error()
				result.FailureCodes[id] = CodeOf(classified)
				result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: id, Status: "failed", Reason: classified.Error(), Code: CodeOf(classified)})
			}
		}
		return result, classified
	}
	return result, nil
}
