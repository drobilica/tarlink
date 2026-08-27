package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func registryRuntimeLayout(t *testing.T) filesystem.Layout {
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
	return layout
}

func registryRuntimeArchive(t *testing.T, version string) []byte {
	return registryRuntimeArchivePlatforms(t, version, false)
}

func registryRuntimeArchivePlatforms(t *testing.T, version string, arm64 bool) []byte {
	return registryRuntimeArchivePlatformVersions(t, version, version+"-arm64", arm64)
}

func registryRuntimeArchivePlatformVersions(t *testing.T, amd64Version, arm64Version string, arm64 bool) []byte {
	t.Helper()
	_ = arm64Version
	arm64Definition := ""
	if arm64 {
		arm64Definition = `        linux-arm64:
          url: https://example.com/fixture-arm64.tar.gz
          verification:
            digest: 1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/SHA256SUMS
`
	}
	manifest := `schema: 5
id: fixture
name: Fixture
summary: Registry fixture
homepage: https://example.com/
categories: [utilities]
release:
  current: "` + amd64Version + `"
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: "` + amd64Version + `"
      artifacts:
        linux-amd64:
          url: https://example.com/fixture.tar.gz
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/SHA256SUMS
` + arm64Definition + `application:
  executable:
    name: fixture
    path: fixture
`
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name string
		mode int64
		body []byte
	}{
		{name: "tarlink-registry-main/", mode: 0o755},
		{name: "tarlink-registry-main/apps/", mode: 0o755},
		{name: "tarlink-registry-main/apps/fixture/", mode: 0o755},
		{name: "tarlink-registry-main/apps/fixture/manifest.yaml", mode: 0o644, body: []byte(manifest)},
	}
	for _, entry := range entries {
		typeFlag := byte(tar.TypeDir)
		if entry.body != nil {
			typeFlag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: typeFlag, Size: int64(len(entry.body))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) != 0 {
			if _, err := tarWriter.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func registryRuntimeCore(t *testing.T, responder roundTripFunc) (*Core, *int) {
	t.Helper()
	layout := registryRuntimeLayout(t)
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return responder(request)
	})}
	client := &download.Client{HTTP: httpClient, RedirectLimit: 2}
	return &Core{
		layout: layout, installer: install.New(layout, client),
		syncer: &registry.Syncer{CacheRoot: filepath.Join(layout.Cache, "registry"), LocksRoot: layout.Locks, Client: client},
		now:    time.Now, registryMaxAge: registry.DefaultMaxAge,
		goos: "linux", goarch: "amd64",
	}, &requests
}

func registryResponse(payload []byte) roundTripFunc {
	return func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(payload)),
			Header:        make(http.Header),
			Request:       request,
			ContentLength: int64(len(payload)),
		}, nil
	}
}

func registryRuntimeSetCheckedAt(t *testing.T, generation string, checkedAt time.Time) {
	t.Helper()
	metadata := `{"checked_at":"` + checkedAt.UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(generation, registry.GenerationMetadataFile), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogAutomaticallyBootstrapsAndUsesFreshCache(t *testing.T) {
	core, requests := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "1.0" {
		t.Fatalf("initial Search() applications=%#v error=%v", applications, err)
	}
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatalf("cached Search() error = %v", err)
	}
	if *requests != 1 {
		t.Fatalf("registry requests = %d, want 1", *requests)
	}
}

func TestCatalogRefreshesStaleCache(t *testing.T) {
	payload := registryRuntimeArchive(t, "1.0")
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		return registryResponse(payload)(request)
	})
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	payload = registryRuntimeArchive(t, "2.0")
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	registryRuntimeSetCheckedAt(t, generation, old)
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("refreshed Search() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests = %d, want 2", *requests)
	}
}

func TestCatalogUsesValidStaleCacheWhenOffline(t *testing.T) {
	offline := false
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		if offline {
			return nil, errors.New("offline")
		}
		return registryResponse(registryRuntimeArchive(t, "1.0"))(request)
	})
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	registryRuntimeSetCheckedAt(t, generation, old)
	offline = true
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "1.0" {
		t.Fatalf("offline Search() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests = %d, want 2", *requests)
	}
}

