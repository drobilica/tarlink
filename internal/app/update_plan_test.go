package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
)

func updatePlanArtifact(t *testing.T, id, body string) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: id, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func updatePlanFixture(t *testing.T) (*Core, map[string][]byte, *[]string) {
	t.Helper()
	ctx := context.Background()
	artifacts := map[string][]byte{}
	for _, id := range []string{"alpha", "bravo", "charlie"} {
		artifacts[id] = updatePlanArtifact(t, id, "old "+id)
	}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	for _, id := range []string{"alpha", "bravo", "charlie"} {
		if _, err := core.Install(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	requests := []string{}
	return core, artifacts, &requests
}

func updatePlanRegistry(t *testing.T, core *Core, artifacts map[string][]byte, requests *[]string, archive []byte) {
	t.Helper()
	if archive == nil {
		archive = repositoryServiceArchive(t, artifacts)
	}
	core.syncer.Client.HTTP.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		*requests = append(*requests, request.URL.String())
		payload := archive
		if request.URL.Host == "example.com" {
			payload = artifacts[strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/"), ".tar.gz")]
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), Header: make(http.Header), Request: request, ContentLength: int64(len(payload))}, nil
	})
	if _, err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	*requests = nil
}

func TestUpdatePlanPreviewAndApplySameVersionPackageChange(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	if err := core.Pin(ctx, "bravo"); err != nil {
		t.Fatal(err)
	}
	before, err := core.installedStates()
	if err != nil {
		t.Fatal(err)
	}
	artifacts["alpha"] = updatePlanArtifact(t, "alpha", "new alpha")
	artifacts["bravo"] = updatePlanArtifact(t, "bravo", "new bravo")
	updatePlanRegistry(t, core, artifacts, requests, nil)
	plan, err := core.PlanUpdates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Schema != 1 || plan.Platform != "linux/amd64" || plan.RegistryRevision == "" || len(plan.Applications) != 3 {
		t.Fatalf("plan=%+v", plan)
	}
	for i, want := range []string{"update", "pinned", "current"} {
		if plan.Applications[i].Status != want {
			t.Fatalf("entry=%+v want %s", plan.Applications[i], want)
		}
	}
	alpha := plan.Applications[0]
	if alpha.Current.Version != alpha.Target.Version || !alpha.PackageChanged || alpha.RuntimeChanged || alpha.Rollback != alpha.Current {
		t.Fatalf("same-version package delta=%+v", alpha)
	}
	after, err := core.installedStates()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed state: %v", err)
	}
	if len(*requests) != 0 {
		t.Fatalf("preview acquired artifacts: %v", *requests)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "snapshot") || strings.Contains(string(encoded), "https:") {
		t.Fatalf("plan exposed acquisition inputs: %s", encoded)
	}
	result, err := core.ApplyUpdatePlan(ctx, plan, nil)
	if err != nil || len(result.Updated) != 1 || len(result.Skipped) != 2 || len(result.Pinned) != 1 || len(result.Failed) != 0 {
		t.Fatalf("apply=%+v %v", result, err)
	}
	if len(*requests) != 1 || !strings.Contains((*requests)[0], "alpha.tar.gz") {
		t.Fatalf("acquisitions=%v", *requests)
	}
	current, err := state.LoadForApp(core.layout, "alpha")
	if err != nil || current.CurrentFingerprint != alpha.Target.Fingerprint || current.PreviousFingerprint != alpha.Current.Fingerprint {
		t.Fatalf("retention=%+v %v", current, err)
	}
	content, err := os.ReadFile(filepath.Join(core.layout.Apps, "alpha", "current", "alpha"))
	if err != nil || string(content) != "new alpha" {
		t.Fatalf("current=%q %v", content, err)
	}
	if _, err := core.Rollback(ctx, "alpha", nil); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(filepath.Join(core.layout.Apps, "alpha", "current", "alpha"))
	if err != nil || string(content) != "old alpha" {
		t.Fatalf("rollback=%q %v", content, err)
	}
}

