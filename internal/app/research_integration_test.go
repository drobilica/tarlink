package app_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/cli"
	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/research"
)

type researchRoundTrip func(*http.Request) (*http.Response, error)

func (f researchRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCoreResearchProviderFailureIsStructuredAndCLIJSONOnly(t *testing.T) {
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
	client := &download.Client{HTTP: &http.Client{Transport: researchRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" {
			return nil, errors.New("unexpected host: " + r.URL.Host)
		}
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Body:       io.NopCloser(strings.NewReader("rate limited")),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}}
	core, err := app.NewCore(layout, client)
	if err != nil {
		t.Fatal(err)
	}
	result, researchErr := core.Research(context.Background(), app.ResearchOptions{Repository: "Owner/Repo"})
	if researchErr == nil || result.Repository != "owner/repo" || result.Provenance.Verdict != research.Error || result.Status != "ERROR" {
		t.Fatalf("result=%+v err=%v", result, researchErr)
	}
	if result.Error == nil || result.Error.Kind != research.APIErrorRateLimited || result.Error.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("provider details=%+v", result.Error)
	}

	var stdout strings.Builder
	var stderr strings.Builder
	code := (cli.Runner{Service: core, Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), []string{"registry", "inspect", "Owner/Repo", "--json"})
	if code == 0 {
		t.Fatal("CLI returned success for provider failure")
	}
	var jsonOutput map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &jsonOutput); err != nil {
		t.Fatalf("stdout was not JSON-only: %q (%v)", stdout.String(), err)
	}
	if !strings.Contains(stdout.String(), `"verdict":"ERROR"`) || !strings.Contains(stdout.String(), `"status":"ERROR"`) || !strings.Contains(stdout.String(), `"kind":"rate_limited"`) {
		t.Fatalf("structured provider error missing: %s", stdout.String())
	}
}

func newResearchCore(t *testing.T, handler func(*http.Request) *http.Response) *app.Core {
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
	client := &download.Client{HTTP: &http.Client{Transport: researchRoundTrip(func(r *http.Request) (*http.Response, error) {
		return handler(r), nil
	})}}
	core, err := app.NewCore(layout, client)
	if err != nil {
		t.Fatal(err)
	}
	return core
}

func researchReleaseJSON(assets string) string {
	return `[{"id":10,"tag_name":"v1.2.3","draft":false,"prerelease":false,"created_at":"2024-01-01T00:00:00Z","published_at":"2024-01-01T00:00:00Z","assets":[` + assets + `]}]`
}

