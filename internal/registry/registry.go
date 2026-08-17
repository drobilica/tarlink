// Package registry validates and reads the one official TarLink registry.
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/manifest"
	"go.yaml.in/yaml/v3"
)

const (
	IndexSchema        = 1
	PolicySchema       = 1
	OfficialArchiveURL = "https://codeload.github.com/drobilica/tarlink-registry/tar.gz/refs/heads/main"
)

var ErrUnavailable = errors.New("validated registry is unavailable; run `tarlink registry sync`")

type Policy struct {
	Schema  int                 `yaml:"schema" json:"schema"`
	Sources map[string][]string `yaml:"sources" json:"sources"`
	parsed  map[string][]*url.URL
}

type Index struct {
	Schema int        `json:"schema"`
	Apps   []IndexApp `json:"apps"`
}

type IndexApp struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Categories []string `json:"categories"`
}

type Catalog struct {
	Root      string
	Manifests map[string]*manifest.Manifest
	Policy    *Policy
	Index     Index
}

func ValidateTree(root string) (*Catalog, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("registry root must be absolute")
	}
	if err := rejectSymlinks(root); err != nil {
		return nil, err
	}
	policy, err := loadPolicy(filepath.Join(root, "policy", "approved-sources.yaml"))
	if err != nil {
		return nil, err
	}
	manifests, err := loadManifests(filepath.Join(root, "apps"))
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return nil, errors.New("registry contains no application manifests")
	}
	if err := validatePolicy(policy, manifests); err != nil {
		return nil, err
	}
	index, source, err := loadIndex(filepath.Join(root, "index", "index.json"))
	if err != nil {
		return nil, err
	}
	expected := GenerateIndex(manifests)
	expectedBytes, err := IndexBytes(expected)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(source, expectedBytes) {
		return nil, errors.New("registry index is stale or not deterministically formatted")
	}
	return &Catalog{Root: root, Manifests: manifests, Policy: policy, Index: index}, nil
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

func (p *Policy) Allows(appID string, candidate *url.URL) bool {
	if p == nil || candidate == nil || candidate.Scheme != "https" || candidate.User != nil || !canonicalURLPath(candidate.Path, false) {
		return false
	}
	for _, prefix := range p.parsed[appID] {
		if strings.EqualFold(candidate.Host, prefix.Host) && strings.HasPrefix(candidate.EscapedPath(), prefix.EscapedPath()) {
			return true
		}
	}
	return false
}

func GenerateIndex(manifests map[string]*manifest.Manifest) Index {
	ids := make([]string, 0, len(manifests))
	for id := range manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	index := Index{Schema: IndexSchema, Apps: make([]IndexApp, 0, len(ids))}
	for _, id := range ids {
		item := manifests[id]
		index.Apps = append(index.Apps, IndexApp{
			ID: item.ID, Name: item.Name, Version: item.Release.Version,
			Categories: append([]string(nil), item.Categories...),
		})
	}
	return index
}

func IndexBytes(index Index) ([]byte, error) {
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func loadPolicy(path string) (*Policy, error) {
	data, err := readLimitedFile(path, manifest.MaxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("open approved source policy: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode approved source policy: %w", err)
	}
	if err := validateRegistryYAMLNode(&document); err != nil {
		return nil, fmt.Errorf("decode approved source policy: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode approved source policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("approved source policy must contain one YAML document")
	}
	if policy.Schema != PolicySchema {
		return nil, fmt.Errorf("unsupported source policy schema %d", policy.Schema)
	}
	policy.parsed = make(map[string][]*url.URL, len(policy.Sources))
	for id, prefixes := range policy.Sources {
		if !manifest.ValidID(id) || len(prefixes) == 0 {
			return nil, fmt.Errorf("invalid or empty source policy for %q", id)
		}
		for _, raw := range prefixes {
			parsed, err := url.Parse(raw)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !canonicalURLPath(parsed.Path, true) {
				return nil, fmt.Errorf("source prefix %q must be a narrow HTTPS directory URL", raw)
			}
			if parsed.Path == "/" {
				return nil, fmt.Errorf("source prefix %q is too broad", raw)
			}
			policy.parsed[id] = append(policy.parsed[id], parsed)
		}
	}
	return &policy, nil
}

func loadManifests(appsRoot string) (map[string]*manifest.Manifest, error) {
	entries, err := os.ReadDir(appsRoot)
	if err != nil {
		return nil, fmt.Errorf("read registry applications: %w", err)
	}
	result := make(map[string]*manifest.Manifest, len(entries))
	names := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !manifest.ValidID(entry.Name()) {
			return nil, fmt.Errorf("invalid application directory %q", entry.Name())
		}
		directory := filepath.Join(appsRoot, entry.Name())
		children, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}
		if len(children) != 1 || children[0].Name() != "manifest.yaml" || children[0].IsDir() {
			return nil, fmt.Errorf("application directory %q must contain only manifest.yaml", entry.Name())
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

func validatePolicy(policy *Policy, manifests map[string]*manifest.Manifest) error {
	for id, item := range manifests {
		if len(policy.parsed[id]) == 0 {
			return fmt.Errorf("manifest %q has no approved source policy", id)
		}
		parsed, err := url.Parse(item.Release.URL)
		if err != nil || !policy.Allows(id, parsed) {
			return fmt.Errorf("manifest %q release URL is outside approved sources", id)
		}
	}
	for id := range policy.Sources {
		if _, ok := manifests[id]; !ok {
			return fmt.Errorf("approved source policy references unknown application %q", id)
		}
	}
	return nil
}

func loadIndex(path string) (Index, []byte, error) {
	data, err := readLimitedFile(path, 16<<20)
	if err != nil {
		return Index{}, nil, fmt.Errorf("read registry index: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var index Index
	if err := decoder.Decode(&index); err != nil {
		return Index{}, nil, fmt.Errorf("decode registry index: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Index{}, nil, errors.New("registry index contains trailing data")
	}
	if index.Schema != IndexSchema {
		return Index{}, nil, fmt.Errorf("unsupported registry index schema %d", index.Schema)
	}
	return index, data, nil
}

func rejectSymlinks(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("registry root must be a real directory")
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

func readLimitedFile(filePath string, maximum int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	return data, nil
}

func validateRegistryYAMLNode(node *yaml.Node) error {
	if node == nil {
		return errors.New("invalid empty YAML node")
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are not allowed")
	}
	allowedTags := map[string]bool{"": true, "!!map": true, "!!seq": true, "!!str": true, "!!int": true, "!!bool": true, "!!null": true}
	if !allowedTags[node.Tag] || node.Tag == "!!merge" || node.Value == "<<" {
		return fmt.Errorf("YAML tag %q or merge key is not allowed", node.Tag)
	}
	for _, child := range node.Content {
		if err := validateRegistryYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func canonicalURLPath(value string, trailingSlash bool) bool {
	if value == "" || strings.Contains(value, "\\") {
		return false
	}
	if trailingSlash {
		if value == "/" || !strings.HasSuffix(value, "/") {
			return false
		}
		trimmed := strings.TrimSuffix(value, "/")
		return path.Clean(trimmed) == trimmed
	}
	return path.Clean(value) == value
}

func beneath(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
