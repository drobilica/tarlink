package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	taruntime "github.com/drobilica/tarlink/internal/runtime"
	"github.com/drobilica/tarlink/internal/state"
)

// UpdatePlan is an in-memory review, not an alternate package authority or a
// replay file. Only plans produced by this core can be applied. JSON is output
// for inspection; the private snapshot binds approval to the displayed plan.
type UpdatePlan struct {
	Schema           int               `json:"schema"`
	Platform         string            `json:"platform"`
	RegistryRevision string            `json:"registry_revision"`
	Applications     []UpdatePlanEntry `json:"applications"`
	snapshot         *updatePlanSnapshot
}

type UpdateRuntime struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type UpdatePackage struct {
	Version     string         `json:"version"`
	Channel     string         `json:"channel"`
	Fingerprint string         `json:"fingerprint"`
	Runtime     *UpdateRuntime `json:"runtime,omitempty"`
}

type UpdatePlanEntry struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Status         string        `json:"status"` // update, current, pinned, error
	Current        UpdatePackage `json:"current"`
	Target         UpdatePackage `json:"target"`
	Rollback       UpdatePackage `json:"rollback"`
	PackageChanged bool          `json:"package_changed"`
	RuntimeChanged bool          `json:"runtime_changed"`
	Reason         string        `json:"reason,omitempty"`
	Code           ErrorCode     `json:"code,omitempty"`
}

type UpdatePlanService interface {
	PlanUpdates(context.Context, []string) (UpdatePlan, error)
	ApplyUpdatePlan(context.Context, UpdatePlan, ProgressSink) (UpdateAllResult, error)
}

type plannedUpdate struct {
	selector  Selector
	installed *state.State
	entry     UpdatePlanEntry
}

type updatePlanSnapshot struct {
	core               *Core
	platform, revision string
	all                bool
	entries            []plannedUpdate
}

// PlanUpdates resolves a stable ID-ordered selection through one validated
// catalog snapshot. An empty selection means every installed application.
// Catalog refresh is permitted; application artifact acquisition is not.
func (core *Core) PlanUpdates(ctx context.Context, selection []string) (UpdatePlan, error) {
	selectors := make([]Selector, 0, len(selection))
	seen := map[string]bool{}
	for _, value := range selection {
		selector, err := ParseSelector(value)
		if err != nil || seen[selector.App] {
			return UpdatePlan{}, &Error{Code: CodeInvalidArguments, Op: "plan updates", Err: fmt.Errorf("invalid or duplicate application selector %q", value)}
		}
		seen[selector.App] = true
		selectors = append(selectors, selector)
	}
	sort.Slice(selectors, func(i, j int) bool { return selectors[i].App < selectors[j].App })
	all := len(selection) == 0
	installed, err := core.updatePlanStates(selectors, all)
	if err != nil {
		return UpdatePlan{}, err
	}
	var catalogErr error
	if len(installed) > 0 {
		_, catalogErr = core.catalog(ctx, nil)
		if err := ctx.Err(); err != nil {
			return UpdatePlan{}, err
		}
	}
	var plan UpdatePlan
	err = core.installer.WithLifecycle(ctx, func() error {
		states, err := core.updatePlanStates(selectors, all)
		if err != nil {
			return err
		}
		var catalog *registry.Catalog
		if len(states) > 0 {
			catalog, err = registry.Open(filepath.Join(core.layout.Cache, "registry"))
			if err != nil && catalogErr == nil {
				catalogErr = classify("open update catalog", err)
			}
		}
		goos, goarch := core.platform()
		snapshot := &updatePlanSnapshot{core: core, platform: goos + "/" + goarch, all: all}
		if catalog != nil {
			snapshot.revision = catalog.Revision
		}
		byID := map[string]state.State{}
		for _, value := range states {
			byID[value.App] = value
			if snapshot.all {
				selectors = append(selectors, Selector{App: value.App})
			}
		}
		plan = UpdatePlan{Schema: 1, Platform: snapshot.platform, RegistryRevision: snapshot.revision, Applications: []UpdatePlanEntry{}, snapshot: snapshot}
		for _, selector := range selectors {
			if err := ctx.Err(); err != nil {
				return err
			}
			value, ok := byID[selector.App]
			entry := UpdatePlanEntry{ID: selector.App, Name: selector.App}
			var expected *state.State
			if !ok {
				entry.Status, entry.Reason, entry.Code = "error", install.ErrNotInstalled.Error(), CodeNotInstalled
			} else {
				if err := value.ValidateForLayout(core.layout); err != nil {
					return err
				}
				expected = &value
				entry.Current = updatePackage(value.Current, value.Channel, value.CurrentFingerprint, value.Runtime)
				entry.Rollback = updatePackage(value.Previous, value.PreviousChannel, value.PreviousFingerprint, value.PreviousRuntime)
				resolveErr := catalogErr
				var item *manifest.Manifest
				if catalog != nil {
					item, resolveErr = core.resolvePlannedUpdate(catalog, selector, value)
				}
				if resolveErr != nil {
					entry.Status, entry.Reason, entry.Code = "error", resolveErr.Error(), CodeOf(resolveErr)
				} else {
					fingerprint, err := item.ResolvedPackageFingerprint()
					if err != nil {
						return err
					}
					entry.Name = item.Name
					entry.Target = updatePackage(item.Release.Version, item.Release.Channel, fingerprint, item.Runtime)
					entry.PackageChanged = value.CurrentFingerprint != fingerprint
					entry.RuntimeChanged = !reflect.DeepEqual(entry.Current.Runtime, entry.Target.Runtime)
					entry.Status = "current"
					if value.Current != item.Release.Version || entry.PackageChanged {
						entry.Status = "update"
						entry.Rollback = entry.Current
					}
				}
				if value.Pinned && selector.Target == "" {
					entry.Status, entry.Reason, entry.Code = "pinned", "application is pinned", CodePinned
					entry.Rollback = updatePackage(value.Previous, value.PreviousChannel, value.PreviousFingerprint, value.PreviousRuntime)
				}
			}
			snapshot.entries = append(snapshot.entries, plannedUpdate{selector: selector, installed: expected, entry: cloneUpdateEntry(entry)})
			plan.Applications = append(plan.Applications, entry)
		}
		return nil
	})
	return plan, err
}

