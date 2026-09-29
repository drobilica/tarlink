package docs_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type discoveryWorkflow struct {
	On struct {
		Dispatch any `yaml:"workflow_dispatch"`
		Schedule []struct {
			Cron string `yaml:"cron"`
		} `yaml:"schedule"`
	} `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		RunsOn      string            `yaml:"runs-on"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadDiscoveryWorkflow(t *testing.T) discoveryWorkflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "registry-candidate-discovery.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow discoveryWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "  workflow_dispatch:") {
		t.Fatal("missing manual trigger")
	}
	return workflow
}

func TestDiscoveryWorkflowRemainsReadOnlyAndPublishesPair(t *testing.T) {
	workflow := loadDiscoveryWorkflow(t)
	if workflow.Permissions["contents"] != "read" || len(workflow.Jobs) != 1 || len(workflow.On.Schedule) != 1 {
		t.Fatalf("invalid workflow scope: %+v", workflow)
	}
	fields := strings.Fields(workflow.On.Schedule[0].Cron)
	if len(fields) != 5 || fields[2] != "*" || fields[3] != "*" || fields[4] != "*" {
		t.Fatal("discovery must run daily")
	}
	for _, value := range workflow.Permissions {
		if value != "read" && value != "none" {
			t.Fatal("workflow has write permission")
		}
	}
	checkouts, builds, discover, uploads, evidence := 0, 0, 0, 0, false
	for _, job := range workflow.Jobs {
		if job.RunsOn != "ubuntu-24.04" {
			t.Fatal("workflow must use Ubuntu 24.04")
		}
		for _, value := range job.Permissions {
			if value != "read" && value != "none" {
				t.Fatal("job has write permission")
			}
		}
		for _, step := range job.Steps {
			switch {
			case strings.HasPrefix(step.Uses, "actions/checkout@"):
				checkouts++
				if step.With["persist-credentials"] != "false" {
					t.Fatal("checkout retains credentials")
				}
				if step.With["repository"] == "" && step.With["ref"] != "${{ github.sha }}" {
					t.Fatal("source is not checked out at triggering SHA")
				}
				if step.With["repository"] != "" && (step.With["repository"] != "drobilica/tarlink-registry" || step.With["path"] != "tarlink-registry") {
					t.Fatal("unexpected comparison repository")
				}
			case strings.HasPrefix(step.Uses, "actions/upload-artifact@"):
				uploads++
				if step.With["name"] != "tarlink-candidate-discovery" || step.With["if-no-files-found"] != "error" || !strings.Contains(step.With["path"], "discovery/discovery.json") || !strings.Contains(step.With["path"], "discovery/discovery.md") || !strings.Contains(step.With["path"], "workflow-revisions.txt") {
					t.Fatal("artifact is not the complete report/evidence pair")
				}
			case step.Uses != "" && !strings.HasPrefix(step.Uses, "actions/setup-go@"):
				t.Fatalf("unexpected external action: %s", step.Uses)
			}
			builds += strings.Count(step.Run, "go build ")
			if strings.Contains(step.Run, "registry candidates discover") {
				discover++
				for _, required := range []string{"--catalog quiver:https://github.com/tgeorgiadis/quiver-community-app-catalog", "--registry", "--changed", "--output-dir discovery"} {
					if !strings.Contains(step.Run, required) {
						t.Fatalf("missing discovery option %s", required)
					}
				}
				if step.Env["GH_TOKEN"] != "${{ github.token }}" {
					t.Fatal("metadata API must use the read-only workflow token")
				}
			}
			if strings.Contains(step.Run, "git rev-parse HEAD") && strings.Contains(step.Run, "git -C tarlink-registry rev-parse HEAD") && strings.Contains(step.Run, "workflow-revisions.txt") {
				evidence = true
			}
			for _, forbidden := range []string{"git push", "git commit", "gh issue", "gh pr", "candidates.yaml", "tarlink-data"} {
				if strings.Contains(step.Run, forbidden) {
					t.Fatalf("workflow performs forbidden operation: %s", forbidden)
				}
			}
		}
	}
	if checkouts != 2 || builds != 1 || discover != 1 || uploads != 1 || !evidence {
		t.Fatalf("incomplete workflow: checkout=%d build=%d discover=%d upload=%d evidence=%v", checkouts, builds, discover, uploads, evidence)
	}
}

func TestDiscoveryWorkflowRejectsInvalidReportContract(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is unavailable; workflow uses Ubuntu's Python")
	}
	workflow := loadDiscoveryWorkflow(t)
	var code string
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "python3 - <<'PY'") {
				if !strings.Contains(step.Run, "test -s discovery/discovery.json") || !strings.Contains(step.Run, "test -s discovery/discovery.md") {
					t.Fatal("workflow doesn't verify both reports")
				}
				_, code, _ = strings.Cut(step.Run, "python3 - <<'PY'\n")
				code = strings.TrimSuffix(code, "PY\n")
			}
		}
	}
	if code == "" {
		t.Fatal("missing workflow output validator")
	}
	base := func() map[string]any {
		return map[string]any{"schema_version": 1, "catalog": map[string]any{"adapter": "quiver", "source": "https://github.com/example/catalog", "revision": strings.Repeat("a", 40)}, "registry": map[string]any{"source": "/registry"}, "summary": map[string]any{"new": 0, "release_diff": 0, "registered_same": 0, "unsupported": 0, "ambiguous": 0, "no_linux": 0}, "entries": []any{}}
	}
	for _, kind := range []string{"valid", "schema", "revision", "summary", "null-array", "incomplete-entry"} {
		t.Run(kind, func(t *testing.T) {
			report := base()
			switch kind {
			case "schema":
				report["schema_version"] = 2
			case "revision":
				report["catalog"].(map[string]any)["revision"] = ""
			case "summary":
				report["summary"].(map[string]any)["new"] = 1
			case "null-array":
				report["entries"] = nil
			case "incomplete-entry":
				report["entries"] = []any{map[string]any{"status": "new"}}
			}
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "discovery"), 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "discovery", "discovery.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(python, "-c", code)
			command.Dir = dir
			output, err := command.CombinedOutput()
			if (err == nil) != (kind == "valid") {
				t.Fatalf("result=%v output=%s", err, output)
			}
		})
	}
}
