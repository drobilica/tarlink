// Package registrycheck validates registry structure and, when requested,
// materializes registry artifacts through TarLink's production lifecycle.
package registrycheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
)

type Selection struct {
	Items []*manifest.Manifest
}

func Structural(root string) error {
	_, err := registry.ValidateTree(root)
	return err
}

// Select validates root once and selects the requested materialization set.
// Keeping validation and selection together lets callers that need both avoid
// reparsing every manifest (the registry checker is commonly run this way).
func Select(root, appID string, allArtifacts bool, oldRoot string) (Selection, error) {
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return Selection{}, err
	}
	switch {
	case appID != "":
		return appFromCatalog(catalog, appID)
	case allArtifacts:
		return allFromCatalog(catalog), nil
	case oldRoot != "":
		return changedFromCatalog(root, oldRoot, catalog)
	default:
		return Selection{}, nil
	}
}

func All(root string) (Selection, error) {
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return Selection{}, err
	}
	return allFromCatalog(catalog), nil
}

func allFromCatalog(catalog *registry.Catalog) Selection {
	var items []*manifest.Manifest
	for _, variants := range catalog.Variants {
		for _, item := range variants {
			items = append(items, releaseProjections(item)...)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		if items[i].Platform.Arch != items[j].Platform.Arch {
			return items[i].Platform.Arch < items[j].Platform.Arch
		}
		if items[i].Release.Channel != items[j].Release.Channel {
			return items[i].Release.Channel < items[j].Release.Channel
		}
		return items[i].Release.Version < items[j].Release.Version
	})
	return Selection{Items: items}
}

func App(root, id string) (Selection, error) {
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return Selection{}, err
	}
	return appFromCatalog(catalog, id)
}

func appFromCatalog(catalog *registry.Catalog, id string) (Selection, error) {
	all := allFromCatalog(catalog)
	var items []*manifest.Manifest
	for _, item := range all.Items {
		if item.ID == id {
			items = append(items, item)
		}
	}
	if len(items) > 0 {
		return Selection{Items: items}, nil
	}
	return Selection{}, fmt.Errorf("application %q is not in the registry", id)
}

// Changed selects new or materialization-affecting manifests. oldRoot is an
// extracted tree from the comparison commit, not a YAML parser workaround.
func Changed(root, oldRoot string) (Selection, error) {
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return Selection{}, err
	}
	return changedFromCatalog(root, oldRoot, catalog)
}

func changedFromCatalog(_ string, oldRoot string, catalog *registry.Catalog) (Selection, error) {
	current := platformDefinitions(catalog)
	previous, err := comparisonDefinitions(oldRoot)
	if err != nil {
		return Selection{}, err
	}
	for key, before := range previous {
		if _, exists := current[key]; !exists {
			return Selection{}, fmt.Errorf("approved platform definition was removed: %s %s/%s", before.ID, before.Platform.OS, before.Platform.Arch)
		}
	}

	selected := make(map[string]*manifest.Manifest)
	for key, after := range current {
		before, exists := previous[key]
		if !exists {
			selectProjections(selected, after)
			continue
		}
		if after.Revision < before.Revision {
			return Selection{}, fmt.Errorf("package revision decreased for %s %s/%s: current revision: %d, previous: %d", after.ID, after.Platform.OS, after.Platform.Arch, after.Revision, before.Revision)
		}
		materiallyChanged := affectsMaterialization(before, after)
		if materiallyChanged && after.Revision <= before.Revision {
			return Selection{}, fmt.Errorf("package definition changed without revision bump: %s %s-%s current revision: %d required: >= %d", after.ID, after.Platform.Arch, after.Platform.OS, after.Revision, before.Revision+1)
		}
		added, compareErr := historyChanges(before, after)
		if compareErr != nil {
			return Selection{}, fmt.Errorf("compare %s %s/%s: %w", after.ID, after.Platform.OS, after.Platform.Arch, compareErr)
		}
		if materiallyChanged || after.Revision > before.Revision {
			selectProjections(selected, after)
			continue
		}
		for _, item := range added {
			selected[projectionIdentity(item)] = item
		}
	}
	items := make([]*manifest.Manifest, 0, len(selected))
	for _, item := range selected {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		if items[i].Platform.Arch != items[j].Platform.Arch {
			return items[i].Platform.Arch < items[j].Platform.Arch
		}
		if items[i].Release.Channel != items[j].Release.Channel {
			return items[i].Release.Channel < items[j].Release.Channel
		}
		return items[i].Release.Version < items[j].Release.Version
	})
	return Selection{Items: items}, nil
}

func releaseProjections(item *manifest.Manifest) []*manifest.Manifest {
	if item == nil {
		return nil
	}
	result := make([]*manifest.Manifest, 0, len(item.ReleaseHistory.Releases))
	for _, release := range item.ReleaseHistory.Releases {
		copy := *item
		copy.Release = release
		result = append(result, &copy)
	}
	return result
}

func platformDefinitions(catalog *registry.Catalog) map[string]*manifest.Manifest {
	result := make(map[string]*manifest.Manifest)
	if catalog == nil {
		return result
	}
	for id, variants := range catalog.Variants {
		for platform, item := range variants {
			result[platformIdentity(id, platform)] = item
		}
	}
	return result
}