func updatePackage(version, channel, fingerprint string, runtime *manifest.Runtime) UpdatePackage {
	result := UpdatePackage{Version: version, Channel: channel, Fingerprint: fingerprint}
	if runtime != nil {
		fp, _ := runtime.Fingerprint()
		result.Runtime = &UpdateRuntime{ID: runtime.ID, Version: runtime.Version, Fingerprint: fp}
	}
	return result
}

func (core *Core) updatePlanStates(selectors []Selector, all bool) ([]state.State, error) {
	if all {
		return core.installedStates()
	}
	states := make([]state.State, 0, len(selectors))
	for _, selector := range selectors {
		value, err := state.LoadForApp(core.layout, selector.App)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, classify("load update state "+selector.App, err)
		}
		states = append(states, value)
	}
	return states, nil
}

func cloneUpdateEntry(entry UpdatePlanEntry) UpdatePlanEntry {
	for _, pkg := range []*UpdatePackage{&entry.Current, &entry.Target, &entry.Rollback} {
		if pkg.Runtime != nil {
			value := *pkg.Runtime
			pkg.Runtime = &value
		}
	}
	return entry
}

func (core *Core) resolvePlannedUpdate(catalog *registry.Catalog, selector Selector, installed state.State) (*manifest.Manifest, error) {
	goos, goarch := core.platform()
	target := selector.Target
	if target == "" {
		target = installed.Channel
	}
	item, err := catalog.ReleaseForPlatform(selector.App, goos, goarch, target)
	if err != nil {
		code := CodeNotFound
		if errors.Is(err, registry.ErrUnavailableForPlatform) {
			code = CodeUnsupportedPlatform
		}
		return nil, &Error{Code: code, Op: "plan update " + selector.App, Err: err}
	}
	if !item.WantsEnabled() {
		return nil, &Error{Code: CodeInvalidArguments, Op: "plan update " + selector.App, Err: errors.New("application is disabled")}
	}
	return item, nil
}

