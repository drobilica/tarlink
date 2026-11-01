package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/research"
	"go.yaml.in/yaml/v3"
)

func registryApplyFixture(t *testing.T) (string, *Maintainer, *[]research.Release, *int) {
	t.Helper()
	root := t.TempDir()
	data := strings.ReplaceAll(onboardingValidManifest, "foo", "godot")
	data = strings.ReplaceAll(data, "archive: zip", "archive: appimage")
	data = strings.ReplaceAll(data, "path: godot", "path: appimage")
	writeOnboardingManifest(t, filepath.Join(root, "apps", "godot", "manifest.yaml"), data)
	payload := appImageELF(0x3e)
	sum := sha256.Sum256(payload)
	releases := []research.Release{{ID: 10, Tag: "v2.0", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Assets: []research.Asset{{ID: 20, Name: "godot-linux-amd64.AppImage", URL: "https://github.com/godotengine/godot/releases/download/v2.0/godot-linux-amd64.AppImage", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(sum[:]), State: "uploaded"}}}, {ID: 11, Tag: "v1.0", PublishedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}}
	downloads := 0
	client := &download.Client{HTTP: &http.Client{Transport: maintainerRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" && r.URL.Path == "/repos/godotengine/godot/releases" {
			data, err := json.Marshal(releases)
			if err != nil {
				t.Fatal(err)
			}
			return maintainerResponse(http.StatusOK, string(data)), nil
		}
		if r.URL.Host == "github.com" && strings.HasPrefix(r.URL.Path, "/godotengine/godot/releases/download/") {
			downloads++
			return maintainerResponse(http.StatusOK, string(payload)), nil
		}
		t.Fatalf("unexpected URL %s", r.URL)
		return nil, nil
	})}}
	return root, &Maintainer{client: client}, &releases, &downloads
}

func TestRegistryApplyPreviewPublicationAndRepeat(t *testing.T) {
	root, m, _, downloads := registryApplyFixture(t)
	before := registryEditSnapshot(t, root)
	report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root, DryRun: true})
	if err != nil || len(report.Results) != 1 || report.Results[0].Status != "would-update" || *downloads != 1 {
		t.Fatalf("report=%+v downloads=%d err=%v", report, *downloads, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("dry run changed checkout")
	}
	report, err = m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
	if err != nil || report.Results[0].Status != "updated" || !report.Results[0].Edit.Written {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	item := catalog.Variants["godot"][manifest.Platform{OS: "linux", Arch: "amd64"}]
	if item.Release.Version != "2.0" || len(item.ReleaseHistory.Releases) != 2 {
		t.Fatalf("history=%+v", item.ReleaseHistory)
	}
	before = registryEditSnapshot(t, root)
	report, err = m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
	if err != nil || report.Results[0].Status != "no-change" || *downloads != 2 {
		t.Fatalf("repeat report=%+v downloads=%d err=%v", report, *downloads, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("repeat changed checkout")
	}
}

func TestRegistryApplyFailurePreservesManifest(t *testing.T) {
	for _, kind := range []string{"ambiguous", "wrong-repository", "digest-mismatch", "malformed-digest", "unsupported-arch", "download-failure"} {
		t.Run(kind, func(t *testing.T) {
			root, m, releases, _ := registryApplyFixture(t)
			asset := &(*releases)[0].Assets[0]
			switch kind {
			case "ambiguous":
				copy := *asset
				copy.ID++
				copy.Name = "godot-other-linux-amd64.AppImage"
				copy.URL = strings.Replace(copy.URL, "godot-linux-amd64.AppImage", copy.Name, 1)
				(*releases)[0].Assets = append((*releases)[0].Assets, copy)
			case "wrong-repository":
				asset.URL = strings.Replace(asset.URL, "godotengine/godot", "evil/project", 1)
			case "digest-mismatch":
				asset.Digest = "sha256:" + strings.Repeat("0", 64)
			case "malformed-digest":
				asset.Digest = "sha256:bad"
			case "unsupported-arch":
				asset.Name = "godot-linux-arm64.AppImage"
			case "download-failure":
				asset.Size++
			}
			before := registryEditSnapshot(t, root)
			report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
			if err == nil || report.Results[0].Status != "error" {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
				t.Fatal("failed apply changed checkout")
			}
		})
	}
}

func TestRegistryApplyNeverBackfillsOrDowngrades(t *testing.T) {
	for _, tag := range []string{"v1.2.3", "v0.9"} {
		root, m, releases, downloads := registryApplyFixture(t)
		(*releases)[0].Tag = tag
		before := registryEditSnapshot(t, root)
		report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
		if err != nil || report.Results[0].Status != "no-change" || *downloads != 0 {
			t.Fatalf("tag=%s report=%+v err=%v", tag, report, err)
		}
		if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
			t.Fatal("historical releases changed checkout")
		}
	}
}

func TestRegistryEditUpdateRejectsStaleExpectedBytes(t *testing.T) {
	root := registryEditFixture(t)
	before := registryEditSnapshot(t, root)
	_, err := (&Maintainer{}).EditRegistry(context.Background(), RegistryEditOptions{Root: root, ID: "foo", Update: []byte(onboardingValidManifest), Expected: []byte("stale")})
	if err == nil || !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatalf("stale write err=%v", err)
	}
}

