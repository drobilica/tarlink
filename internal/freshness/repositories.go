package freshness

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed approved-upstreams.yaml
var approvedUpstreamsYAML []byte

var (
	approvedRules, approvedRepositoriesErr = LoadRules(approvedUpstreamsYAML)
)

type approvedUpstream struct {
	App           string `yaml:"app"`
	Repository    string `yaml:"repository"`
	VersionPrefix string `yaml:"version-prefix"`
	VersionSuffix string `yaml:"version-suffix"`
	AssetPrefix   string `yaml:"asset-prefix"`
	AssetExclude  string `yaml:"asset-exclude"`
}

// LoadRules validates the compiled provider, version and asset rules.
func LoadRules(data []byte) (map[string]approvedUpstream, error) {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	var entries []approvedUpstream
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode approved freshness sources: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("approved freshness sources contain multiple documents")
		}
		return nil, fmt.Errorf("decode approved freshness sources: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("approved freshness sources are empty")
	}
	result := make(map[string]approvedUpstream, len(entries))
	for _, entry := range entries {
		if err := validateRepositoryApp(entry.App); err != nil {
			return nil, err
		}
		if !repositoryPattern.MatchString(entry.Repository) {
			return nil, fmt.Errorf("invalid GitHub repository %q for %q", entry.Repository, entry.App)
		}
		if _, exists := result[entry.App]; exists {
			return nil, fmt.Errorf("duplicate approved freshness mapping for %q", entry.App)
		}
		for _, value := range []string{entry.VersionPrefix, entry.VersionSuffix, entry.AssetPrefix, entry.AssetExclude} {
			if len(value) > 80 || strings.ContainsAny(value, "/\\\n\r\t ") {
				return nil, fmt.Errorf("invalid discovery rule for %q", entry.App)
			}
		}
		for _, other := range result {
			if other.Repository == entry.Repository && (other.VersionPrefix != entry.VersionPrefix || other.VersionSuffix != entry.VersionSuffix) {
				return nil, fmt.Errorf("conflicting version rules for %q", entry.Repository)
			}
		}
		result[entry.App] = entry
	}
	return result, nil
}

// Repository returns the explicitly reviewed GitHub repository for an app.
func Repository(appID string) (string, bool, error) {
	if approvedRepositoriesErr != nil {
		return "", false, approvedRepositoriesErr
	}
	rule, ok := approvedRules[appID]
	return rule.Repository, ok, nil
}

func validateRepositoryApp(appID string) error {
	if appID == "" || len(appID) > 80 || strings.TrimSpace(appID) != appID {
		return fmt.Errorf("invalid freshness application ID %q", appID)
	}
	for index, character := range appID {
		if !(character == '-' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') || index == 0 && !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return fmt.Errorf("invalid freshness application ID %q", appID)
		}
	}
	return nil
}
