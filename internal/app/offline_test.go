package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/artifactrepo"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
)

func offlineDenyHTTP(core *Core) *int {
	calls := new(int)
	core.installer.Client.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		*calls++
		return nil, errors.New("unexpected offline HTTP")
	})
	core.EnableOffline()
	return calls
}

func TestOfflineLockPreflightsEntireSelectionThenReplaysExactPackages(t *testing.T) {
	ctx := context.Background()
	core, artifacts, _ := updatePlanFixture(t)
	path := filepath.Join(t.TempDir(), "tarlink.lock")
	if err := core.WriteLock(ctx, path); err != nil {
		t.Fatal(err)
	}
	before, err := core.installedStates()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha", "bravo", "charlie"} {
		if _, err := core.Uninstall(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	missingDigest := repositoryServiceDigest(artifacts["charlie"])
	if err := os.Remove(filepath.Join(core.layout.ArtifactCacheRoot(), "v1", "sha256", missingDigest)); err != nil {
		t.Fatal(err)
	}
	core.installer.Client.Sources = []string{"https://mirror.invalid"}
	calls := offlineDenyHTTP(core)
	_, err = core.InstallLock(ctx, path, true, nil)
	if err == nil || !strings.Contains(err.Error(), missingDigest) || !strings.Contains(err.Error(), "charlie") {
		t.Fatalf("missing later artifact: %v", err)
	}
	states, err := core.installedStates()
	if err != nil || len(states) != 0 {
		t.Fatalf("partial replay before preflight completed: %+v %v", states, err)
	}
	repository := t.TempDir()
	if err := artifactrepo.Init(repository); err != nil {
		t.Fatal(err)
	}
	if err := artifactrepo.Add(ctx, repository, "sha256", missingDigest, func(_ context.Context, _, _, _, destination string) error {
		return os.WriteFile(destination, artifacts["charlie"], 0o600)
	}, "fixture"); err != nil {
		t.Fatal(err)
	}
	core.installer.Client.Sources = append(core.installer.Client.Sources, repository)
	result, err := core.InstallLock(ctx, path, true, nil)
	if err != nil || len(result.Completed) != 3 || len(result.Failed) != 0 {
		t.Fatalf("replay=%+v %v", result, err)
	}
	after, err := core.installedStates()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("replay did not restore exact packages: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("offline HTTP=%d", *calls)
	}
}

func TestOfflineBatchMissingLaterArtifactDoesNotInstallEarlierApps(t *testing.T) {
	ctx := context.Background()
	artifacts := map[string][]byte{"alpha": updatePlanArtifact(t, "alpha", "alpha"), "bravo": updatePlanArtifact(t, "bravo", "bravo")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	if _, err := core.SyncRegistry(ctx, nil); err != nil {
		t.Fatal(err)
	}
	client := core.installer.Client
	client.ArtifactCache = core.layout.ArtifactCacheRoot()
	verified, err := client.AcquireVerified(ctx, download.VerifiedRequest{URL: "https://example.com/alpha.tar.gz", Algorithm: "sha256", Digest: repositoryServiceDigest(artifacts["alpha"])})
	if err != nil {
		t.Fatal(err)
	}
	if err := verified.Close(); err != nil {
		t.Fatal(err)
	}
	calls := offlineDenyHTTP(core)
	if _, err := core.InstallBatchWithOptions(ctx, []string{"alpha", "bravo"}, true, nil); err == nil {
		t.Fatal("incomplete batch succeeded")
	}
	states, err := core.installedStates()
	if err != nil || len(states) != 0 {
		t.Fatalf("earlier app installed: %+v %v", states, err)
	}
	if *calls != 0 {
		t.Fatalf("offline HTTP=%d", *calls)
	}
}

func TestOfflineCatalogRequiresValidMetadataAndNeverAdvancesFreshness(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"stale", "missing", "corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			core, _, _ := updatePlanFixture(t)
			catalogRoot := filepath.Join(core.layout.Cache, "registry")
			pointer := filepath.Join(catalogRoot, "current")
			generation, err := filepath.EvalSymlinks(pointer)
			if err != nil {
				t.Fatal(err)
			}
			offlineNow := time.Now().Add(48 * time.Hour)
			core.now = func() time.Time { return offlineNow }
			switch kind {
			case "missing":
				if err := os.RemoveAll(catalogRoot); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filepath.Join(generation, "apps", "alpha", "manifest.yaml"), []byte("schema: bogus"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(pointer); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), pointer); err != nil {
					t.Fatal(err)
				}
			}
			calls := offlineDenyHTTP(core)
			cached, readErr := registry.Open(catalogRoot)
			catalog, err := core.catalog(ctx, nil)
			if kind == "stale" {
				if err != nil || readErr != nil || !cached.Stale(core.now(), registry.DefaultMaxAge) || !catalog.FetchedAt.Equal(cached.FetchedAt) {
					t.Fatalf("stale catalog changed freshness: %+v %v", catalog, err)
				}
			} else if err == nil || CodeOf(err) != CodeRegistry {
				t.Fatalf("invalid cache accepted: %v", err)
			}
			if *calls != 0 {
				t.Fatalf("offline catalog HTTP=%d", *calls)
			}
		})
	}
}

