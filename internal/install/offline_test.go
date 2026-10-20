package install

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/artifactrepo"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
)

type offlineTransport struct{ calls int }

func (transport *offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls++
	return nil, fmt.Errorf("unexpected offline HTTP")
}

type offlineObject struct {
	digest string
	body   []byte
}

func offlineClosureFixture(t *testing.T) (*Manager, *manifest.Manifest, string, map[string]offlineObject, *offlineTransport) {
	t.Helper()
	layout := testLayout(t)
	application, runtimeBytes, icon := fixtureArchive(t, "offline"), repositoryRuntimeArchive(t), minimalPNG(t, 512, 512)
	objects := map[string]offlineObject{}
	for role, body := range map[string][]byte{"application": application, "runtime": runtimeBytes, "desktop icon": icon} {
		objects[role] = offlineObject{digest: fmt.Sprintf("%x", sha256.Sum256(body)), body: body}
	}
	source := artifactServer{server: &httptest.Server{URL: "https://upstream.invalid/application.tar.gz"}, data: application}
	item := source.manifest("1.0")
	item.Application.Executables[0].CreateBinLink = boolPointer(false)
	reference := &manifest.RuntimeReference{ID: "steam-linux-runtime-4", Version: "1.0"}
	runtime := &manifest.Runtime{Schema: manifest.RuntimeSchemaV1, ID: reference.ID, Kind: manifest.RuntimeKindSteamLinuxRuntime, Version: reference.Version, Platform: item.Platform, Interface: manifest.RuntimeInterfaceValveV2, Artifact: manifest.RuntimeArtifact{URL: "https://upstream.invalid/runtime.tar.xz", Archive: "tar.xz", Verification: manifest.Verification{Algorithm: "sha256", Digest: objects["runtime"].digest, Source: "https://upstream.invalid/releases"}}}
	item.RuntimeRef, item.Runtime = reference, runtime
	item.Release.RuntimeRef, item.Release.Runtime = reference, runtime
	item.ReleaseHistory.Releases[0] = item.Release
	item.Desktop.Icon = manifest.DesktopIcon{URL: "https://upstream.invalid/icon.png", SHA256: objects["desktop icon"].digest}
	if _, err := item.ResolvedPackageFingerprint(); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	if err := artifactrepo.Init(repository); err != nil {
		t.Fatal(err)
	}
	transport := &offlineTransport{}
	client := &download.Client{Offline: true, HTTP: &http.Client{Transport: transport}, Sources: []string{"https://mirror.invalid", repository}}
	return New(layout, client), item, repository, objects, transport
}

func offlinePublish(t *testing.T, repository string, object offlineObject) {
	t.Helper()
	if err := artifactrepo.Add(context.Background(), repository, "sha256", object.digest, func(_ context.Context, _, _, _, path string) error { return os.WriteFile(path, object.body, 0o600) }, "fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineClosureMissingObjectsAbortBeforeApplicationOrRuntimeMutation(t *testing.T) {
	for _, missing := range []string{"application", "runtime", "desktop icon", "all"} {
		t.Run(missing, func(t *testing.T) {
			manager, item, repository, objects, transport := offlineClosureFixture(t)
			for role, object := range objects {
				if missing != "all" && role != missing {
					offlinePublish(t, repository, object)
				}
			}
			_, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil)
			if err == nil {
				t.Fatal("incomplete offline closure installed")
			}
			for role, object := range objects {
				if role == missing || missing == "all" {
					if !strings.Contains(err.Error(), role) || !strings.Contains(err.Error(), object.digest) {
						t.Fatalf("missing %s not reported: %v", role, err)
					}
				}
			}
			for _, path := range []string{filepath.Join(manager.Layout.Apps, item.ID), filepath.Join(manager.Layout.States, item.ID+".json"), manager.Layout.Runtimes} {
				if entries, readErr := os.ReadDir(path); readErr == nil && len(entries) > 0 {
					t.Fatalf("payload/state mutation at %s: %v", path, entries)
				} else if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatalf("inspect %s: %v", path, readErr)
				}
			}
			if transport.calls != 0 {
				t.Fatalf("HTTP calls=%d", transport.calls)
			}
		})
	}
}

