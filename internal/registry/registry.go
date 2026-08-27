// Package registry validates and reads the one official TarLink registry.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/manifest"
)

const (
	OfficialArchiveURL = "https://codeload.github.com/drobilica/tarlink-registry/tar.gz/refs/heads/main"
	DefaultMaxAge      = 24 * time.Hour
	// GenerationMetadataFile is the cache-local metadata file written only
	// after a downloaded registry has been validated. Its checked-at value is
	// the freshness source; directory timestamps are deliberately ignored.
	GenerationMetadataFile = "metadata.json"
)

var ErrUnavailable = errors.New("validated registry is unavailable")
var ErrUnavailableForPlatform = errors.New("application is unavailable for this platform")

type PlatformError struct {
	Name string
	OS   string
	Arch string
}

func (e *PlatformError) Error() string {
	return fmt.Sprintf("%s is not available for %s/%s", e.Name, e.OS, e.Arch)
}

func (e *PlatformError) Unwrap() error { return ErrUnavailableForPlatform }

type Catalog struct {
	FetchedAt time.Time
	Variants  map[string]map[manifest.Platform]*manifest.Manifest
}

func ValidateTree(root string) (*Catalog, error) {
	return validateTree(root, true)
}

// validateTree validates a registry tree and optionally reads cache-local
// generation metadata. Downloaded archive roots are not cache generations;
// any metadata shipped in an archive is ignored and replaced on activation.
func validateTree(root string, readMetadata bool) (*Catalog, error) {
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
	var fetchedAt time.Time
	if readMetadata {
		fetchedAt, err = readGenerationMetadata(root)
		if err != nil {
			return nil, err
		}
	}
	appsRoot := filepath.Join(root, "apps")
	if err := rejectSymlinks(appsRoot); err != nil {
		return nil, err
	}
	variants, err := loadManifests(appsRoot)
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return nil, errors.New("registry contains no application manifests")
	}
	return &Catalog{FetchedAt: fetchedAt, Variants: variants}, nil
}

type generationMetadata struct {
	CheckedAt string `json:"checked_at"`
}

