package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/research"
)

func TestRegistryApplyPreservesVerificationSource(t *testing.T) {
	for _, published := range []bool{true, false} {
		t.Run(map[bool]string{true: "published", false: "missing"}[published], func(t *testing.T) {
			root, m, releases, _ := registryApplyFixture(t)
			asset := &(*releases)[0].Assets[0]
			want := "https://api.github.com/repos/godotengine/godot-builds/releases/assets/20"
			if !published {
				asset.Digest = ""
				want = asset.URL
			}
			if _, err := m.ApplyRegistryUpdates(context.Background(), RegistryApplyOptions{Root: root}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "apps", "godot", "manifest.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			doc, err := manifest.ParseBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			got := doc.Release.Releases[len(doc.Release.Releases)-1].Artifacts["linux-amd64"].Verification
			if got.Source != want {
				t.Fatalf("source=%s, want %s", got.Source, want)
			}
		})
	}
}

func TestOnboardingPreservesVerificationSourceForAnyRepository(t *testing.T) {
	for _, repo := range []string{"example/project", "AnotherOwner/AnotherRepo"} {
		for _, published := range []bool{true, false} {
			t.Run(repo+map[bool]string{true: "/published", false: "/missing"}[published], func(t *testing.T) {
				payload := appImageELF(0x3e)
				sum := sha256.Sum256(payload)
				digest := hex.EncodeToString(sum[:])
				url := "https://github.com/" + repo + "/releases/download/v1/app-linux-amd64.AppImage"
				asset := research.Asset{ID: 20, Name: "app-linux-amd64.AppImage", URL: url, Size: int64(len(payload)), State: "uploaded"}
				want := url
				if published {
					asset.Digest = "sha256:" + digest
					want = "https://api.github.com/repos/" + strings.ToLower(repo) + "/releases/assets/20"
				}
				release := research.Release{ID: 10, Tag: "v1", Assets: []research.Asset{asset}}
				m := &Maintainer{client: &download.Client{HTTP: &http.Client{Transport: maintainerRoundTrip(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host == "api.github.com" {
						data, err := json.Marshal(release)
						if err != nil {
							t.Fatal(err)
						}
						return maintainerResponse(200, string(data)), nil
					}
					if r.URL.String() != url {
						t.Fatalf("unexpected URL %s", r.URL)
					}
					return maintainerResponse(200, string(payload)), nil
				})}}}
				m.layout.Cache = t.TempDir()
				target, err := research.ParseReleaseAssetURL(url)
				if err != nil {
					t.Fatal(err)
				}
				candidate, _, err := m.deriveRegistryCandidate(context.Background(), target, true)
				if err != nil {
					t.Fatal(err)
				}
				check := func(candidate RegistryCandidate) {
					t.Helper()
					doc, err := candidateDocument(candidate, RegistryAddOptions{})
					if err != nil {
						t.Fatal(err)
					}
					got := doc.Release.Releases[0].Artifacts["linux-amd64"].Verification
					if got.Source != want || got.Digest != digest {
						t.Fatalf("verification=%+v want source=%s digest=%s", got, want, digest)
					}
				}
				check(candidate)
				release.Repository = target.Repository
				asset.Repository = release.Repository
				asset.ReleaseID = release.ID
				candidate, _ = candidateFromResearch(ResearchResult{Repository: release.Repository, Release: release, Asset: asset, Provenance: research.EvaluateProvenance(release.Repository, release, asset), Inspection: &research.Inspection{ArtifactType: "appimage", ComputedDigests: map[string]string{"sha256": digest}}}, url)
				candidate.Platform = "linux-amd64"
				check(candidate)
			})
		}
	}
}
