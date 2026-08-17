// Package registry validates and reads the one official TarLink registry.
package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/manifest"
)

const (
	OfficialArchiveURL = "https://codeload.github.com/drobilica/tarlink-registry/tar.gz/refs/heads/main"
	DefaultMaxAge      = 24 * time.Hour
)

var ErrUnavailable = errors.New("validated registry is unavailable")

type Catalog struct {
	FetchedAt time.Time
	Manifests map[string]*manifest.Manifest
}

func ValidateTree(root string) (*Catalog, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("registry root must be absolute")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("registry root must be a real directory")
	}
	appsRoot := filepath.Join(root, "apps")
	if err := rejectSymlinks(appsRoot); err != nil {
		return nil, err
	}
	manifests, err := loadManifests(appsRoot)
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return nil, errors.New("registry contains no application manifests")
	}
	return &Catalog{FetchedAt: rootInfo.ModTime(), Manifests: manifests}, nil
}

func Open(cacheRoot string) (*Catalog, error) {
	current := filepath.Join(cacheRoot, "current")
	info, err := os.Lstat(current)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrUnavailable
		}
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil, errors.New("registry current pointer is not a symlink")
	}
	target, err := os.Readlink(current)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(target) {
		return nil, errors.New("registry current pointer must be relative")
	}
	root := filepath.Clean(filepath.Join(cacheRoot, target))
	generations := filepath.Join(cacheRoot, "generations")
	if !beneath(generations, root) {
		return nil, errors.New("registry current pointer escapes generations directory")
	}
	return ValidateTree(root)
}

func (c *Catalog) Stale(now time.Time, maxAge time.Duration) bool {
	if c == nil || c.FetchedAt.IsZero() || maxAge <= 0 {
		return true
	}
	return now.Sub(c.FetchedAt) >= maxAge
}

func (c *Catalog) Manifest(id string) (*manifest.Manifest, error) {
	item, ok := c.Manifests[id]
	if !ok {
		return nil, fmt.Errorf("application %q is not in the registry", id)
	}
	copy := *item
	copy.Categories = append([]string(nil), item.Categories...)
	copy.Desktop.Categories = append([]string(nil), item.Desktop.Categories...)
	return &copy, nil
}

func (c *Catalog) Search(query string) []*manifest.Manifest {
	query = strings.ToLower(strings.TrimSpace(query))
	var result []*manifest.Manifest
	for _, item := range c.Manifests {
		haystack := strings.ToLower(strings.Join([]string{item.ID, item.Name, item.Summary, strings.Join(item.Categories, " ")}, " "))
		if query == "" || strings.Contains(haystack, query) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func loadManifests(appsRoot string) (map[string]*manifest.Manifest, error) {
	entries, err := os.ReadDir(appsRoot)
	if err != nil {
		return nil, fmt.Errorf("read registry applications: %w", err)
	}
	result := make(map[string]*manifest.Manifest, len(entries))
	names := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !manifest.ValidID(entry.Name()) {
			return nil, fmt.Errorf("invalid application directory %q", entry.Name())
		}
		directory := filepath.Join(appsRoot, entry.Name())
		children, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}
		if len(children) != 1 || children[0].Name() != "manifest.yaml" || !children[0].Type().IsRegular() {
			return nil, fmt.Errorf("application directory %q must contain only a regular manifest.yaml", entry.Name())
		}
		file, err := os.Open(filepath.Join(directory, "manifest.yaml"))
		if err != nil {
			return nil, err
		}
		item, parseErr := manifest.Parse(file)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("validate %s manifest: %w", entry.Name(), parseErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if item.ID != entry.Name() {
			return nil, fmt.Errorf("manifest ID %q does not match directory %q", item.ID, entry.Name())
		}
		foldedName := strings.ToLower(item.Name)
		if previous, duplicate := names[foldedName]; duplicate {
			return nil, fmt.Errorf("duplicate application name %q for %s and %s", item.Name, previous, item.ID)
		}
		names[foldedName] = item.ID
		result[item.ID] = item
	}
	return result, nil
}

func rejectSymlinks(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("registry apps root must be a real directory")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("registry contains symlink %q", path)
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("registry contains non-regular entry %q", path)
		}
		return nil
	})
}

func beneath(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