func TestOfflineRuntimeIconInstallAndPinnedRepairUseVerifiedLocalBytes(t *testing.T) {
	manager, item, repository, objects, transport := offlineClosureFixture(t)
	for _, object := range objects {
		offlinePublish(t, repository, object)
	}
	ctx := context.Background()
	outcome, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State.Runtime == nil || outcome.State.Integration.IconSize != 512 {
		t.Fatalf("incomplete closure: %+v", outcome.State)
	}
	if err := manager.SetPinned(ctx, item.ID, true); err != nil {
		t.Fatal(err)
	}
	before, err := state.LoadForApp(manager.Layout, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(manager.Layout.Apps, item.ID, "current", "bin/run")
	if err := os.WriteFile(executable, []byte("damaged"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(before.Integration.IconFile); err != nil {
		t.Fatal(err)
	}
	// After preparation the repository may be disconnected: every resource must
	// now come from the digest-verified cache, even with HTTPS sources configured.
	manager.Client.Sources = []string{"https://mirror.invalid"}
	if _, err := manager.RepairSubject(ctx, item, nil); err != nil {
		t.Fatal(err)
	}
	after, err := state.LoadForApp(manager.Layout, item.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("repair changed pin/history: %+v %v", after, err)
	}
	body, err := os.ReadFile(executable)
	if err != nil || string(body) != "offline" {
		t.Fatalf("repaired executable=%q %v", body, err)
	}
	if transport.calls != 0 {
		t.Fatalf("HTTP calls=%d", transport.calls)
	}
}

func TestOfflineRepairFailureLeavesExistingPayloadAndIntegrationUntouched(t *testing.T) {
	manager, item, repository, objects, transport := offlineClosureFixture(t)
	for _, object := range objects {
		offlinePublish(t, repository, object)
	}
	ctx := context.Background()
	before, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cacheIcon := filepath.Join(manager.Layout.ArtifactCacheRoot(), "v1", "sha256", objects["desktop icon"].digest)
	if err := os.WriteFile(cacheIcon, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Client.Sources = []string{"https://mirror.invalid"}
	executable := filepath.Join(manager.Layout.Apps, item.ID, "current", "bin/run")
	if err := os.WriteFile(executable, []byte("original damaged payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	iconBefore, err := os.ReadFile(before.State.Integration.IconFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RepairSubject(ctx, item, nil); err == nil || !strings.Contains(err.Error(), "desktop icon") {
		t.Fatalf("missing icon accepted: %v", err)
	}
	after, err := state.LoadForApp(manager.Layout, item.ID)
	if err != nil || !reflect.DeepEqual(before.State, after) {
		t.Fatalf("failed repair changed state: %v", err)
	}
	iconAfter, err := os.ReadFile(before.State.Integration.IconFile)
	if err != nil || string(iconBefore) != string(iconAfter) {
		t.Fatal("failed repair changed icon")
	}
	body, err := os.ReadFile(executable)
	if err != nil || string(body) != "original damaged payload" {
		t.Fatalf("failed repair changed payload: %q %v", body, err)
	}
	if transport.calls != 0 {
		t.Fatalf("HTTP calls=%d", transport.calls)
	}
}

func TestOfflinePreflightHonorsStricterIconLimitForSharedDigest(t *testing.T) {
	layout := testLayout(t)
	body := make([]byte, maxRemoteIconBytes+1)
	source := artifactServer{server: &httptest.Server{URL: "https://upstream.invalid/application.tar.gz"}, data: body}
	item := source.manifest("1.0")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	item.Desktop.Icon = manifest.DesktopIcon{URL: "https://upstream.invalid/icon.png", SHA256: digest}
	repository := t.TempDir()
	if err := artifactrepo.Init(repository); err != nil {
		t.Fatal(err)
	}
	offlinePublish(t, repository, offlineObject{digest: digest, body: body})
	transport := &offlineTransport{}
	manager := New(layout, &download.Client{Offline: true, HTTP: &http.Client{Transport: transport}, Sources: []string{repository}})
	_, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil)
	if err == nil || !strings.Contains(err.Error(), "desktop icon") {
		t.Fatalf("broader application limit bypassed icon bound: %v", err)
	}
	if _, err := state.LoadForApp(layout, item.ID); !os.IsNotExist(err) {
		t.Fatalf("oversized icon produced installed state: %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("HTTP calls=%d", transport.calls)
	}
}

func TestOfflineMaterializationRechecksCacheAfterPreflight(t *testing.T) {
	layout := testLayout(t)
	body := fixtureArchive(t, "offline")
	source := artifactServer{server: &httptest.Server{URL: "https://upstream.invalid/application.tar.gz"}, data: body}
	item := source.manifest("1.0")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	repository := t.TempDir()
	if err := artifactrepo.Init(repository); err != nil {
		t.Fatal(err)
	}
	offlinePublish(t, repository, offlineObject{digest: digest, body: body})
	transport := &offlineTransport{}
	manager := New(layout, &download.Client{Offline: true, HTTP: &http.Client{Transport: transport}, Sources: []string{repository}})
	ctx := context.Background()
	if err := manager.PreflightOffline(ctx, []*manifest.Manifest{item}); err != nil {
		t.Fatal(err)
	}
	manager.Client.Sources = []string{"https://mirror.invalid"}
	changed := false
	_, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, func(stage string, _, _ int64) {
		if stage == "downloading" && !changed {
			changed = true
			path := filepath.Join(layout.ArtifactCacheRoot(), "v1", "sha256", digest)
			if err := os.WriteFile(path, []byte("hostile cache substitution"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err == nil || !changed {
		t.Fatalf("materializer trusted preflight without rehash: changed=%v err=%v", changed, err)
	}
	if _, err := state.LoadForApp(layout, item.ID); !os.IsNotExist(err) {
		t.Fatalf("substituted cache produced state: %v", err)
	}
	if transport.calls != 0 {
		t.Fatalf("HTTP calls=%d", transport.calls)
	}
}