func researchAssetJSON(id int, name, digest string) string {
	return `{"id":` + strconv.Itoa(id) + `,"name":"` + name + `","browser_download_url":"https://objects.example.test/` + name + `","size":1,"digest":"` + digest + `","content_type":"application/octet-stream","state":"uploaded"}`
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestResearchSelectorsAndRefreshRemainAvailableToInspection(t *testing.T) {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	calls := 0
	core := newResearchCore(t, func(r *http.Request) *http.Response {
		calls++
		return jsonResponse(http.StatusOK, researchReleaseJSON(researchAssetJSON(20, "linux.zip", digest)))
	})
	result, err := core.Research(context.Background(), app.ResearchOptions{Repository: "owner/repo"})
	if err != nil || result.Asset.Name != "linux.zip" || result.Provenance.Verdict != research.Acceptable {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	_, err = core.Research(context.Background(), app.ResearchOptions{Repository: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected discovery cache hit, calls=%d", calls)
	}
	_, err = core.Research(context.Background(), app.ResearchOptions{Repository: "owner/repo", Refresh: true})
	if err != nil || calls != 2 {
		t.Fatalf("refresh did not bypass cache, calls=%d", calls)
	}
}

func TestCoreResearchCLIMalformedRepository(t *testing.T) {
	core := newResearchCore(t, func(*http.Request) *http.Response {
		t.Fatal("malformed repository made a network request")
		return nil
	})
	var out strings.Builder
	code := (cli.Runner{Service: core, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "inspect", "github.com/owner/repo", "--json"})
	if code == 0 || !strings.Contains(out.String(), `"reason_code":"INVALID_REPOSITORY"`) {
		t.Fatalf("malformed repository output=%s code=%d", out.String(), code)
	}
}

func TestCoreResearchCLIInspectWithoutGitHubDigestReportsArtifactBlocker(t *testing.T) {
	body := strings.Replace(researchReleaseJSON(researchAssetJSON(20, "linux.bin", "")), `"size":1`, `"size":14`, 1)
	core := newResearchCore(t, func(r *http.Request) *http.Response {
		if strings.Contains(r.URL.Host, "objects.example.test") {
			return jsonResponse(http.StatusOK, "not an archive")
		}
		if strings.Contains(r.URL.Path, "/tags/") {
			body = strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
		}
		return jsonResponse(http.StatusOK, body)
	})
	var out strings.Builder
	code := (cli.Runner{Service: core, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "inspect", "owner/repo", "--release", "v1.2.3", "--asset", "linux.bin", "--json"})
	if code != 0 || !strings.Contains(out.String(), `"status":"BLOCKED"`) || !strings.Contains(out.String(), `"blockers":["UNSUPPORTED_ARTIFACT"]`) || strings.Contains(out.String(), `"blockers":["NO_AUTHORITATIVE_DIGEST"]`) {
		t.Fatalf("blocked inspection output=%s code=%d", out.String(), code)
	}
}

func TestCoreResearchCLIInspectComputesDigestWithoutGitHubDigest(t *testing.T) {
	var archiveBytes bytes.Buffer
	zw := gzip.NewWriter(&archiveBytes)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "harvestmoon", Mode: 0755, Size: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte{0x7f, 'E', 'L', 'F'}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	payload := archiveBytes.Bytes()
	asset := `{"id":20,"name":"linux.tar.gz","browser_download_url":"https://objects.example.test/linux.tar.gz","size":` + strconv.Itoa(len(payload)) + `,"digest":"","content_type":"application/gzip","state":"uploaded"}`
	core := newResearchCore(t, func(r *http.Request) *http.Response {
		if strings.Contains(r.URL.Host, "objects.example.test") {
			return jsonResponse(http.StatusOK, string(payload))
		}
		body := researchReleaseJSON(asset)
		if strings.Contains(r.URL.Path, "/tags/") {
			body = strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
		}
		return jsonResponse(http.StatusOK, body)
	})
	var out strings.Builder
	code := (cli.Runner{Service: core, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "inspect", "owner/repo", "--json"})
	if code != 0 || !strings.Contains(out.String(), `"status":"READY_FOR_REVIEW"`) || !strings.Contains(out.String(), `"artifact_type":"tar.gz"`) || !strings.Contains(out.String(), `"computed_digests"`) || strings.Contains(out.String(), `"blockers":["NO_AUTHORITATIVE_DIGEST"]`) {
		t.Fatalf("ready inspection output=%s code=%d", out.String(), code)
	}
}

func TestCoreResearchCLIInspectionDownloadFailurePreservesEvidence(t *testing.T) {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := researchReleaseJSON(researchAssetJSON(20, "linux.tar.gz", digest))
	core := newResearchCore(t, func(r *http.Request) *http.Response {
		if strings.Contains(r.URL.Host, "objects.example.test") {
			return jsonResponse(http.StatusBadGateway, "upstream unavailable")
		}
		if strings.Contains(r.URL.Path, "/tags/") {
			body = strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
		}
		return jsonResponse(http.StatusOK, body)
	})
	var out strings.Builder
	code := (cli.Runner{Service: core, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "inspect", "owner/repo", "--release", "v1.2.3", "--asset", "linux.tar.gz", "--json"})
	if code == 0 {
		t.Fatal("download failure returned success")
	}
	for _, field := range []string{`"repository":"owner/repo"`, `"release"`, `"asset"`, `"verdict":"ACCEPTABLE"`, `"status":"ERROR"`, `"kind":"local_failure"`} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("missing %s in structured failure: %s", field, out.String())
		}
	}
}
