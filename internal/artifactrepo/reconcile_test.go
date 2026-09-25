package artifactrepo

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
)

func contentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmtDigest(sum[:])
}

func planRelease(version, url, digest string) manifest.Release {
	return manifest.Release{Version: version, URL: url, Verification: manifest.Verification{Algorithm: "sha256", Digest: digest}}
}

func planRuntimeRelease(version, url, digest, runtimeURL, runtimeDigest string) manifest.Release {
	release := planRelease(version, url, digest)
	release.Runtime = &manifest.Runtime{
		ID: "steam", Version: "1",
		Artifact: manifest.RuntimeArtifact{URL: runtimeURL, Verification: manifest.Verification{Algorithm: "sha256", Digest: runtimeDigest}},
	}
	return release
}

type planContents struct {
	byName  map[string]string
	digests map[string]string
	byURL   map[string]string
}

func fixtureURL(name string) string { return "https://example.test/" + name + ".bin" }

func fixtureContents() *planContents {
	byName := map[string]string{
		"alpha-amd64-v1": "alpha amd64 v1 bytes",
		"alpha-amd64-v2": "alpha amd64 v2 bytes",
		"alpha-amd64-v3": "alpha amd64 v3 bytes",
		"alpha-arm64-v1": "alpha arm64 v1 bytes",
		"alpha-arm64-v2": "alpha arm64 v2 bytes",
		"alpha-icon":     "alpha icon bytes",
		"runtime-v1":     "runtime v1 bytes",
		"beta-amd64-v1":  "beta amd64 v1 bytes",
		"gamma-arm64-v1": "gamma arm64 v1 bytes",
	}
	contents := &planContents{byName: byName, digests: map[string]string{}, byURL: map[string]string{}}
	for name, body := range byName {
		contents.digests[name] = contentDigest(body)
		contents.byURL[fixtureURL(name)] = body
	}
	contents.byURL[fixtureURL("beta-amd64-v2")] = byName["alpha-amd64-v2"]
	return contents
}

func fixtureCatalog(contents *planContents) *registry.Catalog {
	digests := contents.digests
	alphaAMD64 := []manifest.Release{
		planRelease("1", fixtureURL("alpha-amd64-v1"), digests["alpha-amd64-v1"]),
		planRuntimeRelease("2", fixtureURL("alpha-amd64-v2"), digests["alpha-amd64-v2"], fixtureURL("runtime-v1"), digests["runtime-v1"]),
		planRelease("3", fixtureURL("alpha-amd64-v3"), digests["alpha-amd64-v3"]),
	}
	alphaARM64 := []manifest.Release{
		planRelease("1", fixtureURL("alpha-arm64-v1"), digests["alpha-arm64-v1"]),
		planRuntimeRelease("2", fixtureURL("alpha-arm64-v2"), digests["alpha-arm64-v2"], fixtureURL("runtime-v1"), digests["runtime-v1"]),
	}
	betaAMD64 := []manifest.Release{
		planRelease("1", fixtureURL("beta-amd64-v1"), digests["beta-amd64-v1"]),
		{Version: "2", URL: fixtureURL("beta-amd64-v2"), Verification: manifest.Verification{Algorithm: "sha256", Digest: digests["alpha-amd64-v2"]}},
	}
	gammaARM64 := []manifest.Release{
		planRelease("1", fixtureURL("gamma-arm64-v1"), digests["gamma-arm64-v1"]),
	}
	newVariant := func(releases []manifest.Release, icon manifest.DesktopIcon) *manifest.Manifest {
		return &manifest.Manifest{ID: "fixture", ReleaseHistory: manifest.ReleaseHistory{Releases: releases}, Desktop: manifest.Desktop{Icon: icon}}
	}
	remoteIcon := manifest.DesktopIcon{URL: fixtureURL("alpha-icon"), SHA256: digests["alpha-icon"]}
	return &registry.Catalog{Revision: "fixture-rev-1", Variants: map[string]map[manifest.Platform]*manifest.Manifest{
		"alpha": {
			{OS: "linux", Arch: "amd64"}: newVariant(alphaAMD64, remoteIcon),
			{OS: "linux", Arch: "arm64"}: newVariant(alphaARM64, remoteIcon),
		},
		"beta": {
			{OS: "linux", Arch: "amd64"}: newVariant(betaAMD64, manifest.DesktopIcon{Path: "icon.png"}),
		},
		"gamma": {
			{OS: "linux", Arch: "arm64"}: newVariant(gammaARM64, manifest.DesktopIcon{}),
		},
	}}
}