func TestOfflineSuppressesAdvisoryRefreshAndSelfUpdateNetworking(t *testing.T) {
	layout := registryRuntimeLayout(t)
	calls := 0
	client := download.NewClient()
	client.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("unexpected HTTP") })
	core, err := NewCore(layout, client)
	if err != nil {
		t.Fatal(err)
	}
	core.upgrader.Current = "0.19.12"
	core.EnableOffline()
	ctx := context.Background()
	for _, check := range []func(context.Context) (TarLinkVersion, error){core.CheckTarLinkVersion, core.CheckTarLinkVersionFresh} {
		version, err := check(ctx)
		if err != nil || version.UpgradeAvailable || version.Latest != "" {
			t.Fatalf("offline advisory=%+v %v", version, err)
		}
	}
	if _, err := core.SyncRegistry(ctx, nil); !errors.Is(err, download.ErrOffline) {
		t.Fatalf("offline refresh=%v", err)
	}
	if _, err := core.UpgradeTarLink(ctx, nil); !errors.Is(err, download.ErrOffline) {
		t.Fatalf("offline self-update=%v", err)
	}
	if calls != 0 || !core.upgrader.Client.Offline {
		t.Fatalf("offline did not suppress release client: calls=%d", calls)
	}
}

func TestOfflineLockRejectsAllChangedMetadataBeforeMutation(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	path := filepath.Join(t.TempDir(), "tarlink.lock")
	if err := core.WriteLock(ctx, path); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha", "bravo", "charlie"} {
		if _, err := core.Uninstall(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	artifacts["alpha"] = updatePlanArtifact(t, "alpha", "new unapproved-for-lock bytes")
	artifacts["charlie"] = updatePlanArtifact(t, "charlie", "also changed")
	updatePlanRegistry(t, core, artifacts, requests, nil)
	calls := offlineDenyHTTP(core)
	_, err := core.InstallLock(ctx, path, true, nil)
	if err == nil || !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "charlie") {
		t.Fatalf("metadata failures not aggregated: %v", err)
	}
	states, err := core.installedStates()
	if err != nil || len(states) != 0 {
		t.Fatalf("metadata failure changed installed state: %+v %v", states, err)
	}
	if *calls != 0 {
		t.Fatalf("offline HTTP=%d", *calls)
	}
}

func TestOfflineRepairStillRequiresApprovedInstalledIdentity(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	before, err := state.LoadForApp(core.layout, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	artifacts["alpha"] = updatePlanArtifact(t, "alpha", "changed approved package")
	updatePlanRegistry(t, core, artifacts, requests, nil)
	calls := offlineDenyHTTP(core)
	if _, err := core.Repair(ctx, "alpha", nil); err == nil {
		t.Fatal("offline repair accepted changed identity")
	}
	after, err := state.LoadForApp(core.layout, "alpha")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("refused repair changed state")
	}
	if *calls != 0 {
		t.Fatalf("offline HTTP=%d", *calls)
	}
}

func TestOfflineLockContinuesAfterIndependentPublicationConflict(t *testing.T) {
	ctx := context.Background()
	core, _, _ := updatePlanFixture(t)
	lockPath := filepath.Join(t.TempDir(), "tarlink.lock")
	if err := core.WriteLock(ctx, lockPath); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha", "bravo", "charlie"} {
		if _, err := core.Uninstall(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	conflict := filepath.Join(core.layout.Bin, "alpha")
	if err := os.WriteFile(conflict, []byte("unrelated user executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := offlineDenyHTTP(core)
	result, err := core.InstallLock(ctx, lockPath, true, nil)
	if err != nil || len(result.Completed) != 2 || len(result.Failed) != 1 || result.FailureCodes["alpha"] != CodeConflict {
		t.Fatalf("independent conflict stopped later entries: %+v %v", result, err)
	}
	body, err := os.ReadFile(conflict)
	if err != nil || string(body) != "unrelated user executable" {
		t.Fatal("conflict overwrote user file")
	}
	if *calls != 0 {
		t.Fatalf("offline HTTP=%d", *calls)
	}
}