func TestCatalogRejectsCorruptStaleCacheWhenOffline(t *testing.T) {
	tests := map[string]func(*testing.T, string, string){
		"current pointer": func(t *testing.T, cacheRoot, generation string) {
			current := filepath.Join(cacheRoot, "current")
			if err := os.Remove(current); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../outside", current); err != nil {
				t.Fatal(err)
			}
		},
		"generation": func(t *testing.T, _, generation string) {
			if err := os.RemoveAll(filepath.Join(generation, "apps")); err != nil {
				t.Fatal(err)
			}
		},
		"manifest": func(t *testing.T, _, generation string) {
			manifestPath := filepath.Join(generation, "apps", "fixture", "manifest.yaml")
			if err := os.WriteFile(manifestPath, []byte("not a manifest"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			offline := false
			core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
				if offline {
					return nil, errors.New("offline")
				}
				return registryResponse(registryRuntimeArchive(t, "1.0"))(request)
			})
			if _, err := core.Search(context.Background(), "fixture"); err != nil {
				t.Fatal(err)
			}
			current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
			if err != nil {
				t.Fatal(err)
			}
			generation := filepath.Join(core.syncer.CacheRoot, current)
			old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
			registryRuntimeSetCheckedAt(t, generation, old)
			corrupt(t, core.syncer.CacheRoot, generation)
			offline = true
			applications, err := core.Search(context.Background(), "fixture")
			if err == nil || applications != nil {
				t.Fatalf("corrupt offline Search() applications=%#v error=%v", applications, err)
			}
			if CodeOf(err) != CodeNetwork {
				t.Fatalf("corrupt offline Search() code=%q error=%v", CodeOf(err), err)
			}
			if *requests != 2 {
				t.Fatalf("registry requests = %d, want 2", *requests)
			}
		})
	}
}

func TestCatalogDoesNotHideCancellationBehindStaleCache(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	registryRuntimeSetCheckedAt(t, generation, old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := core.Search(ctx, "fixture"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context cancellation", err)
	}
}

func TestCatalogFailsOfflineWithoutCache(t *testing.T) {
	core, _ := registryRuntimeCore(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})
	if _, err := core.Search(context.Background(), "fixture"); err == nil {
		t.Fatal("Search() unexpectedly succeeded without a registry")
	}
}

func TestExplicitRegistrySyncAlwaysFetches(t *testing.T) {
	core, requests := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if _, err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests = %d, want 2", *requests)
	}
}

func TestRefreshRegistryPublishesImmediatelyAndRetainsCheckedAtOnFailure(t *testing.T) {
	payload := registryRuntimeArchive(t, "1.0")
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		if string(payload) == "invalid" {
			return registryResponse([]byte("invalid"))(request)
		}
		return registryResponse(payload)(request)
	})
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	core.now = func() time.Time { return clock }
	checkedAt, err := core.SyncRegistry(context.Background(), nil)
	if err != nil || !checkedAt.Equal(clock) {
		t.Fatalf("initial refresh checked-at=%s error=%v", checkedAt, err)
	}
	payload = registryRuntimeArchive(t, "2.0")
	clock = clock.Add(time.Hour)
	checkedAt, err = core.SyncRegistry(context.Background(), nil)
	if err != nil || !checkedAt.Equal(clock) {
		t.Fatalf("second refresh checked-at=%s error=%v", checkedAt, err)
	}
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("same-process Search() applications=%#v error=%v", applications, err)
	}
	payload = []byte("invalid")
	clock = clock.Add(time.Hour)
	if _, err := core.SyncRegistry(context.Background(), nil); err == nil {
		t.Fatal("invalid refresh unexpectedly succeeded")
	}
	catalog, err := registry.Open(core.syncer.CacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.FetchedAt.Equal(clock.Add(-time.Hour)) {
		t.Fatalf("failed refresh advanced checked-at to %s", catalog.FetchedAt)
	}
	applications, err = core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("failed refresh replaced previous catalog: applications=%#v error=%v", applications, err)
	}
	if *requests != 3 {
		t.Fatalf("registry requests=%d, want 3", *requests)
	}
}

func TestCatalogUsesCheckedAtForTwentyFourHourFreshness(t *testing.T) {
	payload := registryRuntimeArchive(t, "1.0")
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		return registryResponse(payload)(request)
	})
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	core.now = func() time.Time { return clock }
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(23 * time.Hour)
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("registry requests at 23 hours=%d, want 1", *requests)
	}
	payload = registryRuntimeArchive(t, "2.0")
	clock = clock.Add(time.Hour)
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("24-hour Search() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests at 24 hours=%d, want 2", *requests)
	}
}

