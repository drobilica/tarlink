package registrycheck

// This file is deliberately limited to old-root comparison during the
// coordinated schema-v3 to schema-v4 registry cutover. Runtime registry
// loading accepts only schema v4.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"go.yaml.in/yaml/v3"
)

var errRetiredComparisonSchema = errors.New("retired comparison schema")

type legacyV3Manifest struct {
	Schema         int                     `yaml:"schema"`
	Revision       int                     `yaml:"revision,omitempty"`
	ID             string                  `yaml:"id"`
	Name           string                  `yaml:"name"`
	Summary        string                  `yaml:"summary"`
	Homepage       string                  `yaml:"homepage"`
	Categories     []string                `yaml:"categories"`
	Requirements   []string                `yaml:"requirements,omitempty"`
	Platform       manifest.Platform       `yaml:"platform"`
	ReleaseHistory manifest.ReleaseHistory `yaml:"release"`
	Application    manifest.Application    `yaml:"application"`
	Desktop        manifest.Desktop        `yaml:"desktop"`
}

func comparisonDefinitions(root string) (map[string]*manifest.Manifest, error) {
	if root == "" {
		return map[string]*manifest.Manifest{}, nil
	}
	if catalog, err := registry.ValidateTree(root); err == nil {
		return platformDefinitions(catalog), nil
	}
	if hasV4ComparisonLayout(root) {
		_, err := registry.ValidateTree(root)
		return nil, fmt.Errorf("validate previous schema-v4 registry: %w", err)
	}
	return legacyV3Definitions(root)
}

func hasV4ComparisonLayout(root string) bool {
	matches, err := filepath.Glob(filepath.Join(root, "apps", "*", "manifest.yaml"))
	return err == nil && len(matches) != 0
}

func legacyV3Definitions(root string) (map[string]*manifest.Manifest, error) {
	result := make(map[string]*manifest.Manifest)
	appsRoot := filepath.Join(root, "apps")
	applications, err := os.ReadDir(appsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read previous registry applications: %w", err)
	}
	for _, application := range applications {
		if !application.IsDir() || !manifest.ValidID(application.Name()) {
			return nil, fmt.Errorf("invalid previous application directory %q", application.Name())
		}
		directory := filepath.Join(appsRoot, application.Name())
		files, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			platform, supported := legacyPlatformFilename(file.Name())
			if !supported {
				return nil, fmt.Errorf("previous application directory %q contains unexpected file %q", application.Name(), file.Name())
			}
			item, err := parseLegacyV3(filepath.Join(directory, file.Name()))
			if errors.Is(err, errRetiredComparisonSchema) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("parse previous manifest %s/%s: %w", application.Name(), file.Name(), err)
			}
			if item.ID != application.Name() {
				return nil, fmt.Errorf("previous manifest ID %q does not match directory %q", item.ID, application.Name())
			}
			if item.Platform != platform {
				return nil, fmt.Errorf("previous manifest platform %s/%s does not match filename %q", item.Platform.OS, item.Platform.Arch, file.Name())
			}
			key := platformIdentity(item.ID, item.Platform)
			if _, duplicate := result[key]; duplicate {
				return nil, fmt.Errorf("duplicate previous platform definition for %s %s/%s", item.ID, item.Platform.OS, item.Platform.Arch)
			}
			result[key] = item
		}
	}
	return result, nil
}

func legacyPlatformFilename(name string) (manifest.Platform, bool) {
	switch name {
	case "linux-amd64.yaml":
		return manifest.Platform{OS: "linux", Arch: "amd64"}, true
	case "linux-arm64.yaml":
		return manifest.Platform{OS: "linux", Arch: "arm64"}, true
	default:
		return manifest.Platform{}, false
	}
}

func parseLegacyV3(filePath string) (*manifest.Manifest, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, manifest.MaxManifestBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if len(data) > manifest.MaxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes", manifest.MaxManifestBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var legacy legacyV3Manifest
	if err := decoder.Decode(&legacy); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("manifest must contain exactly one YAML document")
	}
	if legacy.Schema != manifest.SchemaV3 {
		return nil, errRetiredComparisonSchema
	}
	if legacy.Revision == 0 {
		legacy.Revision = 1
	}
	for index := range legacy.Application.Executables {
		if legacy.Application.Executables[index].Name == "" {
			legacy.Application.Executables[index].Name = path.Base(legacy.Application.Executables[index].Path)
		}
	}
	item := &manifest.Manifest{
		Schema: manifest.SchemaV4, Revision: legacy.Revision,
		ID: legacy.ID, Name: legacy.Name, Summary: legacy.Summary,
		Homepage: legacy.Homepage, Categories: legacy.Categories,
		Requirements: legacy.Requirements, Platform: legacy.Platform,
		ReleaseHistory: legacy.ReleaseHistory, Application: legacy.Application,
		Desktop: legacy.Desktop,
	}
	item.Release, err = item.ReleaseHistory.ResolveDefault()
	if err != nil {
		return nil, err
	}
	if err := item.Validate(); err != nil {
		return nil, err
	}
	if err := item.ValidateHistory(); err != nil {
		return nil, err
	}
	return item, nil
}