type recordingFetch struct {
	contents map[string]string
	calls    []string
	fail     map[string]error
	onCall   func(url string)
}

func (f *recordingFetch) fetch(_ context.Context, url, _, _ string, destination string) error {
	f.calls = append(f.calls, url)
	if f.onCall != nil {
		f.onCall(url)
	}
	if err, ok := f.fail[url]; ok {
		return err
	}
	body, ok := f.contents[url]
	if !ok {
		return errors.New("unexpected fetch " + url)
	}
	return os.WriteFile(destination, []byte(body), 0600)
}

func writeRepoObject(t *testing.T, root, algorithm, digest, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "v1", algorithm, digest), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func repoObjectExists(root, algorithm, digest string) bool {
	info, err := os.Lstat(filepath.Join(root, "v1", algorithm, digest))
	return err == nil && info.Mode().IsRegular()
}

func readRepoObject(t *testing.T, root, algorithm, digest string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "v1", algorithm, digest))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func desiredDigests(plan *Plan) []string {
	keys := make([]string, 0, len(plan.Desired))
	for _, object := range plan.Desired {
		keys = append(keys, object.Algorithm+":"+object.Digest)
	}
	sort.Strings(keys)
	return keys
}

func statusByDigest(statuses []Status) map[string]Status {
	result := map[string]Status{}
	for _, status := range statuses {
		result[status.Algorithm+":"+status.Digest] = status
	}
	return result
}

func retainedByDigest(objects []RetainedObject) map[string]RetainedObject {
	result := map[string]RetainedObject{}
	for _, object := range objects {
		result[object.Algorithm+":"+object.Digest] = object
	}
	return result
}

func mustBuildPlan(t *testing.T, catalog *registry.Catalog, selection Selection) *Plan {
	t.Helper()
	plan, err := BuildPlan(catalog, selection)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func populateRepo(t *testing.T, ctx context.Context, root string, plan *Plan, fetch Fetch) ReconcileResult {
	t.Helper()
	result, err := Reconcile(ctx, root, plan, fetch)
	if err != nil {
		t.Fatalf("populate Reconcile() error = %v", err)
	}
	return result
}

func TestBuildPlanDefaultSelectsAllAppsAndPlatforms(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{})
	digests := contents.digests
	want := []string{
		"sha256:" + digests["alpha-amd64-v1"],
		"sha256:" + digests["alpha-amd64-v2"],
		"sha256:" + digests["alpha-amd64-v3"],
		"sha256:" + digests["alpha-arm64-v1"],
		"sha256:" + digests["alpha-arm64-v2"],
		"sha256:" + digests["alpha-icon"],
		"sha256:" + digests["runtime-v1"],
		"sha256:" + digests["beta-amd64-v1"],
		"sha256:" + digests["gamma-arm64-v1"],
	}
	sort.Strings(want)
	if got := desiredDigests(plan); len(got) != len(want) {
		t.Fatalf("desired=%v want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("desired=%v want %v", got, want)
			}
		}
	}
	if len(plan.Unsupported) != 0 {
		t.Fatalf("unsupported=%v", plan.Unsupported)
	}
	if len(plan.Complete) != len(plan.Desired) {
		t.Fatalf("complete=%d desired=%d", len(plan.Complete), len(plan.Desired))
	}
}

func TestBuildPlanNarrowsByAppAndPlatform(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "alpha", Platform: "linux-amd64"})
	digests := contents.digests
	want := []string{
		"sha256:" + digests["alpha-amd64-v1"],
		"sha256:" + digests["alpha-amd64-v2"],
		"sha256:" + digests["alpha-amd64-v3"],
		"sha256:" + digests["alpha-icon"],
		"sha256:" + digests["runtime-v1"],
	}
	sort.Strings(want)
	if got := desiredDigests(plan); len(got) != len(want) {
		t.Fatalf("desired=%v want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("desired=%v want %v", got, want)
			}
		}
	}
	byDigest := map[string]Object{}
	for _, object := range plan.Desired {
		byDigest[object.Digest] = object
	}
	if urls := byDigest[digests["alpha-icon"]].URLs; len(urls) != 1 || urls[0] != fixtureURL("alpha-icon") {
		t.Fatalf("icon urls=%v", urls)
	}
}