func platformIdentity(id string, platform manifest.Platform) string {
	return id + "\x00" + platform.OS + "\x00" + platform.Arch
}

func projectionIdentity(item *manifest.Manifest) string {
	return platformIdentity(item.ID, item.Platform) + "\x00" + item.Release.Channel + "\x00" + item.Release.Version
}

func selectProjections(selected map[string]*manifest.Manifest, item *manifest.Manifest) {
	for _, projected := range releaseProjections(item) {
		selected[projectionIdentity(projected)] = projected
	}
}

func historyChanges(before, after *manifest.Manifest) ([]*manifest.Manifest, error) {
	oldReleases := make(map[string]manifest.Release, len(before.ReleaseHistory.Releases))
	for _, release := range before.ReleaseHistory.Releases {
		oldReleases[release.Channel+"\x00"+release.Version] = release
	}
	newReleases := make(map[string]manifest.Release, len(after.ReleaseHistory.Releases))
	for _, release := range after.ReleaseHistory.Releases {
		key := release.Channel + "\x00" + release.Version
		newReleases[key] = release
		if old, ok := oldReleases[key]; ok && old != release && after.Revision <= before.Revision {
			return nil, fmt.Errorf("approved release %q in channel %q was mutated", release.Version, release.Channel)
		}
	}
	for key, old := range oldReleases {
		if _, ok := newReleases[key]; !ok {
			// A release correction is an explicit pre-1.0 escape hatch for an
			// immutable upstream version whose TarLink payload contract was
			// wrong. The corrected release must use the old version plus the
			// reserved -appimage suffix; all other removals remain rejected.
			if _, corrected := newReleases[old.Channel+"\x00"+old.Version+"-appimage"]; corrected {
				continue
			}
			// The pre-revision registry used a synthetic -appimage suffix for
			// this release correction. Permit the one-way return to the
			// authoritative upstream version while retaining the normal
			// immutable-release rule for all other removals.
			if strings.HasSuffix(old.Version, "-appimage") {
				if _, corrected := newReleases[old.Channel+"\x00"+strings.TrimSuffix(old.Version, "-appimage")]; corrected {
					continue
				}
			}
			return nil, fmt.Errorf("approved release %q in channel %q was removed", old.Version, old.Channel)
		}
	}
	var added []*manifest.Manifest
	for _, release := range after.ReleaseHistory.Releases {
		if _, ok := oldReleases[release.Channel+"\x00"+release.Version]; !ok {
			copy := *after
			copy.Release = release
			added = append(added, &copy)
		}
	}
	return added, nil
}

func Materialize(ctx context.Context, item *manifest.Manifest) error {
	return MaterializeWithClient(ctx, item, download.NewClient())
}

func MaterializeWithClient(ctx context.Context, item *manifest.Manifest, client *download.Client) error {
	if item == nil {
		return errors.New("manifest is nil")
	}
	home, err := os.MkdirTemp("", "tarlink-registry-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	layout, err := filesystem.LayoutFor(home, func(name string) string {
		switch name {
		case "XDG_DATA_HOME":
			return filepath.Join(home, "data")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "XDG_CACHE_HOME":
			return filepath.Join(home, "cache")
		default:
			return ""
		}
	})
	if err != nil {
		return err
	}
	manager := install.New(layout, client)
	// Materialization operates on a release projection. Preserve its
	// registry-approved channel in the state record so the lifecycle check
	// exercises the same tracking metadata as a real install.
	if _, err := manager.InstallWithOptions(ctx, item, install.Options{Channel: item.Release.Channel}, nil); err != nil {
		return fmt.Errorf("materialize %s %s/%s: %w", item.ID, item.Platform.OS, item.Platform.Arch, err)
	}
	if _, err := state.LoadForApp(layout, item.ID); err != nil {
		return fmt.Errorf("verify materialized state for %s: %w", item.ID, err)
	}
	if err := manager.Uninstall(ctx, item.ID, nil); err != nil {
		return fmt.Errorf("uninstall %s: %w", item.ID, err)
	}
	if _, err := state.LoadForApp(layout, item.ID); !os.IsNotExist(err) {
		return fmt.Errorf("state remains after uninstall for %s: %v", item.ID, err)
	}
	return nil
}

func affectsMaterialization(before, after *manifest.Manifest) bool {
	if before == nil || after == nil {
		return true
	}
	return before.Platform != after.Platform ||
		before.Name != after.Name ||
		!reflect.DeepEqual(before.Categories, after.Categories) ||
		!reflect.DeepEqual(before.Requirements, after.Requirements) ||
		!reflect.DeepEqual(before.Application, after.Application) ||
		before.Desktop.Enabled != after.Desktop.Enabled ||
		before.Desktop.Executable != after.Desktop.Executable ||
		before.Desktop.WorkingDirectory != after.Desktop.WorkingDirectory ||
		!reflect.DeepEqual(before.Desktop.Categories, after.Desktop.Categories) ||
		before.Desktop.Icon != after.Desktop.Icon
}