func TestUpdatePlanRejectsStaleOrModifiedReviewBeforeAcquisition(t *testing.T) {
	for _, change := range []string{"pin", "registry-target", "new-installed", "modified-plan", "foreign-plan", "cancel"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			core, artifacts, requests := updatePlanFixture(t)
			for _, id := range []string{"alpha", "bravo"} {
				artifacts[id] = updatePlanArtifact(t, id, "new "+id)
			}
			updatePlanRegistry(t, core, artifacts, requests, nil)
			plan, err := core.PlanUpdates(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "pin":
				if err := core.Pin(ctx, "bravo"); err != nil {
					t.Fatal(err)
				}
			case "registry-target":
				artifacts["bravo"] = updatePlanArtifact(t, "bravo", "changed after review")
				updatePlanRegistry(t, core, artifacts, requests, nil)
			case "new-installed":
				artifacts["delta"] = updatePlanArtifact(t, "delta", "new delta")
				updatePlanRegistry(t, core, artifacts, requests, nil)
				if _, err := core.Install(ctx, "delta", nil); err != nil {
					t.Fatal(err)
				}
				*requests = nil
			case "modified-plan":
				plan.Applications[0].Target.Version = "unreviewed"
			case "foreign-plan":
				plan.snapshot = nil
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, err := core.installedStates()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := core.ApplyUpdatePlan(ctx, plan, nil); err == nil {
				t.Fatal("accepted stale or modified plan")
			}
			after, err := core.installedStates()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("stale plan changed state: %v", err)
			}
			if len(*requests) != 0 {
				t.Fatalf("stale plan acquired artifacts: %v", *requests)
			}
		})
	}
}