func TestBuildPlanSparsePlatformExcludesMissingRelease(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "alpha", Platform: "linux-arm64"})
	for _, key := range desiredDigests(plan) {
		if key == "sha256:"+contents.digests["alpha-amd64-v3"] {
			t.Fatalf("amd64-only release selected for arm64: %v", desiredDigests(plan))
		}
	}
	if len(plan.Desired) != 4 {
		t.Fatalf("desired=%v", desiredDigests(plan))
	}
}

func TestBuildPlanPlatformOnlyReportsUnsupported(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{Platform: "linux-amd64"})
	if len(plan.Unsupported) != 1 || plan.Unsupported[0] != "gamma" {
		t.Fatalf("unsupported=%v", plan.Unsupported)
	}
	for _, key := range desiredDigests(plan) {
		if key == "sha256:"+contents.digests["gamma-arm64-v1"] {
			t.Fatalf("gamma selected for amd64: %v", desiredDigests(plan))
		}
	}
}

func TestBuildPlanMultiChannelUnion(t *testing.T) {
	digestOne := contentDigest("stable bytes")
	digestTwo := contentDigest("nightly bytes")
	catalog := &registry.Catalog{Revision: "rev", Variants: map[string]map[manifest.Platform]*manifest.Manifest{
		"delta": {
			{OS: "linux", Arch: "amd64"}: {
				ID: "delta",
				ReleaseHistory: manifest.ReleaseHistory{
					DefaultChannel: "stable",
					Channels:       map[string]manifest.ChannelHead{"stable": {Current: "1"}, "nightly": {Current: "n1"}},
					Releases: []manifest.Release{
						{Channel: "stable", Version: "1", URL: "https://example.test/stable.bin", Verification: manifest.Verification{Algorithm: "sha256", Digest: digestOne}},
						{Channel: "nightly", Version: "n1", URL: "https://example.test/nightly.bin", Verification: manifest.Verification{Algorithm: "sha256", Digest: digestTwo}},
					},
				},
			},
		},
	}}
	plan := mustBuildPlan(t, catalog, Selection{App: "delta"})
	if len(plan.Desired) != 2 {
		t.Fatalf("desired=%v", desiredDigests(plan))
	}
}

func TestBuildPlanInputErrors(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	for _, test := range []struct {
		name      string
		selection Selection
		want      string
	}{
		{name: "unknown app", selection: Selection{App: "missing"}, want: "unknown application"},
		{name: "unsupported app platform", selection: Selection{App: "beta", Platform: "linux-arm64"}, want: "has no"},
		{name: "bad platform", selection: Selection{Platform: "windows-amd64"}, want: "unsupported platform"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildPlan(catalog, test.selection); err == nil {
				t.Fatal("expected input error")
			} else if got := err.Error(); !contains(got, test.want) {
				t.Fatalf("error=%q want substring %q", got, test.want)
			}
		})
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

func TestBuildPlanZeroSelectionErrors(t *testing.T) {
	full := fixtureCatalog(fixtureContents())
	catalog := &registry.Catalog{Revision: "rev", Variants: map[string]map[manifest.Platform]*manifest.Manifest{
		"gamma": full.Variants["gamma"],
	}}
	if _, err := BuildPlan(catalog, Selection{Platform: "linux-amd64"}); err == nil {
		t.Fatal("expected zero-selection error")
	} else if !contains(err.Error(), "no retained releases") {
		t.Fatalf("error=%v", err)
	}
}

func TestBuildPlanRejectsConflictingURLMetadata(t *testing.T) {
	digestOne := contentDigest("first bytes")
	digestTwo := contentDigest("second bytes")
	catalog := &registry.Catalog{Revision: "rev", Variants: map[string]map[manifest.Platform]*manifest.Manifest{
		"one": {
			{OS: "linux", Arch: "amd64"}: {
				ID:             "one",
				ReleaseHistory: manifest.ReleaseHistory{Releases: []manifest.Release{planRelease("1", "https://example.test/same.bin", digestOne)}},
			},
		},
		"two": {
			{OS: "linux", Arch: "amd64"}: {
				ID:             "two",
				ReleaseHistory: manifest.ReleaseHistory{Releases: []manifest.Release{planRelease("1", "https://example.test/same.bin", digestTwo)}},
			},
		},
	}}
	if _, err := BuildPlan(catalog, Selection{}); err == nil {
		t.Fatal("expected conflicting metadata error")
	} else if !contains(err.Error(), "conflicting repository metadata") {
		t.Fatalf("error=%v", err)
	}
}

func TestClosureIncludesRuntimeAndRemoteIcon(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "alpha", Platform: "linux-amd64"})
	byDigest := statusByDigestForObjects(plan.Desired)
	for _, key := range []string{contents.digests["runtime-v1"], contents.digests["alpha-icon"]} {
		if _, ok := byDigest[key]; !ok {
			t.Fatalf("closure missing %s in %v", key, desiredDigests(plan))
		}
	}
}

