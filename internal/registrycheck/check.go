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

func All(root string) (Selection, error) {
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return Selection{}, err
	}
	var items []*manifest.Manifest
	for _, variants := range catalog.Variants {
		for _, item := range variants {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		return items[i].Platform.Arch < items[j].Platform.Arch
	})
	return Selection{Items: items}, nil
}

func App(root, id string) (Selection, error) {
	all, err := All(root)
	if err != nil {
		return Selection{}, err
	}
	for _, item := range all.Items {
		if item.ID == id {
			return Selection{Items: append([]*manifest.Manifest(nil), item)}, nil
		}
	}
	return Selection{}, fmt.Errorf("application %q is not in the registry", id)
}

// Changed selects new or materialization-affecting manifests. oldRoot is an
// extracted tree from the comparison commit, not a YAML parser workaround.
func Changed(root, oldRoot string) (Selection, error) {
	if err := Structural(root); err != nil {
		return Selection{}, err
	}
	current, err := manifestFiles(root)
	if err != nil {
		return Selection{}, err
	}
	old, err := manifestFiles(oldRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Selection{}, err
	}
	selected := make(map[string]struct{})
	for path, currentPath := range current {
		oldPath, exists := old[path]
		if !exists {
			selected[currentPath] = struct{}{}
			continue
		}
		before, beforeErr := parseManifest(oldPath)
		after, afterErr := parseManifest(currentPath)
		if beforeErr != nil || afterErr != nil || affectsMaterialization(before, after) {
			selected[currentPath] = struct{}{}
		}
	}
	var items []*manifest.Manifest
	for path := range selected {
		item, err := parseManifest(path)
		if err != nil {
			return Selection{}, fmt.Errorf("parse changed manifest %s: %w", path, err)
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return items[i].ID < items[j].ID
		}
		return items[i].Platform.Arch < items[j].Platform.Arch
	})
	return Selection{Items: items}, nil
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
	if _, err := manager.Install(ctx, item, nil); err != nil {
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
		before.Release != after.Release ||
		before.Application != after.Application ||
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
