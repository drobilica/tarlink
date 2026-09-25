package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/artifactrepo"
)

func repositoryServiceDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func repositoryServiceArchive(t *testing.T, artifacts map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	add := func(name string, mode int64, body []byte) {
		t.Helper()
		typeFlag := byte(tar.TypeDir)
		if body != nil {
			typeFlag = tar.TypeReg
		}
		header := &tar.Header{Name: name, Mode: mode, Typeflag: typeFlag, Size: int64(len(body))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			if _, err := tarWriter.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("tarlink-registry-main/", 0o755, nil)
	add("tarlink-registry-main/apps/", 0o755, nil)
	for id, content := range artifacts {
		manifest := `schema: 5
id: ` + id + `
name: ` + id + `
summary: Registry service fixture
homepage: https://example.com/
categories: [utilities]
release:
  current: "1.0"
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: "1.0"
      artifacts:
        linux-amd64:
          url: https://example.com/` + id + `.tar.gz
          verification:
            digest: ` + repositoryServiceDigest(content) + `
            source: https://example.com/SHA256SUMS
application:
  executable:
    name: ` + id + `
    path: ` + id + `
`
		add("tarlink-registry-main/apps/"+id+"/", 0o755, nil)
		add("tarlink-registry-main/apps/"+id+"/manifest.yaml", 0o644, []byte(manifest))
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func repositoryServiceCore(t *testing.T, archive []byte, artifacts map[string][]byte) (*Core, *int) {
	t.Helper()
	return registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		var payload []byte
		if request.URL.Host == "example.com" {
			name := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/"), ".tar.gz")
			payload = artifacts[name]
			if payload == nil {
				return nil, errors.New("no artifact fixture for " + name)
			}
		} else {
			payload = archive
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(payload)),
			Header:        make(http.Header),
			Request:       request,
			ContentLength: int64(len(payload)),
		}, nil
	})
}

func repositoryServiceBootstrap(t *testing.T, core *Core) {
	t.Helper()
	if _, err := core.Search(context.Background(), "bravo"); err != nil {
		t.Fatal(err)
	}
}

func repositoryServiceObject(t *testing.T, repo, digest string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repo, "v1", "sha256", digest))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestRepositoryStatusMissingOnlyIsNotFound(t *testing.T) {
	artifacts := map[string][]byte{"bravo": []byte("bravo artifact bytes")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	repo := filepath.Join(t.TempDir(), "repository")
	report, err := core.RepositoryStatus(context.Background(), repo, artifactrepo.Selection{})
	if CodeOf(err) != CodeNotFound {
		t.Fatalf("error = %v, want CodeNotFound", err)
	}
	if report.Missing == 0 || report.Missing != report.Required {
		t.Fatalf("report = %+v", report)
	}
	if report.Revision == "" {
		t.Fatal("revision is not visible")
	}
}

func TestRepositoryStatusCorruptWinsOverMissing(t *testing.T) {
	artifacts := map[string][]byte{
		"bravo":   []byte("bravo artifact bytes"),
		"charlie": []byte("charlie artifact bytes"),
	}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	repo := filepath.Join(t.TempDir(), "repository")
	if _, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false); err != nil {
		t.Fatal(err)
	}
	charlieDigest := repositoryServiceDigest(artifacts["charlie"])
	if err := os.Remove(filepath.Join(repo, "v1", "sha256", charlieDigest)); err != nil {
		t.Fatal(err)
	}
	bravoDigest := repositoryServiceDigest(artifacts["bravo"])
	if err := os.WriteFile(filepath.Join(repo, "v1", "sha256", bravoDigest), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := core.RepositoryStatus(context.Background(), repo, artifactrepo.Selection{})
	if CodeOf(err) != CodeChecksum {
		t.Fatalf("error = %v, want CodeChecksum", err)
	}
	if report.Corrupt != 1 || report.Missing != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRepositoryVerifyAbsentIsNotFound(t *testing.T) {
	artifacts := map[string][]byte{"bravo": []byte("bravo artifact bytes")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	_, err := core.RepositoryVerify(context.Background(), filepath.Join(t.TempDir(), "absent"))
	if CodeOf(err) != CodeNotFound {
		t.Fatalf("error = %v, want CodeNotFound", err)
	}
}

func TestRepositoryVerifyCorruptIsChecksum(t *testing.T) {
	artifacts := map[string][]byte{"bravo": []byte("bravo artifact bytes")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	repo := filepath.Join(t.TempDir(), "repository")
	if _, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false); err != nil {
		t.Fatal(err)
	}
	bravoDigest := repositoryServiceDigest(artifacts["bravo"])
	if err := os.WriteFile(filepath.Join(repo, "v1", "sha256", bravoDigest), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := core.RepositoryVerify(context.Background(), repo)
	if CodeOf(err) != CodeChecksum {
		t.Fatalf("error = %v, want CodeChecksum", err)
	}
	if report.Corrupt != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestRepositoryDryRunPlansWithoutMutating(t *testing.T) {
	artifacts := map[string][]byte{"bravo": []byte("bravo artifact bytes")}
	core, requests := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	before := *requests
	repo := filepath.Join(t.TempDir(), "repository")
	report, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.Plan == nil {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Plan.Acquire) != report.Required || report.Required == 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Downloaded != 0 || report.Repaired != 0 || report.Removed != 0 {
		t.Fatalf("completed counters are nonzero: %+v", report)
	}
	if _, statErr := os.Lstat(repo); !os.IsNotExist(statErr) {
		t.Fatal("dry run created the absent path")
	}
	if *requests != before {
		t.Fatalf("registry cache was refreshed: %d requests", *requests-before)
	}
}

func TestRepositorySyncFailureKeepsPartialReport(t *testing.T) {
	artifacts := map[string][]byte{
		"bravo":   []byte("bravo artifact bytes"),
		"charlie": []byte("charlie artifact bytes"),
	}
	served := map[string][]byte{
		"bravo":   artifacts["bravo"],
		"charlie": []byte("wrong bytes for charlie"),
	}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), served)
	repositoryServiceBootstrap(t, core)
	repo := filepath.Join(t.TempDir(), "repository")
	report, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false)
	if err == nil {
		t.Fatal("expected sync failure")
	}
	if report.Downloaded != 1 || report.Missing != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.Revision == "" {
		t.Fatal("revision is not visible on failure")
	}
	bravoDigest := repositoryServiceDigest(artifacts["bravo"])
	if got := repositoryServiceObject(t, repo, bravoDigest); got != string(artifacts["bravo"]) {
		t.Fatalf("bravo object = %q", got)
	}
}

func TestRepositorySyncSecondRunIsIdempotent(t *testing.T) {
	artifacts := map[string][]byte{"bravo": []byte("bravo artifact bytes")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	repo := filepath.Join(t.TempDir(), "repository")
	first, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Downloaded != first.Required || first.Required == 0 {
		t.Fatalf("first report = %+v", first)
	}
	second, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Downloaded != 0 || second.Repaired != 0 || second.Removed != 0 {
		t.Fatalf("second report = %+v", second)
	}
	if second.Unchanged != second.Required {
		t.Fatalf("second report = %+v", second)
	}
}

func TestRepositorySyncOrphanNarrowedPreservedUnrestrictedRemoved(t *testing.T) {
	artifacts := map[string][]byte{"bravo": []byte("bravo artifact bytes")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	repositoryServiceBootstrap(t, core)
	repo := filepath.Join(t.TempDir(), "repository")
	if _, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false); err != nil {
		t.Fatal(err)
	}
	orphanDigest := repositoryServiceDigest([]byte("orphan bytes"))
	if err := os.WriteFile(filepath.Join(repo, "v1", "sha256", orphanDigest), []byte("orphan bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	narrowed, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{App: "bravo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if narrowed.Removed != 0 || narrowed.Preserved == 0 {
		t.Fatalf("narrowed report = %+v", narrowed)
	}
	if _, err := os.Stat(filepath.Join(repo, "v1", "sha256", orphanDigest)); err != nil {
		t.Fatalf("narrowed sync removed the orphan: %v", err)
	}
	unrestricted, err := core.RepositorySync(context.Background(), repo, artifactrepo.Selection{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if unrestricted.Removed != 1 {
		t.Fatalf("unrestricted report = %+v", unrestricted)
	}
	if _, err := os.Lstat(filepath.Join(repo, "v1", "sha256", orphanDigest)); !os.IsNotExist(err) {
		t.Fatal("unrestricted sync kept the orphan")
	}
}
