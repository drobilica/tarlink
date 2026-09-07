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
	approvedRepositories, approvedRepositoriesErr = LoadRepositories(approvedUpstreamsYAML)
)

type approvedUpstream struct {
	App        string `yaml:"app"`
	Repository string `yaml:"repository"`
}

// LoadRepositories validates the reviewed freshness source list. The input is
// kept as a function parameter so malformed maintainer data is testable without
// changing the compiled source list.
func LoadRepositories(data []byte) (map[string]string, error) {
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
	result := make(map[string]string, len(entries))
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
		result[entry.App] = entry.Repository
	}
	return result, nil
}

// Repository returns the explicitly reviewed GitHub repository for an app.
func Repository(appID string) (string, bool, error) {
	if approvedRepositoriesErr != nil {
		return "", false, approvedRepositoriesErr
	}
	repository, ok := approvedRepositories[appID]
	return repository, ok, nil
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