// ApplyUpdatePlan preflights the entire frozen selection under the lifecycle
// lock before any acquisition. Independent operational failures still continue
// through the batch, retaining existing per-application atomicity.
func (core *Core) ApplyUpdatePlan(ctx context.Context, plan UpdatePlan, sink ProgressSink) (UpdateAllResult, error) {
	result := UpdateAllResult{Updated: []Result{}, Skipped: []string{}, Failed: map[string]string{}, FailureCodes: map[string]ErrorCode{}}
	snapshot := plan.snapshot
	if snapshot == nil || snapshot.core != core || plan.Schema != 1 || plan.Platform != snapshot.platform || plan.RegistryRevision != snapshot.revision || len(plan.Applications) != len(snapshot.entries) {
		return result, &Error{Code: CodeInvalidArguments, Op: "apply update plan", Err: errors.New("invalid update plan")}
	}
	for i, entry := range snapshot.entries {
		if !reflect.DeepEqual(entry.entry, plan.Applications[i]) {
			return result, &Error{Code: CodeInvalidArguments, Op: "apply update plan", Err: errors.New("update plan was modified")}
		}
	}
	err := core.installer.WithLifecycle(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		selectors := make([]Selector, 0, len(snapshot.entries))
		for _, planned := range snapshot.entries {
			selectors = append(selectors, planned.selector)
		}
		currentStates, err := core.updatePlanStates(selectors, snapshot.all)
		if err != nil {
			return err
		}
		states := map[string]state.State{}
		for _, value := range currentStates {
			states[value.App] = value
		}
		stale := func(id string) error {
			return &Error{Code: CodeConflict, Op: "apply update plan", Err: fmt.Errorf("%s changed since planning; review a new update plan", id)}
		}
		if snapshot.all && len(currentStates) != len(snapshot.entries) {
			return stale("installed selection")
		}
		var catalog *registry.Catalog
		needsCatalog := false
		for _, planned := range snapshot.entries {
			needsCatalog = needsCatalog || planned.entry.Status == "update" || planned.entry.Status == "current"
		}
		if needsCatalog {
			catalog, err = registry.Open(filepath.Join(core.layout.Cache, "registry"))
			if err != nil {
				return err
			}
		}
		items := make([]*manifest.Manifest, len(snapshot.entries))
		for i, planned := range snapshot.entries {
			value, exists := states[planned.selector.App]
			if planned.installed == nil {
				if exists {
					return stale(planned.selector.App)
				}
				continue
			}
			if !exists || !reflect.DeepEqual(value, *planned.installed) {
				return stale(planned.selector.App)
			}
			if planned.entry.Status == "error" || planned.entry.Status == "pinned" {
				continue
			}
			item, err := core.resolvePlannedUpdate(catalog, planned.selector, value)
			if err != nil {
				return stale(planned.selector.App)
			}
			fingerprint, err := item.ResolvedPackageFingerprint()
			if err != nil {
				return err
			}
			if item.Release.Version != planned.entry.Target.Version || item.Release.Channel != planned.entry.Target.Channel || fingerprint != planned.entry.Target.Fingerprint {
				return stale(planned.selector.App)
			}
			items[i] = item
		}
		for i, planned := range snapshot.entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := planned.selector.App
			switch planned.entry.Status {
			case "error":
				result.Failed[id] = planned.entry.Reason
				result.FailureCodes[id] = planned.entry.Code
				continue
			case "pinned":
				result.Pinned = append(result.Pinned, id)
				result.Skipped = append(result.Skipped, id)
				continue
			case "current":
				result.Skipped = append(result.Skipped, id)
				continue
			}
			progress := func(value Progress) {
				value.Item, value.Total = i+1, len(snapshot.entries)
				if sink != nil {
					sink(value)
				}
			}
			outcome, err := core.installer.UpdatePlannedUnderLifecycle(ctx, items[i], install.Options{Channel: planned.entry.Target.Channel, Explicit: planned.selector.Target != ""}, *planned.installed, core.progress(progress, id))
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				classified := classify("update "+id, err)
				result.Failed[id] = classified.Error()
				result.FailureCodes[id] = CodeOf(classified)
				continue
			}
			core.emit(progress, ProgressComplete, id, 0, 0)
			result.Updated = append(result.Updated, Result{AppID: id, Version: outcome.State.Current, Fingerprint: outcome.State.CurrentFingerprint, Previous: outcome.State.Previous, PreviousFingerprint: outcome.State.PreviousFingerprint, Channel: outcome.State.Channel, Pinned: outcome.State.Pinned, Warnings: outcome.Warnings})
		}
		if len(result.Updated) > 0 {
			if err := taruntime.GC(core.layout); err != nil {
				result.Updated[len(result.Updated)-1].Warnings = append(result.Updated[len(result.Updated)-1].Warnings, "runtime cleanup deferred: "+err.Error())
			}
		}
		return nil
	})
	return result, err
}