func statusByDigestForObjects(objects []Object) map[string]Object {
	result := map[string]Object{}
	for _, object := range objects {
		result[object.Digest] = object
	}
	return result
}

func TestSharedDigestMergesURLAlternativesWithOrderedFallback(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{})
	byDigest := statusByDigestForObjects(plan.Desired)
	shared, ok := byDigest[contents.digests["alpha-amd64-v2"]]
	if !ok {
		t.Fatalf("shared digest missing in %v", desiredDigests(plan))
	}
	if len(shared.URLs) != 2 || shared.URLs[0] != fixtureURL("alpha-amd64-v2") || shared.URLs[1] != fixtureURL("beta-amd64-v2") {
		t.Fatalf("shared urls=%v", shared.URLs)
	}
	root := t.TempDir()
	fetch := &recordingFetch{contents: contents.byURL, fail: map[string]error{fixtureURL("alpha-amd64-v2"): errors.New("first source failed")}}
	narrowed, err := BuildPlan(fixtureCatalog(contents), Selection{App: "beta", Platform: "linux-amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(context.Background(), root, narrowed, fetch.fetch); err != nil {
		t.Fatal(err)
	}
	if got := readRepoObject(t, root, "sha256", contents.digests["alpha-amd64-v2"]); got != contents.byName["alpha-amd64-v2"] {
		t.Fatalf("shared object=%q", got)
	}
	var sharedCalls []string
	for _, call := range fetch.calls {
		if call == fixtureURL("alpha-amd64-v2") || call == fixtureURL("beta-amd64-v2") {
			sharedCalls = append(sharedCalls, call)
		}
	}
	if len(sharedCalls) != 2 || sharedCalls[0] != fixtureURL("alpha-amd64-v2") || sharedCalls[1] != fixtureURL("beta-amd64-v2") {
		t.Fatalf("fallback order=%v (all calls=%v)", sharedCalls, fetch.calls)
	}
}

func TestArchiveContainedIconProducesNoObject(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "beta"})
	if len(plan.Desired) != 2 {
		t.Fatalf("desired=%v", desiredDigests(plan))
	}
}

func TestInitEnsuresSyncLockAndHealsLegacy(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".sync.lock")
	if info, err := os.Lstat(lock); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("lock missing after Init: %v", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(lock); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("lock missing after heal: %v", err)
	}
	if err := Open(root); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileDownloadsMissingAtomically(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	fetch := &recordingFetch{contents: contents.byURL}
	result, err := Reconcile(context.Background(), root, plan, fetch.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Required != 1 || result.Downloaded != 1 || result.Unchanged != 0 || result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", contents.digests["gamma-arm64-v1"]); got != contents.byName["gamma-arm64-v1"] {
		t.Fatalf("object=%q", got)
	}
	if info, err := os.Stat(filepath.Join(root, "v1", "sha256", contents.digests["gamma-arm64-v1"])); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("object mode=%v err=%v", info, err)
	}
}

func TestReconcileRepairsCorruptAtomically(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	writeRepoObject(t, root, "sha256", contents.digests["gamma-arm64-v1"], "corrupt bytes")
	fetch := &recordingFetch{contents: contents.byURL}
	result, err := Reconcile(context.Background(), root, plan, fetch.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repaired != 1 || result.Corrupt != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", contents.digests["gamma-arm64-v1"]); got != contents.byName["gamma-arm64-v1"] {
		t.Fatalf("repaired object=%q", got)
	}
}

