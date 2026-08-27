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
	for _, id := range ids {
		if err := filesystem.ValidateID(id); err != nil {
			return nil, nil, err
		}
		if seen[id] {
			return nil, nil, fmt.Errorf("duplicate application %q", id)
		}
		seen[id] = true
		_, item, _, err := core.resolveSelector(ctx, id, nil)
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
	items, targets, err := core.resolveInstallBatch(ctx, ids)
	if err != nil {
		return BatchResult{}, err
	}
	for index, target := range targets {
		conflicts := core.checkItemPath(items[index])
		if len(conflicts) > 0 {
			return BatchResult{}, fmt.Errorf("installation preflight failed for %s: %s", target.AppID, formatPathConflicts(conflicts))
		}
	}
	result := BatchResult{Failed: map[string]string{}}
	for index, target := range targets {
		if err := ctx.Err(); err != nil {
			result.Canceled = true
			return result, err
		}
		item := items[index]
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
			result.Failed[target.AppID] = installErr.Error()
			continue
		}
		result.Completed = append(result.Completed, Result{AppID: target.AppID, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings})
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
	result := BatchResult{Failed: map[string]string{}}
	for index, id := range ids {
		if err := filesystem.ValidateID(id); err != nil {
			return result, err
		}
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
		if uninstallErr := core.installer.UninstallSubject(ctx, id, core.progress(progress, id)); uninstallErr != nil {
			if ctx.Err() != nil {
				result.Canceled = true
				return result, ctx.Err()
			}
			result.Failed[id] = uninstallErr.Error()
			continue
		}
		result.Completed = append(result.Completed, Result{AppID: id})
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
