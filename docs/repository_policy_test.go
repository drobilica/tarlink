package docs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

// A required check must be emitted for every PR; otherwise protection can
// permanently block an agent's valid change or accept an unrelated check.
func TestRequiredCheckRunsForEveryPullRequest(t *testing.T) {
	var policy struct {
		RequiredStatusChecks struct {
			Strict bool `json:"strict"`
			Checks []struct {
				Context string `json:"context"`
				AppID   int    `json:"app_id"`
			} `json:"checks"`
		} `json:"required_status_checks"`
	}
	data, err := os.ReadFile(filepath.Join("..", ".github", "branch-protection.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Name string `yaml:"name"`
			If   string `yaml:"if"`
		} `yaml:"jobs"`
	}
	data, err = os.ReadFile(filepath.Join("..", ".github", "workflows", "main.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if value, ok := workflow.On["pull_request"]; !ok || value != nil {
		t.Fatal("required validation must run for every pull request without event, branch, or path filters")
	}
	if len(policy.RequiredStatusChecks.Checks) == 0 || !policy.RequiredStatusChecks.Strict {
		t.Fatal("up-to-date PR checks are not required")
	}
	for _, check := range policy.RequiredStatusChecks.Checks {
		matches := 0
		for _, job := range workflow.Jobs {
			if job.Name == check.Context && job.If == "" {
				matches++
			}
		}
		if matches != 1 || check.AppID != 15368 {
			t.Fatalf("required check %+v has no unique unconditional GitHub Actions job", check)
		}
	}
}