// readGenerationMetadata treats metadata as disposable cache state. A
// missing file is accepted for old caches, but leaves FetchedAt zero so the
// caller will refresh it before relying on it as fresh.
func readGenerationMetadata(root string) (time.Time, error) {
	path := filepath.Join(root, GenerationMetadataFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return time.Time{}, errors.New("registry generation metadata must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer file.Close()
	limited := io.LimitReader(file, 4097)
	data, err := io.ReadAll(limited)
	if err != nil {
		return time.Time{}, err
	}
	if len(data) > 4096 {
		return time.Time{}, errors.New("registry generation metadata is too large")
	}
	var metadata generationMetadata
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.CheckedAt == "" {
		return time.Time{}, errors.New("registry generation metadata is invalid")
	}
	checkedAt, err := time.Parse(time.RFC3339, metadata.CheckedAt)
	if err != nil {
		return time.Time{}, errors.New("registry generation metadata has invalid checked-at")
	}
	return checkedAt.UTC(), nil
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
	age := now.Sub(c.FetchedAt)
	return age < 0 || age >= maxAge
}

func (c *Catalog) ManifestForPlatform(id, goos, goarch string) (*manifest.Manifest, error) {
	if c == nil {
		return nil, errors.New("registry catalog is nil")
	}
	variants, ok := c.Variants[id]
	if !ok {
		return nil, fmt.Errorf("application %q is not in the registry", id)
	}
	item, ok := variants[manifest.Platform{OS: goos, Arch: goarch}]
	if !ok {
		name := id
		for _, candidate := range variants {
			name = candidate.Name
			break
		}
		return nil, &PlatformError{Name: name, OS: goos, Arch: goarch}
	}
	copy := *item
	copy.Categories = append([]string(nil), item.Categories...)
	copy.Requirements = append([]string(nil), item.Requirements...)
	copy.Desktop.Categories = append([]string(nil), item.Desktop.Categories...)
	copy.ReleaseHistory.Releases = append([]manifest.Release(nil), item.ReleaseHistory.Releases...)
	copy.ReleaseHistory.Channels = make(map[string]manifest.ChannelHead, len(item.ReleaseHistory.Channels))
	for channel, head := range item.ReleaseHistory.Channels {
		copy.ReleaseHistory.Channels[channel] = head
	}
	return &copy, nil
}

// ReleaseForPlatform resolves an explicitly requested approved channel head
// or opaque version. It never consults upstream metadata or sorts versions.
func (c *Catalog) ReleaseForPlatform(id, goos, goarch, selector string) (*manifest.Manifest, error) {
	item, err := c.ManifestForPlatform(id, goos, goarch)
	if err != nil {
		return nil, err
	}
	channel := selector
	version := ""
	if head, ok := item.ReleaseHistory.Channels[selector]; ok {
		version = head.Current
	} else {
		version = selector
		channel = ""
	}
	for _, release := range item.ReleaseHistory.Releases {
		if release.Version == version && (channel == "" || release.Channel == channel) {
			item.Release = release
			return item, nil
		}
	}
	return nil, fmt.Errorf("approved release %q for %s is not available", selector, id)
}

func (c *Catalog) SearchForPlatform(query, goos, goarch string) []*manifest.Manifest {
	if c == nil {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	var result []*manifest.Manifest
	items := make(map[string]*manifest.Manifest)
	platform := manifest.Platform{OS: goos, Arch: goarch}
	for id, variants := range c.Variants {
		if item, ok := variants[platform]; ok {
			items[id] = item
		}
	}
	for _, item := range items {
		haystack := strings.ToLower(strings.Join([]string{item.ID, item.Name, item.Summary, strings.Join(item.Categories, " ")}, " "))
		if query == "" || strings.Contains(haystack, query) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func loadManifests(appsRoot string) (map[string]map[manifest.Platform]*manifest.Manifest, error) {
	entries, err := os.ReadDir(appsRoot)
	if err != nil {
		return nil, fmt.Errorf("read registry applications: %w", err)
	}
	variants := make(map[string]map[manifest.Platform]*manifest.Manifest, len(entries))
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
		if len(children) == 0 || len(children) > 2 {
			return nil, fmt.Errorf("application directory %q must contain one or two platform manifests", entry.Name())
		}
		appVariants := make(map[manifest.Platform]*manifest.Manifest, len(children))
		var representative *manifest.Manifest
		for _, child := range children {
			expected, ok := platformFilename(child.Name())
			if !ok {
				return nil, fmt.Errorf("application directory %q contains unexpected file %q", entry.Name(), child.Name())
			}
			filePath := filepath.Join(directory, child.Name())
			info, err := os.Lstat(filePath)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("application manifest %q must be a regular file", child.Name())
			}
			file, err := os.Open(filePath)
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
			if item.Platform != expected {
				return nil, fmt.Errorf("manifest %q platform %s/%s does not match filename %q", child.Name(), item.Platform.OS, item.Platform.Arch, child.Name())
			}
			if _, duplicate := appVariants[item.Platform]; duplicate {
				return nil, fmt.Errorf("duplicate platform %s/%s for application %q", item.Platform.OS, item.Platform.Arch, item.ID)
			}
			if representative == nil {
				representative = item
			} else if !sameApplicationMetadata(representative, item) {
				return nil, fmt.Errorf("platform manifests for application %q have inconsistent shared metadata", item.ID)
			}
			foldedName := strings.ToLower(item.Name)
			if previous, duplicate := names[foldedName]; duplicate && previous != item.ID {
				return nil, fmt.Errorf("duplicate application name %q for %s and %s", item.Name, previous, item.ID)
			}
			names[foldedName] = item.ID
			appVariants[item.Platform] = item
		}
		variants[entry.Name()] = appVariants
	}
	return variants, nil
}

func platformFilename(name string) (manifest.Platform, bool) {
	switch name {
	case "linux-amd64.yaml":
		return manifest.Platform{OS: "linux", Arch: "amd64"}, true
	case "linux-arm64.yaml":
		return manifest.Platform{OS: "linux", Arch: "arm64"}, true
	default:
		return manifest.Platform{}, false
	}
}

func sameApplicationMetadata(left, right *manifest.Manifest) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Summary == right.Summary &&
		left.Homepage == right.Homepage && reflect.DeepEqual(left.Categories, right.Categories) &&
		reflect.DeepEqual(left.Requirements, right.Requirements) &&
		reflect.DeepEqual(left.Desktop, right.Desktop)
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