func TestSecondRunIsNoop(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{})
	root := t.TempDir()
	firstFetch := &recordingFetch{contents: contents.byURL}
	populateRepo(t, context.Background(), root, plan, firstFetch.fetch)
	before := map[string]string{}
	for _, object := range plan.Desired {
		before[object.Algorithm+":"+object.Digest] = readRepoObject(t, root, object.Algorithm, object.Digest)
	}
	descriptor := filepath.Join(root, "repository.json")
	beforeDescriptor, err := os.ReadFile(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Lstat(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	secondFetch := &recordingFetch{contents: contents.byURL}
	result, err := Reconcile(context.Background(), root, plan, secondFetch.fetch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded != 0 || result.Repaired != 0 || result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if result.Unchanged != result.Required {
		t.Fatalf("result=%+v", result)
	}
	if result.Retained != 0 || len(result.RetainedObjects) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(secondFetch.calls) != 0 {
		t.Fatalf("fetch calls=%v", secondFetch.calls)
	}
	for _, object := range plan.Desired {
		if got := readRepoObject(t, root, object.Algorithm, object.Digest); got != before[object.Algorithm+":"+object.Digest] {
			t.Fatalf("object %s:%s mutated by second run", object.Algorithm, object.Digest)
		}
	}
	afterDescriptor, err := os.ReadFile(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterDescriptor) != string(beforeDescriptor) {
		t.Fatalf("repository.json rewritten: %q", afterDescriptor)
	}
	afterInfo, err := os.Lstat(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("repository.json metadata rewritten by second run")
	}
}

func TestNarrowedAppSyncPreservesOtherApp(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	full := mustBuildPlan(t, catalog, Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	narrowed := mustBuildPlan(t, catalog, Selection{App: "alpha"})
	result, err := Reconcile(context.Background(), root, narrowed, (&recordingFetch{contents: contents.byURL}).fetch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	for _, key := range []string{contents.digests["beta-amd64-v1"], contents.digests["alpha-amd64-v2"], orphan} {
		if !repoObjectExists(root, "sha256", key) {
			t.Fatalf("object %s was removed", key)
		}
		if got := readRepoObject(t, root, "sha256", key); key == orphan && got != "orphan bytes" {
			t.Fatalf("orphan object=%q", got)
		}
	}
	retained := retainedByDigest(result.RetainedObjects)
	if retained["sha256:"+contents.digests["beta-amd64-v1"]].State != StateRetained {
		t.Fatalf("retained=%v", result.RetainedObjects)
	}
	if retained["sha256:"+orphan].State != StateRetained {
		t.Fatalf("retained=%v", result.RetainedObjects)
	}
	if result.Retained != len(result.RetainedObjects) {
		t.Fatalf("result=%+v", result)
	}
	for _, status := range result.Statuses {
		if status.State != StateUnchanged {
			t.Fatalf("statuses=%v", result.Statuses)
		}
	}
}

func TestNarrowedPlatformSyncPreservesOtherArch(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	full := mustBuildPlan(t, catalog, Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	narrowed := mustBuildPlan(t, catalog, Selection{Platform: "linux-amd64"})
	result, err := Reconcile(context.Background(), root, narrowed, (&recordingFetch{contents: contents.byURL}).fetch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	for _, key := range []string{contents.digests["alpha-arm64-v1"], contents.digests["gamma-arm64-v1"]} {
		if !repoObjectExists(root, "sha256", key) {
			t.Fatalf("object %s was removed", key)
		}
	}
	retained := retainedByDigest(result.RetainedObjects)
	for _, key := range []string{contents.digests["alpha-arm64-v1"], contents.digests["gamma-arm64-v1"]} {
		if retained["sha256:"+key].State != StateRetained {
			t.Fatalf("retained=%v", result.RetainedObjects)
		}
	}
}

func TestSharedDigestSurvivesNarrowedSync(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	full := mustBuildPlan(t, catalog, Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	narrowed := mustBuildPlan(t, catalog, Selection{App: "alpha"})
	result, err := Reconcile(context.Background(), root, narrowed, (&recordingFetch{contents: contents.byURL}).fetch)
	if err != nil {
		t.Fatal(err)
	}
	shared := contents.digests["alpha-amd64-v2"]
	if !repoObjectExists(root, "sha256", shared) {
		t.Fatal("shared digest was removed by narrowed sync")
	}
	if got := readRepoObject(t, root, "sha256", shared); got != contents.byName["alpha-amd64-v2"] {
		t.Fatalf("shared object=%q", got)
	}
	_ = result
}

func TestUnrestrictedSyncPreservesOrphan(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	full := mustBuildPlan(t, catalog, Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	before := map[string]string{}
	for _, object := range full.Desired {
		before[object.Algorithm+":"+object.Digest] = readRepoObject(t, root, object.Algorithm, object.Digest)
	}
	narrowed := mustBuildPlan(t, catalog, Selection{App: "alpha"})
	narrowedResult, err := Reconcile(context.Background(), root, narrowed, (&recordingFetch{contents: contents.byURL}).fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !repoObjectExists(root, "sha256", orphan) {
		t.Fatal("narrowed sync removed an unattributed orphan")
	}
	if narrowedResult.Removed != 0 {
		t.Fatalf("result=%+v", narrowedResult)
	}
	unrestricted, err := BuildPlan(catalog, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(context.Background(), root, unrestricted, (&recordingFetch{contents: contents.byURL}).fetch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if !repoObjectExists(root, "sha256", orphan) {
		t.Fatal("unrestricted sync removed the orphan")
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
	for _, object := range full.Desired {
		if got := readRepoObject(t, root, object.Algorithm, object.Digest); got != before[object.Algorithm+":"+object.Digest] {
			t.Fatalf("desired object %s was mutated", object.Digest)
		}
	}
	retained := retainedByDigest(result.RetainedObjects)
	entry, ok := retained["sha256:"+orphan]
	if !ok || entry.State != StateRetained {
		t.Fatalf("retained=%v", result.RetainedObjects)
	}
	if entry.Size != int64(len("orphan bytes")) {
		t.Fatalf("retained=%v", result.RetainedObjects)
	}
	if result.Retained != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestUnsafeEntriesAbortSync(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	full := mustBuildPlan(t, catalog, Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "v1", "sha256", contentDigest("link target"))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	fetch := &recordingFetch{contents: contents.byURL}
	if _, err := Reconcile(context.Background(), root, full, fetch.fetch); err == nil {
		t.Fatal("symlinked entry was accepted")
	}
	if !repoObjectExists(root, "sha256", orphan) {
		t.Fatal("orphan was touched despite unsafe entry")
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
	if len(fetch.calls) != 0 {
		t.Fatalf("fetch calls=%v", fetch.calls)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(context.Background(), root, full, fetch.fetch); err == nil {
		t.Fatal("unexpected entry was accepted")
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
}

func TestAcquisitionFailurePreservesValidObjects(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "alpha", Platform: "linux-amd64"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	validDesired := contents.digests["alpha-amd64-v2"]
	writeRepoObject(t, root, "sha256", validDesired, contents.byName["alpha-amd64-v2"])
	fetch := &recordingFetch{contents: contents.byURL, fail: map[string]error{fixtureURL("alpha-amd64-v1"): errors.New("source unavailable")}}
	result, err := Reconcile(context.Background(), root, plan, fetch.fetch)
	if err == nil {
		t.Fatal("expected acquisition failure")
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
	if got := readRepoObject(t, root, "sha256", validDesired); got != contents.byName["alpha-amd64-v2"] {
		t.Fatalf("pre-existing valid object mutated: %q", got)
	}
	if result.Missing == 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestFailedRepairLeavesCorruptObjectUnchanged(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	corrupt := contents.digests["gamma-arm64-v1"]
	writeRepoObject(t, root, "sha256", corrupt, "tampered bytes")
	fetch := &recordingFetch{contents: map[string]string{fixtureURL("gamma-arm64-v1"): "wrong bytes"}}
	result, err := Reconcile(context.Background(), root, plan, fetch.fetch)
	if err == nil {
		t.Fatal("expected repair failure")
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", corrupt); got != "tampered bytes" {
		t.Fatalf("corrupt object mutated by failed repair: %q", got)
	}
	if result.Corrupt != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestWrongBytesAreNeverPublished(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	fetch := &recordingFetch{contents: map[string]string{fixtureURL("gamma-arm64-v1"): "wrong bytes"}}
	result, err := Reconcile(context.Background(), root, plan, fetch.fetch)
	if err == nil {
		t.Fatal("expected digest failure")
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
	if repoObjectExists(root, "sha256", contents.digests["gamma-arm64-v1"]) {
		t.Fatal("bad bytes were published")
	}
}

func TestDestinationWriteFailurePreservesValidObjects(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	fetch := &recordingFetch{fail: map[string]error{fixtureURL("gamma-arm64-v1"): ErrDestinationWrite}}
	result, err := Reconcile(context.Background(), root, plan, fetch.fetch)
	if err == nil {
		t.Fatal("expected destination write failure")
	}
	if !errors.Is(err, ErrDestinationWrite) {
		t.Fatalf("error=%v", err)
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
}

func TestCancellationPreservesValidObjects(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	ctx, cancel := context.WithCancel(context.Background())
	fetch := &recordingFetch{contents: contents.byURL, onCall: func(string) { cancel() }}
	result, err := Reconcile(ctx, root, plan, fetch.fetch)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if result.Removed != 0 {
		t.Fatalf("result=%+v", result)
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
}

func TestFailedSyncRerunConverges(t *testing.T) {
	contents := fixtureContents()
	catalog := fixtureCatalog(contents)
	full := mustBuildPlan(t, catalog, Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	missing := contents.digests["gamma-arm64-v1"]
	if err := os.Remove(filepath.Join(root, "v1", "sha256", missing)); err != nil {
		t.Fatal(err)
	}
	failing := &recordingFetch{contents: contents.byURL, fail: map[string]error{fixtureURL("gamma-arm64-v1"): errors.New("source unavailable")}}
	failed, err := Reconcile(context.Background(), root, full, failing.fetch)
	if err == nil {
		t.Fatal("expected acquisition failure")
	}
	if failed.Removed != 0 {
		t.Fatalf("failed result=%+v", failed)
	}
	if failed.Missing == 0 {
		t.Fatalf("failed result=%+v", failed)
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
	for _, object := range full.Desired {
		if object.Digest == missing {
			continue
		}
		if !repoObjectExists(root, object.Algorithm, object.Digest) {
			t.Fatalf("desired object %s was touched", object.Digest)
		}
	}
	rerun, err := Reconcile(context.Background(), root, full, (&recordingFetch{contents: contents.byURL}).fetch)
	if err != nil {
		t.Fatalf("rerun error = %v", err)
	}
	if rerun.Removed != 0 || rerun.Missing != 0 || rerun.Corrupt != 0 {
		t.Fatalf("rerun result=%+v", rerun)
	}
	if rerun.Downloaded != 1 {
		t.Fatalf("rerun result=%+v", rerun)
	}
	for _, object := range full.Desired {
		if !repoObjectExists(root, object.Algorithm, object.Digest) {
			t.Fatalf("desired object %s missing after rerun", object.Digest)
		}
	}
	if got := readRepoObject(t, root, "sha256", orphan); got != "orphan bytes" {
		t.Fatalf("orphan object=%q", got)
	}
}

func TestWriterLockExclusion(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	lock, err := locking.AcquireExistingWithTimeout(context.Background(), filepath.Join(root, ".sync.lock"), locking.DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	fetch := &recordingFetch{contents: contents.byURL}
	if _, err := Reconcile(ctx, root, plan, fetch.fetch); err == nil {
		t.Fatal("second writer was admitted")
	}
	if repoObjectExists(root, "sha256", contents.digests["gamma-arm64-v1"]) {
		t.Fatal("excluded writer mutated the repository")
	}
}

func TestInspectReadAbsentStaysAbsent(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := filepath.Join(t.TempDir(), "absent")
	statuses, retained, err := InspectRead(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != StateMissing {
		t.Fatalf("statuses=%v", statuses)
	}
	if len(retained) != 0 {
		t.Fatalf("retained=%v", retained)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("absent path was created: %v", err)
	}
}

func TestInspectReadEmptyDirStaysEmpty(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	statuses, retained, err := InspectRead(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != StateMissing {
		t.Fatalf("statuses=%v", statuses)
	}
	if len(retained) != 0 {
		t.Fatalf("retained=%v", retained)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestInspectReadNeverCreatesLockFile(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".sync.lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectRead(context.Background(), root, plan); err == nil {
		t.Fatal("expected lock error for legacy repository")
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatal("reader created the lock file")
	}
}

func TestInspectReadLeavesRepositoryUntouched(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, plan, (&recordingFetch{contents: contents.byURL}).fetch)
	orphan := contentDigest("orphan bytes")
	writeRepoObject(t, root, "sha256", orphan, "orphan bytes")
	before := map[string]string{}
	for _, object := range plan.Desired {
		before[object.Digest] = readRepoObject(t, root, object.Algorithm, object.Digest)
	}
	statuses, retained, err := InspectRead(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range plan.Desired {
		if got := readRepoObject(t, root, object.Algorithm, object.Digest); got != before[object.Digest] {
			t.Fatalf("object %s changed", object.Digest)
		}
	}
	if !repoObjectExists(root, "sha256", orphan) {
		t.Fatal("orphan changed during inspection")
	}
	byState := statusByDigest(statuses)
	for _, status := range byState {
		if status.State != StateUnchanged {
			t.Fatalf("statuses=%v", statuses)
		}
	}
	if len(retained) != 1 {
		t.Fatalf("retained=%v", retained)
	}
	if retained[0].Algorithm != "sha256" || retained[0].Digest != orphan || retained[0].State != StateRetained {
		t.Fatalf("retained=%v", retained)
	}
	if retained[0].Size != int64(len("orphan bytes")) {
		t.Fatalf("retained=%v", retained)
	}
}

func TestInspectReadReportsCorruptExtrasWithoutRequiredCounters(t *testing.T) {
	contents := fixtureContents()
	plan := mustBuildPlan(t, fixtureCatalog(contents), Selection{App: "gamma"})
	root := t.TempDir()
	populateRepo(t, context.Background(), root, plan, (&recordingFetch{contents: contents.byURL}).fetch)
	validExtra := contentDigest("valid extra")
	writeRepoObject(t, root, "sha256", validExtra, "valid extra")
	corruptExtra := contentDigest("corrupt extra expected")
	writeRepoObject(t, root, "sha256", corruptExtra, "corrupt extra actual")
	statuses, retained, err := InspectRead(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	var requiredMissing, requiredCorrupt int
	for _, status := range statuses {
		switch status.State {
		case StateMissing:
			requiredMissing++
		case StateCorrupt:
			requiredCorrupt++
		case StateUnchanged:
		default:
			t.Fatalf("statuses=%v", statuses)
		}
	}
	if requiredMissing != 0 || requiredCorrupt != 0 {
		t.Fatalf("statuses=%v", statuses)
	}
	byDigest := retainedByDigest(retained)
	if byDigest["sha256:"+validExtra].State != StateRetained {
		t.Fatalf("retained=%v", retained)
	}
	if byDigest["sha256:"+corruptExtra].State != StateCorrupt {
		t.Fatalf("retained=%v", retained)
	}
	if len(retained) != 2 {
		t.Fatalf("retained=%v", retained)
	}
}

func TestVerifyAllCollectReportsEveryCorruptObject(t *testing.T) {
	root := t.TempDir()
	if err := Init(root); err != nil {
		t.Fatal(err)
	}
	goodOne := contentDigest("good one")
	goodTwo := contentDigest("good two")
	writeRepoObject(t, root, "sha256", goodOne, "good one")
	writeRepoObject(t, root, "sha256", goodTwo, "good two")
	badOne := contentDigest("bad one expected")
	badTwo := contentDigest("bad two expected")
	writeRepoObject(t, root, "sha256", badOne, "bad one actual")
	writeRepoObject(t, root, "sha256", badTwo, "bad two actual")
	healthy, corrupt, err := VerifyAllCollect(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(healthy) != 2 || len(corrupt) != 2 {
		t.Fatalf("healthy=%v corrupt=%v", healthy, corrupt)
	}
	got := map[string]bool{}
	for _, status := range corrupt {
		got[status.Digest] = true
	}
	if !got[badOne] || !got[badTwo] {
		t.Fatalf("corrupt=%v", corrupt)
	}
}