func TestSearchUsesExactRuntimePlatformVariant(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchivePlatformVersions(t, "1.0", "2.0", true)))
	core.goarch = "arm64"
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].ID != "fixture" || applications[0].Name != "Fixture" || applications[0].Summary != "Registry fixture" || applications[0].Homepage != "https://example.com/" || len(applications[0].Categories) != 1 || applications[0].Categories[0] != "utilities" || applications[0].RegistryVersion != "1.0" {
		t.Fatalf("arm64 Search() applications=%#v error=%v", applications, err)
	}
	info, err := core.Info(context.Background(), "fixture")
	if err != nil || info.ID != "fixture" || info.Name != "Fixture" || info.Summary != "Registry fixture" || info.RegistryVersion != "1.0" {
		t.Fatalf("arm64 Info() application=%#v error=%v", info, err)
	}
	catalog, err := registry.Open(filepath.Join(core.layout.Cache, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.ManifestForPlatform("fixture", "linux", "arm64")
	if err != nil || item.Release.Version != "1.0" || item.Application.Executables[0].Name != "fixture" {
		t.Fatalf("arm64 manifest=%#v error=%v", item, err)
	}
}

func TestResolveMissingVariantReturnsTypedUnsupportedPlatform(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	core.goarch = "arm64"
	_, err := core.Info(context.Background(), "fixture")
	if CodeOf(err) != CodeUnsupportedPlatform {
		t.Fatalf("Info() code = %q, error = %v", CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "Fixture is not available for linux/arm64") {
		t.Fatalf("Info() error = %v", err)
	}
}

func TestListDoesNotClaimRegistryDataWithoutRuntimeVariant(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if _, err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(filepath.Join(core.layout.States, "fixture.json"), state.State{
		Schema: state.Schema, App: "fixture", Current: "0.9", CurrentFingerprint: testStateFingerprint, Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "fixture", Path: "fixture"}},
		Integration: state.Integration{
			Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Link: filepath.Join(core.layout.Bin, "fixture"), Target: filepath.Join(core.layout.Apps, "fixture", "current", "fixture")}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	core.goarch = "arm64"
	applications, err := core.List(context.Background())
	if err != nil || len(applications) != 1 || applications[0].ID != "fixture" || applications[0].Name != "fixture" || applications[0].InstalledVersion != "0.9" || applications[0].RegistryVersion != "" || applications[0].Summary != "" || applications[0].Homepage != "" || len(applications[0].Categories) != 0 || applications[0].UpdateAvailable {
		t.Fatalf("List() applications=%#v error=%v", applications, err)
	}
}

func TestListPrefersExactRuntimeVariant(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchivePlatformVersions(t, "1.0", "2.0", true)))
	if _, err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(filepath.Join(core.layout.States, "fixture.json"), state.State{
		Schema: state.Schema, App: "fixture", Current: "1.5", CurrentFingerprint: testStateFingerprint, Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "fixture", Path: "fixture"}},
		Integration: state.Integration{
			Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Link: filepath.Join(core.layout.Bin, "fixture"), Target: filepath.Join(core.layout.Apps, "fixture", "current", "fixture")}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	core.goarch = "arm64"
	applications, err := core.List(context.Background())
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "1.0" || !applications[0].UpdateAvailable {
		t.Fatalf("arm64 List() applications=%#v error=%v", applications, err)
	}
}

func TestListUsesSharedRegistryFreshnessPolicy(t *testing.T) {
	version := "1.0"
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		return registryResponse(registryRuntimeArchive(t, version))(request)
	})
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	registryRuntimeSetCheckedAt(t, generation, old)
	if err := state.Write(filepath.Join(core.layout.States, "fixture.json"), state.State{Schema: state.Schema, App: "fixture", Current: "0.9", CurrentFingerprint: testStateFingerprint, Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "fixture", Path: "fixture"}}, Integration: state.Integration{Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Link: filepath.Join(core.layout.Bin, "fixture"), Target: filepath.Join(core.layout.Apps, "fixture", "current", "fixture")}}}}); err != nil {
		t.Fatal(err)
	}
	version = "2.0"
	applications, err := core.List(context.Background())
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("List() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests=%d, want 2", *requests)
	}
}