func TestRegistryApplyRequiresEveryPlatformBeforePublishing(t *testing.T) {
	root, m, _, _ := registryApplyFixture(t)
	path := filepath.Join(root, "apps", "godot", "manifest.yaml")
	data, err := readRegistryManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := manifest.ParseBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	document.Release.Releases[0].Artifacts["linux-arm64"] = document.Release.Releases[0].Artifacts["linux-amd64"]
	data, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeOnboardingManifest(t, path, string(data))
	before := registryEditSnapshot(t, root)
	report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
	if err == nil || report.Results[0].Status != "error" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("incomplete platform set published")
	}
}

func TestRegistryApplyPreservesChannelsAndHistory(t *testing.T) {
	root, m, releases, _ := registryApplyFixture(t)
	path := filepath.Join(root, "apps", "godot", "manifest.yaml")
	data, err := readRegistryManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := manifest.ParseBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	document.Release.Current = ""
	document.Release.DefaultChannel = "stable"
	document.Release.Channels = map[string]manifest.ChannelHead{"stable": {Current: "1.2.3"}, "nightly": {Current: "1.2.3-nightly"}}
	document.Release.Releases[0].Channel = "stable"
	nightly := document.Release.Releases[0]
	nightly.Channel = "nightly"
	nightly.Version = "1.2.3-nightly"
	document.Release.Releases = append(document.Release.Releases, nightly)
	data, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeOnboardingManifest(t, path, string(data))
	incoming := (*releases)[0]
	incoming.ID = 12
	incoming.Tag = "v3.0-nightly"
	incoming.Prerelease = true
	incoming.Assets = append([]research.Asset{}, incoming.Assets...)
	incoming.Assets[0].ID = 22
	incoming.Assets[0].URL = strings.Replace(incoming.Assets[0].URL, "/v2.0/", "/v3.0-nightly/", 1)
	*releases = append(*releases, incoming, research.Release{ID: 13, Tag: "v1.2.3-nightly", Prerelease: true, PublishedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)})
	report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
	if err != nil || report.Results[0].Status != "updated" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, err = readRegistryManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err = manifest.ParseBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if document.Release.Channels["stable"].Current != "2.0" || document.Release.Channels["nightly"].Current != "3.0-nightly" || len(document.Release.Releases) != 4 {
		t.Fatalf("release=%+v", document.Release)
	}
}

func TestRegistryApplyMissingDigestAndCancellation(t *testing.T) {
	root, m, releases, _ := registryApplyFixture(t)
	(*releases)[0].Assets[0].Digest = ""
	report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root, DryRun: true})
	if err != nil || report.Results[0].Status != "would-update" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	before := registryEditSnapshot(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ApplyRegistryUpdates(ctx, RegistryApplyOptions{Root: root}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("cancellation changed checkout")
	}
}

func TestRegistryApplyRejectsYAMLAliasesWithoutWriting(t *testing.T) {
	root, m, _, _ := registryApplyFixture(t)
	path := filepath.Join(root, "apps", "godot", "manifest.yaml")
	data, err := readRegistryManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(strings.Replace(string(data), "name: Foo", "name: &name Foo", 1), "summary: A portable application", "summary: *name", 1))
	writeOnboardingManifest(t, path, string(data))
	before := registryEditSnapshot(t, root)
	report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
	if err == nil || len(report.Results) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("alias edit changed checkout")
	}
}

func TestRegistryApplyPreservesVerificationAlgorithm(t *testing.T) {
	root, m, _, _ := registryApplyFixture(t)
	path := filepath.Join(root, "apps", "godot", "manifest.yaml")
	data, err := readRegistryManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(strings.Replace(string(data), "algorithm: sha256", "algorithm: sha512", 1), "digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "digest: "+strings.Repeat("a", 128), 1))
	writeOnboardingManifest(t, path, string(data))
	report, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root})
	if err != nil || report.Results[0].Status != "updated" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, err = readRegistryManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := manifest.ParseBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if document.Release.Verification.Algorithm != "sha512" || len(document.Release.Releases[1].Artifacts["linux-amd64"].Verification.Digest) != 128 {
		t.Fatal("verification algorithm changed")
	}
}
