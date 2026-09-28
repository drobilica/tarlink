package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/research"
)

func emptyDiscoveryReport() research.DiscoveryReport {
	return research.DiscoveryReport{SchemaVersion: 1, Catalog: research.DiscoveryCatalog{Adapter: "quiver", Source: "/catalog"}, Registry: research.DiscoveryRegistry{Source: "/registry"}, Entries: []research.DiscoveryEntry{}}
}

func TestDiscoveryCLIRequiresInputsAndExclusiveOutputModes(t *testing.T) {
	base := []string{"registry", "candidates", "discover"}
	valid := append(append([]string{}, base...), "--catalog", "quiver:/catalog", "--registry", "/registry")
	cases := [][]string{
		base,
		append(append([]string{}, base...), "--catalog", "quiver:/catalog"),
		append(append([]string{}, base...), "--registry", "/registry"),
		append(append([]string{}, valid...), "unexpected"),
		append(append([]string{}, valid...), "--json", "--markdown"),
		append(append([]string{}, valid...), "--json", "--output-dir", "/output"),
		append(append([]string{}, valid...), "--markdown", "--output-dir", "/output"),
		append(append([]string{}, valid...), "--output-dir="),
		append(append([]string{}, valid...), "--json", "--output-dir="),
	}
	for _, args := range cases {
		t.Run(strings.Join(args[3:], " "), func(t *testing.T) {
			service := &discoveryService{report: emptyDiscoveryReport()}
			var out, errOut bytes.Buffer
			code := (Runner{Registry: RegistryTools{Discovery: service}, Stdout: &out, Stderr: &errOut}).Run(context.Background(), args)
			if code == 0 || service.calls != 0 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("code=%d calls=%d out=%q err=%q", code, service.calls, out.String(), errOut.String())
			}
		})
	}
}

func TestDiscoveryCLIFormatsAndChangedOptions(t *testing.T) {
	for _, mode := range []string{"", "--json", "--markdown"} {
		service := &discoveryService{report: emptyDiscoveryReport()}
		var out, errOut bytes.Buffer
		args := []string{"registry", "candidates", "discover", "--catalog", "quiver:/catalog", "--registry", "/registry", "--changed"}
		if mode != "" {
			args = append(args, mode)
		}
		code := (Runner{Registry: RegistryTools{Discovery: service}, Stdout: &out, Stderr: &errOut}).Run(context.Background(), args)
		if code != 0 || service.calls != 1 || errOut.Len() != 0 || !service.options.Changed || service.options.Catalog != "quiver:/catalog" || service.options.Registry != "/registry" {
			t.Fatalf("mode=%q code=%d calls=%d options=%+v err=%q", mode, code, service.calls, service.options, errOut.String())
		}
		switch mode {
		case "--json":
			var report research.DiscoveryReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.SchemaVersion != 1 || report.Entries == nil {
				t.Fatalf("invalid machine output: %q", out.String())
			}
		case "--markdown":
			if !strings.Contains(out.String(), "| App | Catalog release | Registry | Arch | Preferred | Alternatives | Status |") {
				t.Fatalf("missing Markdown table: %q", out.String())
			}
		default:
			if !strings.Contains(out.String(), "Catalog:") || !strings.Contains(out.String(), "SUMMARY") && !strings.Contains(out.String(), "Summary:") {
				t.Fatalf("missing human provenance: %q", out.String())
			}
		}
	}
}

func TestDiscoveryCLIFailureDoesNotPublishAReportPair(t *testing.T) {
	for _, failure := range []string{"discovery", "invalid-result", "write"} {
		t.Run(failure, func(t *testing.T) {
			service := &discoveryService{report: emptyDiscoveryReport()}
			dir := filepath.Join(t.TempDir(), "reports")
			switch failure {
			case "discovery":
				service.err = errors.New("injected discovery failure")
			case "invalid-result":
				service.report.SchemaVersion = 2
			case "write":
				if err := os.MkdirAll(filepath.Join(dir, "discovery.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			code := (Runner{Registry: RegistryTools{Discovery: service}, Stdout: &out, Stderr: &errOut}).Run(context.Background(), []string{"registry", "candidates", "discover", "--catalog", "quiver:/catalog", "--registry", "/registry", "--output-dir", dir})
			if code == 0 || service.calls != 1 || out.Len() != 0 {
				t.Fatalf("code=%d calls=%d output=%q", code, service.calls, out.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "discovery.json")); !os.IsNotExist(err) {
				t.Fatalf("failed run published JSON: %v", err)
			}
		})
	}
}
