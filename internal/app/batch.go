package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
)

func (core *Core) ResolveInstallBatch(ctx context.Context, ids []string) ([]BatchTarget, error) {
	_, result, err := core.resolveInstallBatch(ctx, ids)
	return result, err
}

func (core *Core) resolveInstallBatch(ctx context.Context, ids []string) ([]*manifest.Manifest, []BatchTarget, error) {
	if len(ids) == 0 {
		return nil, nil, fmt.Errorf("batch selection is empty")
	}
	result := make([]BatchTarget, 0, len(ids))
	items := make([]*manifest.Manifest, 0, len(ids))
	seen := map[string]bool{}
	for _, value := range ids {
		selector, err := ParseSelector(value)
		if err != nil {
			return nil, nil, err
		}
		if seen[selector.App] {
			return nil, nil, fmt.Errorf("duplicate application %q", selector.App)
		}
		seen[selector.App] = true
		_, item, _, err := core.resolveSelector(ctx, value, nil)
		if err != nil {
			return nil, nil, err
		}
		if err := core.checkManifestPlatform(item); err != nil {
			return nil, nil, err
		}
		items = append(items, item)
		result = append(result, BatchTarget{AppID: item.ID, Name: item.Name, Channel: item.Release.Channel, Version: item.Release.Version})
	}
	return items, result, nil
}

func (core *Core) InstallBatch(ctx context.Context, ids []string, sink ProgressSink) (BatchResult, error) {
	return core.InstallBatchWithOptions(ctx, ids, false, sink)
}

// InstallBatchWithOptions installs each selected application in order. The
// forcePath option acknowledges PATH conflicts for every selected item.
func (core *Core) InstallBatchWithOptions(ctx context.Context, ids []string, forcePath bool, sink ProgressSink) (BatchResult, error) {
	items, targets, err := core.resolveInstallBatch(ctx, ids)
	if err != nil {
		return BatchResult{}, err
	}
	result := BatchResult{Failed: map[string]string{}, FailureCodes: map[string]ErrorCode{}}
	for index, target := range targets {
		if err := ctx.Err(); err != nil {
			result.Canceled = true
			return result, err
		}
		item := items[index]
		// Path conflicts are independent per application. Record the failure and
		// continue with the remaining selections.
		if conflicts := core.checkItemPath(item); len(conflicts) > 0 && !forcePath {
			err := &Error{Code: CodeConflict, Op: "install " + target.AppID, Err: fmt.Errorf("path conflict: %s", formatPathConflicts(conflicts))}
			result.Failed[target.AppID] = err.Error()
			result.FailureCodes[target.AppID] = err.Code
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: target.AppID, Status: "failed", Reason: err.Error(), Code: err.Code})
			continue
		}
		progress := func(value Progress) {
			value.Item, value.Total = index+1, len(targets)
			if sink != nil {
				sink(value)
			}
		}
		outcome, installErr := core.installer.InstallWithOptionsSubject(ctx, item, install.Options{Channel: target.Channel}, core.progress(progress, target.AppID))
		if installErr != nil {
			if ctx.Err() != nil {
				result.Canceled = true
				return result, ctx.Err()
			}
			classified := classify("install "+target.AppID, installErr)
			if CodeOf(classified) == CodeAlreadyInstalled {
				result.Skipped = append(result.Skipped, target.AppID)
				result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: target.AppID, Status: "skipped", Reason: "already installed", Code: CodeAlreadyInstalled})
				continue
			}
			result.Failed[target.AppID] = classified.Error()
			result.FailureCodes[target.AppID] = CodeOf(classified)
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: target.AppID, Status: "failed", Reason: classified.Error(), Code: CodeOf(classified)})
			continue
		}
		completed := Result{AppID: target.AppID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings}
		result.Completed = append(result.Completed, completed)
		result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: target.AppID, Status: "completed", Result: &result.Completed[len(result.Completed)-1]})
	}
	return result, nil
}

func (core *Core) checkItemPath(item *manifest.Manifest) []integration.PathConflict {
	spec := integration.Spec{ID: item.ID, LocalBinDirectory: core.layout.Bin}
	for _, executable := range item.Application.Executables {
		spec.Executables = append(spec.Executables, integration.ExecutableSpec{Name: executable.Name, Path: executable.Path, CreateBinLink: executable.CreateBinLink})
	}
	return integration.CheckPath(spec, os.Getenv("PATH"))
}

func (core *Core) UninstallBatch(ctx context.Context, ids []string, sink ProgressSink) (BatchResult, error) {
	if len(ids) == 0 {
		return BatchResult{}, fmt.Errorf("batch selection is empty")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if err := filesystem.ValidateID(id); err != nil {
			return BatchResult{}, err
		}
		if seen[id] {
			return BatchResult{}, fmt.Errorf("duplicate application %q", id)
		}
		seen[id] = true
	}
	result := BatchResult{Failed: map[string]string{}, FailureCodes: map[string]ErrorCode{}}
	for index, id := range ids {
		if err := ctx.Err(); err != nil {
			result.Canceled = true
			return result, err
		}
		progress := func(value Progress) {
			value.Item, value.Total = index+1, len(ids)
			if sink != nil {
				sink(value)
			}
		}
		installed, stateErr := state.LoadForApp(core.layout, id)
		warnings, uninstallErr := core.installer.UninstallSubject(ctx, id, core.progress(progress, id))
		if uninstallErr != nil {
			if ctx.Err() != nil {
				result.Canceled = true
				return result, ctx.Err()
			}
			classified := classify("uninstall "+id, uninstallErr)
			if CodeOf(classified) == CodeNotInstalled {
				result.Skipped = append(result.Skipped, id)
				result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: id, Status: "skipped", Reason: "not installed", Code: CodeNotInstalled})
				continue
			}
			result.Failed[id] = classified.Error()
			result.FailureCodes[id] = CodeOf(classified)
			result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: id, Status: "failed", Reason: classified.Error(), Code: CodeOf(classified)})
			continue
		}
		completed := Result{AppID: id, Warnings: warnings}
		if stateErr == nil {
			completed.Version = installed.Current
			completed.Fingerprint = installed.CurrentFingerprint
			completed.Channel = installed.Channel
			completed.Pinned = installed.Pinned
		}
		result.Completed = append(result.Completed, completed)
		result.Outcomes = append(result.Outcomes, BatchOutcome{AppID: id, Status: "completed", Result: &result.Completed[len(result.Completed)-1]})
	}
	return result, nil
}

func formatPathConflicts(conflicts []PathConflict) string {
	values := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		values = append(values, conflict.Executable+" ("+conflict.Type+")")
	}
	return strings.Join(values, ", ")
}
