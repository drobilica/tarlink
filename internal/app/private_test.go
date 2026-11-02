package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/privatecatalog"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
)

func privateArtifact(t *testing.T, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "recit", Mode: 0755, Size: int64(len(version))}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(version))
	tw.Close()
	gz.Close()
	return b.Bytes()
}
func privateFixture(t *testing.T, current string, a, b []byte) string {
	return fmt.Sprintf(`schema: 5
id: recit
name: Recit
summary: Private recovery toolkit
homepage: https://github.com/drobilica/recit
categories: [development]
release:
  current: %s
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: v0.1.0
      artifacts:
        linux-amd64:
          url: https://api.github.com/repos/drobilica/recit/releases/assets/101
          verification:
            digest: %x
            source: https://github.com/drobilica/recit/releases/tag/v0.1.0
    - version: v0.1.1
      artifacts:
        linux-amd64:
          url: https://api.github.com/repos/drobilica/recit/releases/assets/102
          verification:
            digest: %x
            source: https://github.com/drobilica/recit/releases/tag/v0.1.1
application:
  executable:
    name: recit
    path: recit
`, current, sha256.Sum256(a), sha256.Sum256(b))
}
func TestPrivateLifecycleAndAuthorityIsolation(t *testing.T) {
	ctx := context.Background()
	t.Setenv("TARLINK_PRIVATE_GITHUB_TOKEN", "private-token")
	a, b := privateArtifact(t, "v0.1.0"), privateArtifact(t, "v0.1.1")
	current := "v0.1.0"
	revoked := false
	corruptArtifact := false
	requests := 0
	public := registryRuntimeArchive(t, "1.0")
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		var content []byte
		if req.URL.String() == registry.OfficialArchiveURL {
			content = public
		} else {
			if revoked || req.Header.Get("Authorization") != "Bearer private-token" {
				return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			switch req.URL.Path {
			case "/repos/drobilica/recit":
				content = []byte(`{"id":1442652140,"full_name":"drobilica/recit","private":true}`)
			case "/repos/drobilica/recit/contents/catalog/recit/manifest.yaml":
				content = []byte(privateFixture(t, current, a, b))
			case "/repos/drobilica/recit/releases/assets/101", "/repos/drobilica/recit/releases/assets/102":
				id, v, artifact := "101", "v0.1.0", a
				if strings.HasSuffix(req.URL.Path, "102") {
					id, v, artifact = "102", "v0.1.1", b
				}
				if req.Header.Get("Accept") == "application/octet-stream" {
					content = artifact
					if corruptArtifact {
						content = []byte("corrupt artifact bytes")
					}
				} else {
					content = []byte(fmt.Sprintf(`{"id":%s,"state":"uploaded","name":"recit-%s-linux-amd64.tar.gz","url":"https://api.github.com/repos/drobilica/recit/releases/assets/%s","browser_download_url":"https://github.com/drobilica/recit/releases/download/%s/recit-%s-linux-amd64.tar.gz"}`, id, v, id, v, v))
				}
			default:
				t.Fatalf("unexpected private request %s", req.URL)
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(content)), ContentLength: int64(len(content))}, nil
	})
	client := download.NewClient()
	client.HTTP.Transport = transport
	core, err := NewCore(registryRuntimeLayout(t), client)
	if err != nil {
		t.Fatal(err)
	}
	if err = core.EnrollPrivate(ctx, privatecatalog.RepositoryID); err != nil {
		t.Fatal(err)
	}
	result, err := core.PrivateOperation(ctx, "install", "recit", nil)
	if err != nil || result.Version != "v0.1.0" {
		t.Fatal(result, err)
	}
	installed, err := state.LoadForApp(core.layout, "recit")
	if err != nil || installed.Authority != manifest.PrivateRecitAuthority {
		t.Fatal(installed, err)
	}
	plan, err := core.PlanUpdates(ctx, []string{"recit"})
	if err != nil || len(plan.Applications) != 1 || plan.Applications[0].Status != "error" || !strings.Contains(plan.Applications[0].Reason, "private") {
		t.Fatal("ordinary update failed to identify private authority", plan, err)
	}
	lockPath := filepath.Join(t.TempDir(), "lock.yaml")
	if err = core.WriteLock(ctx, lockPath); err != nil {
		t.Fatal(err)
	}
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := ParseLock(lockBytes)
	if err != nil || len(lock.Applications) != 1 || lock.Applications[0].Authority != manifest.PrivateRecitAuthority {
		t.Fatal("private authority not retained in lock", lock, err)
	}
	if _, err = core.resolveExactLock(ctx, lock.Applications[0]); err == nil {
		t.Fatal("lock replay enrolled private authority")
	}
	path := filepath.Join(core.layout.Apps, "recit", "current", "recit")
	current = "v0.1.1"
	corruptArtifact = true
	if _, err = core.PrivateOperation(ctx, "update", "recit", nil); err == nil {
		t.Fatal("corrupt private asset accepted")
	}
	retained, err := state.LoadForApp(core.layout, "recit")
	if err != nil || retained.Current != "v0.1.0" || retained.CurrentFingerprint != installed.CurrentFingerprint {
		t.Fatal("failed acquisition changed active identity", retained, err)
	}
	corruptArtifact = false
	if _, err = core.PrivateOperation(ctx, "update", "recit", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v0.1.1" {
		t.Fatal("update payload", string(got))
	}
	revoked = true
	if _, err = core.PrivateOperation(ctx, "update", "recit", nil); err == nil {
		t.Fatal("revoked credential accepted")
	}
	if _, err = core.Rollback(ctx, "recit", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "v0.1.0" {
		t.Fatal("rollback payload")
	}
	if _, err = core.Repair(ctx, "recit", nil); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatal("official repair didn't require private route", err)
	}
	// Ordinary local verification still works with revoked network credentials.
	before := requests
	if _, err = core.Verify(ctx, "recit"); err != nil {
		t.Fatal(err)
	}
	if requests != before {
		t.Fatal("verify contacted network")
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	core.EnableOffline()
	before = requests
	if _, err = core.PrivateOperation(ctx, "repair", "recit", nil); err != nil {
		t.Fatal(err)
	}
	if requests != before {
		t.Fatal("offline repair contacted network")
	}
	got, _ = os.ReadFile(path)
	if string(got) != "v0.1.0" {
		t.Fatal("offline repair failed")
	}
	// Clearing authority from the same package cannot change installed authority.
	private := privatecatalog.New(client.HTTP, true)
	item, err := private.Resolve(ctx, core.layout, "v0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	item.Authority = ""
	if _, err = core.installer.Update(ctx, item, nil); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatal("official takeover accepted", err)
	}
}

func TestPrivateEnrollmentRejectsOfficialCollision(t *testing.T) {
	artifacts := map[string][]byte{"recit": privateArtifact(t, "official")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	if err := core.EnrollPrivate(context.Background(), privatecatalog.RepositoryID); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatal("official collision accepted", err)
	}
	item := &manifest.Manifest{ID: "recit", Name: "Official name", Release: manifest.Release{Version: "new"}}
	installed := state.State{App: "recit", Current: "old", Authority: manifest.PrivateRecitAuthority}
	value := applicationFrom(item, &installed)
	if value.UpdateAvailable || value.RegistryVersion != "" || value.Authority != manifest.PrivateRecitAuthority {
		t.Fatal("official catalog advertised takeover", value)
	}
}
