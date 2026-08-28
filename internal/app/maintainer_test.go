package app

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
)

type maintainerRoundTrip func(*http.Request) (*http.Response, error)

func (f maintainerRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func maintainerResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func appImageELF(machine uint16) []byte {
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F'})
	data[4], data[5], data[6] = 2, 1, 1
	data[8], data[9], data[10] = 'A', 'I', 2
	binary.LittleEndian.PutUint16(data[16:18], 3)
	binary.LittleEndian.PutUint16(data[18:20], machine)
	return data
}

func maintainerReleaseObject(assetName string, payload []byte) string {
	return `{"id":10,"tag_name":"v1.2.3","draft":false,"prerelease":false,"created_at":"2024-01-01T00:00:00Z","published_at":"2024-01-01T00:00:00Z","assets":[{"id":20,"name":"` + assetName + `","browser_download_url":"https://objects.example.test/` + assetName + `","size":` + strconv.Itoa(len(payload)) + `,"digest":"","content_type":"application/octet-stream","state":"uploaded"}]}`
}

func maintainerFixture(t *testing.T, assetName string, payload []byte) *Maintainer {
	t.Helper()
	home := t.TempDir()
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
		t.Fatal(err)
	}
	release := maintainerReleaseObject(assetName, payload)
	transport := maintainerRoundTrip(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "objects.example.test"):
			return maintainerResponse(http.StatusOK, string(payload)), nil
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			return maintainerResponse(http.StatusOK, release), nil
		case strings.Contains(r.URL.Path, "/git/ref/tags/"):
			return maintainerResponse(http.StatusOK, `{"ref":"refs/tags/v1.2.3","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"commit"}}`), nil
		case strings.Contains(r.URL.Path, "/git/commits/"):
			return maintainerResponse(http.StatusOK, `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tree":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), nil
		case strings.Contains(r.URL.Path, "/git/trees/"):
			return maintainerResponse(http.StatusOK, `{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","truncated":false,"tree":[]}`), nil
		default:
			return maintainerResponse(http.StatusNotFound, `{"message":"unexpected"}`), nil
		}
	})
	return &Maintainer{layout: layout, client: &download.Client{HTTP: &http.Client{Transport: transport}}}
}

func hasRequiredInput(values []RegistryRequiredInput, field, reason string) bool {
	for _, value := range values {
		if value.Field == field && value.Reason == reason {
			return true
		}
	}
	return false
}

// TestMaintainerCandidateDerivationUsesArtifactEvidence pins that candidate
// derivation reads architecture entirely from artifact evidence and never from
// the host platform: a Maintainer has no platform resolution at all.
func TestMaintainerCandidateDerivationUsesArtifactEvidence(t *testing.T) {
	amd64ELF := appImageELF(0x3e)
	arm64ELF := appImageELF(0xb7)
	baseURL := "https://github.com/owner/repo/releases/download/v1.2.3/"

	t.Run("amd64 name matches amd64 ELF", func(t *testing.T) {
		m := maintainerFixture(t, "app-linux-amd64.AppImage", amd64ELF)
		result, err := m.InspectRegistry(context.Background(), RegistryInspectOptions{Target: baseURL + "app-linux-amd64.AppImage"})
		if err != nil {
			t.Fatal(err)
		}
		if result.Candidate == nil || result.Candidate.Platform != "linux-amd64" {
			t.Fatalf("candidate=%+v", result.Candidate)
		}
		if result.Status != "ready" {
			t.Fatalf("status=%q required=%+v", result.Status, result.Required)
		}
		for _, input := range result.Required {
			if input.Field == "artifact" {
				t.Fatalf("unexpected artifact required input: %+v", result.Required)
			}
		}
	})

	t.Run("arm64 ELF contradicts amd64 name", func(t *testing.T) {
		m := maintainerFixture(t, "app-linux-amd64.AppImage", arm64ELF)
		result, err := m.InspectRegistry(context.Background(), RegistryInspectOptions{Target: baseURL + "app-linux-amd64.AppImage"})
		if err != nil {
			t.Fatal(err)
		}
		if !hasRequiredInput(result.Required, "artifact", "unsupported_arch") {
			t.Fatalf("required=%+v", result.Required)
		}
	})

	t.Run("ambiguous name must not inherit host architecture", func(t *testing.T) {
		m := maintainerFixture(t, "app.AppImage", arm64ELF)
		result, err := m.InspectRegistry(context.Background(), RegistryInspectOptions{Target: baseURL + "app.AppImage"})
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range result.Required {
			if input.Field == "artifact" {
				t.Fatalf("artifact required input must not appear: %+v", result.Required)
			}
		}
		if !hasRequiredInput(result.Required, "platform", "ambiguous_platform") {
			t.Fatalf("required=%+v", result.Required)
		}
	})

	t.Run("same artifact produces identical candidate", func(t *testing.T) {
		m := maintainerFixture(t, "app-linux-amd64.AppImage", amd64ELF)
		first, err := m.InspectRegistry(context.Background(), RegistryInspectOptions{Target: baseURL + "app-linux-amd64.AppImage"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := m.InspectRegistry(context.Background(), RegistryInspectOptions{Target: baseURL + "app-linux-amd64.AppImage"})
		if err != nil {
			t.Fatal(err)
		}
		if first.Candidate == nil || second.Candidate == nil {
			t.Fatalf("missing candidates: %+v %+v", first.Candidate, second.Candidate)
		}
		if first.Candidate.Platform != second.Candidate.Platform || first.Candidate.SHA256 != second.Candidate.SHA256 || first.Status != second.Status {
			t.Fatalf("results differ: first=%+v second=%+v", first, second)
		}
	})
}
