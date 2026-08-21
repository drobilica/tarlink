// Package registrycheck validates registry structure and, when requested,
// materializes registry artifacts through TarLink's production lifecycle.
package registrycheck

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

func changedFromCatalog(root, oldRoot string, _ *registry.Catalog) (Selection, error) {
	current, err := manifestFiles(root)
	if err != nil {
		return Selection{}, err
	}
	old, err := manifestFiles(oldRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Selection{}, err
	}
	// An approved v3 manifest must not disappear in a later registry
	// generation.  Keep the migration behavior for retired v1 manifests:
	// those cannot be parsed by the v3 parser and are intentionally ignored.
	for path, oldPath := range old {
		if _, exists := current[path]; exists {
			continue
		}
		previous, parseErr := parseManifest(oldPath)
		if parseErr == nil && previous.Schema == manifest.SchemaV3 {
			return Selection{}, fmt.Errorf("approved manifest %s was removed", path)
		}
	}
	selected := make(map[string]struct{})
	for path, currentPath := range current {
		oldPath, exists := old[path]
		if !exists {
			for _, item := range mustParseProjections(currentPath) {
				selected[projectionKeyForPath(currentPath, item)] = struct{}{}
			}
			continue
		}
		before, beforeErr := parseManifest(oldPath)
		after, afterErr := parseManifest(currentPath)
		// A historical manifest may be unreadable only because the current
		// pre-1.0 parser deliberately removed its schema. Structural validation
		// already validated the current tree; without a comparable old manifest,
		// a schema-only migration must not turn every unchanged artifact into an
		// audit target.
		if afterErr != nil {
			selected[currentPath] = struct{}{}
			continue
		}
		if beforeErr == nil {
			added, err := historyChanges(before, after)
			if err != nil {
				return Selection{}, fmt.Errorf("compare %s: %w", path, err)
			}
			if affectsMaterialization(before, after) {
				for _, item := range releaseProjections(after) {
					selected[projectionKeyForPath(currentPath, item)] = struct{}{}
				}
			} else {
				for _, item := range added {
					selected[projectionKeyForPath(currentPath, item)] = struct{}{}
				}
			}
		}
	}
	var items []*manifest.Manifest
	for key := range selected {
		path, releaseKey := splitProjectionKey(key)
		item, err := parseManifest(path)
		if err != nil {
			return Selection{}, fmt.Errorf("parse changed manifest %s: %w", path, err)
		}
		for _, projected := range releaseProjections(item) {
			if projected.Release.Channel+"\x00"+projected.Release.Version == releaseKey {
				items = append(items, projected)
				break
			}
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

func projectionKeyForPath(path string, item *manifest.Manifest) string {
	return projectionKeyParts(path, item)
}

func projectionKeyParts(path string, item *manifest.Manifest) string {
	return path + "\x00" + item.Release.Channel + "\x00" + item.Release.Version
}

func splitProjectionKey(key string) (string, string) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return key, ""
	}
	return parts[0], parts[1]
}

func mustParseProjections(path string) []*manifest.Manifest {
	item, err := parseManifest(path)
	if err != nil {
		return nil
	}
	return releaseProjections(item)
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
		if old, ok := oldReleases[key]; ok && old != release {
			return nil, fmt.Errorf("approved release %q in channel %q was mutated", release.Version, release.Channel)
		}
	}
	for key, old := range oldReleases {
		if _, ok := newReleases[key]; !ok {
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
		!reflect.DeepEqual(before.Application, after.Application) ||
		before.Desktop.Enabled != after.Desktop.Enabled ||
		before.Desktop.Icon != after.Desktop.Icon
}

func manifestFiles(root string) (map[string]string, error) {
	result := make(map[string]string)
	if root == "" {
		return result, nil
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(relative)
		if strings.HasPrefix(slash, "apps/") && strings.HasSuffix(slash, ".yaml") {
			result[slash] = path
		}
		return nil
	})
	return result, err
}

func parseManifest(path string) (*manifest.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	item, parseErr := manifest.Parse(file)
	closeErr := file.Close()
	return item, errors.Join(parseErr, closeErr)
}