func TestUpdatePlanResolutionAndIndependentAcquisitionFailure(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	artifacts["alpha"] = updatePlanArtifact(t, "alpha", "new alpha")
	artifacts["bravo"] = updatePlanArtifact(t, "bravo", "new bravo")
	updatePlanRegistry(t, core, artifacts, requests, nil)
	plan, err := core.PlanUpdates(ctx, []string{"missing", "bravo", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applications[2].Status != "error" || plan.Applications[2].Code != CodeNotInstalled {
		t.Fatalf("missing=%+v", plan)
	}
	artifacts["alpha"] = []byte("hostile substituted bytes")
	result, err := core.ApplyUpdatePlan(ctx, plan, nil)
	if err != nil || len(result.Failed) != 2 || len(result.Updated) != 1 || result.Updated[0].AppID != "bravo" {
		t.Fatalf("partial=%+v %v", result, err)
	}
	if result.FailureCodes["alpha"] != CodeChecksum {
		t.Fatalf("failure codes=%+v", result.FailureCodes)
	}
}

func TestUpdatePlanExplicitPinnedTargetAndInvalidSelection(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	if err := core.Pin(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	artifacts["alpha"] = updatePlanArtifact(t, "alpha", "new alpha")
	updatePlanRegistry(t, core, artifacts, requests, nil)
	plan, err := core.PlanUpdates(ctx, []string{"alpha@stable"})
	if err != nil || plan.Applications[0].Status != "update" {
		t.Fatalf("explicit pinned plan=%+v %v", plan, err)
	}
	result, err := core.ApplyUpdatePlan(ctx, plan, nil)
	if err != nil || len(result.Updated) != 1 || !result.Updated[0].Pinned {
		t.Fatalf("explicit pinned apply=%+v %v", result, err)
	}
	for _, ids := range [][]string{{"../unsafe"}, {"alpha", "alpha@stable"}} {
		if _, err := core.PlanUpdates(ctx, ids); CodeOf(err) != CodeInvalidArguments {
			t.Fatalf("selection %v: %v", ids, err)
		}
	}
}

func TestUpdatePlanRuntimeChangeWithoutAcquisition(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	// Extend the trusted fixture tree with an exact runtime reference. No runtime
	// artifact bytes are needed or served while planning.
	source := repositoryServiceArchive(t, artifacts)
	reader, err := gzip.NewReader(bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tr := tar.NewReader(reader)
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(header.Name, "apps/alpha/manifest.yaml") {
			body = bytes.Replace(body, []byte("    - version: \"1.0\"\n"), []byte("    - version: \"1.0\"\n      runtime:\n        id: steam-linux-runtime-4\n        version: \"1.0\"\n"), 1)
			body = bytes.Replace(body, []byte("    path: alpha\n"), []byte("    path: alpha\n    create-bin-link: false\n"), 1)
			header.Size = int64(len(body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	runtime := `schema: 1
id: steam-linux-runtime-4
kind: steam-linux-runtime
version: "1.0"
platform: linux-amd64
artifact:
  url: https://example.com/runtime.tar.xz
  archive: tar.xz
  verification:
    algorithm: sha256
    digest: ` + strings.Repeat("a", 64) + `
    source: https://example.com/releases
interface: valve-v2-entry-point-v1
`
	for _, entry := range []struct {
		name, body string
		directory  bool
	}{
		{name: "tarlink-registry-main/runtimes/", directory: true},
		{name: "tarlink-registry-main/runtimes/steam-linux-runtime-4/", directory: true},
		{name: "tarlink-registry-main/runtimes/steam-linux-runtime-4/manifest.yaml", body: runtime},
	} {
		header := tar.Header{Name: entry.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(entry.body))}
		if entry.directory {
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
		}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	updatePlanRegistry(t, core, artifacts, requests, output.Bytes())
	plan, err := core.PlanUpdates(ctx, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	entry := plan.Applications[0]
	if entry.Status != "update" || !entry.RuntimeChanged || !entry.PackageChanged || entry.Current.Runtime != nil || entry.Target.Runtime == nil || entry.Target.Runtime.ID != "steam-linux-runtime-4" || entry.Rollback.Runtime != nil {
		t.Fatalf("runtime delta=%+v", entry)
	}
	if len(*requests) != 0 {
		t.Fatalf("planning downloaded runtime: %v", *requests)
	}
	// Public runtime objects must not alias the private review snapshot.
	entry.Target.Runtime.Version = "tampered"
	if _, err := core.ApplyUpdatePlan(ctx, plan, nil); CodeOf(err) != CodeInvalidArguments {
		t.Fatalf("runtime plan tampering: %v", err)
	}
}

func TestEmptyUpdatePlanDoesNotBootstrapRegistry(t *testing.T) {
	core, requests := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	plan, err := core.PlanUpdates(context.Background(), nil)
	if err != nil || len(plan.Applications) != 0 || *requests != 0 {
		t.Fatalf("empty plan=%+v requests=%d err=%v", plan, *requests, err)
	}
	result, err := core.ApplyUpdatePlan(context.Background(), plan, nil)
	if err != nil || len(result.Updated) != 0 || *requests != 0 {
		t.Fatalf("empty apply=%+v requests=%d err=%v", result, *requests, err)
	}
}

func TestUpdatePlanReportsCatalogFailureAndPreservesPinnedSkips(t *testing.T) {
	ctx := context.Background()
	core, _, _ := updatePlanFixture(t)
	if err := core.Pin(ctx, "bravo"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(core.layout.Cache, "registry")); err != nil {
		t.Fatal(err)
	}
	core.syncer.Client.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
	plan, err := core.PlanUpdates(ctx, nil)
	if err != nil || len(plan.Applications) != 3 {
		t.Fatalf("catalog error plan=%+v %v", plan, err)
	}
	for i, want := range []string{"error", "pinned", "error"} {
		if plan.Applications[i].Status != want {
			t.Fatalf("entry=%+v", plan.Applications[i])
		}
	}
	if plan.RegistryRevision != "" {
		t.Fatal("failed catalog claimed a revision")
	}
	result, err := core.ApplyUpdatePlan(ctx, plan, nil)
	if err != nil || len(result.Failed) != 2 || len(result.Pinned) != 1 || len(result.Updated) != 0 {
		t.Fatalf("catalog error outcomes=%+v %v", result, err)
	}
}

func TestSingleUpdatePlanDoesNotReadUnrelatedCorruptState(t *testing.T) {
	ctx := context.Background()
	core, artifacts, requests := updatePlanFixture(t)
	artifacts["alpha"] = updatePlanArtifact(t, "alpha", "new alpha")
	updatePlanRegistry(t, core, artifacts, requests, nil)
	unrelated, _ := core.layout.StatePath("bravo")
	if err := os.WriteFile(unrelated, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := core.PlanUpdates(ctx, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := core.ApplyUpdatePlan(ctx, plan, nil)
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("unrelated state blocked selected update: %+v %v", result, err)
	}
}

func TestUpdatePlanResolutionFollowsInstalledChannelAndExactPlatform(t *testing.T) {
	core, _, _ := updatePlanFixture(t)
	catalog, err := core.catalog(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	item := catalog.Variants["alpha"][manifest.Platform{OS: "linux", Arch: "amd64"}]
	beta := item.Release
	beta.Channel, beta.Version = "beta", "2.0-beta"
	item.ReleaseHistory.Releases = append(item.ReleaseHistory.Releases, beta)
	item.ReleaseHistory.Channels["beta"] = manifest.ChannelHead{Current: beta.Version}
	resolved, err := core.resolvePlannedUpdate(catalog, Selector{App: "alpha"}, state.State{Channel: "beta"})
	if err != nil || resolved.Release.Channel != "beta" || resolved.Release.Version != "2.0-beta" {
		t.Fatalf("installed channel resolution=%+v %v", resolved, err)
	}
	resolved, err = core.resolvePlannedUpdate(catalog, Selector{App: "alpha", Target: "stable"}, state.State{Channel: "beta"})
	if err != nil || resolved.Release.Channel != "stable" {
		t.Fatalf("explicit channel resolution=%+v %v", resolved, err)
	}
	core.goarch = "arm64"
	if _, err := core.resolvePlannedUpdate(catalog, Selector{App: "alpha"}, state.State{Channel: "stable"}); CodeOf(err) != CodeUnsupportedPlatform {
		t.Fatalf("platform error=%v", err)
	}
}
