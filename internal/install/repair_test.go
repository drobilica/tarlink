package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
)

func TestRepairRestoresExactPackage(t *testing.T) {
	for _, damage := range []string{"executable", "payload", "application", "integrations", "current", "healthy", "equivalent-metadata"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			layout := testLayout(t)
			first := newArtifactServer(t, fixtureArchive(t, "1.0"))
			manager := managerFor(t, layout, first)
			if _, err := manager.InstallWithOptions(ctx, first.manifest("1.0"), Options{Channel: "stable"}, nil); err != nil {
				t.Fatal(err)
			}
			second := newArtifactServer(t, fixtureArchive(t, "2.0"))
			manager.Client.HTTP = second.server.Client()
			item := second.manifest("2.0")
			if _, err := manager.UpdateWithOptions(ctx, item, Options{Channel: "stable"}, nil); err != nil {
				t.Fatal(err)
			}
			if err := manager.SetPinned(ctx, item.ID, true); err != nil {
				t.Fatal(err)
			}
			before, err := state.LoadForApp(layout, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := layout.PackagePath(item.ID, before.Current, before.CurrentFingerprint)
			appRoot := filepath.Join(layout.Apps, item.ID)
			switch damage {
			case "executable":
				err = os.WriteFile(filepath.Join(payload, "bin/run"), []byte("damaged"), 0o700)
			case "payload":
				err = os.RemoveAll(payload)
			case "application":
				err = os.RemoveAll(appRoot)
			case "integrations":
				for _, path := range []string{before.Integration.Executables[0].Link, before.Integration.DesktopEntry, before.Integration.IconFile} {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			case "current":
				err = os.Remove(filepath.Join(appRoot, "current"))
			case "equivalent-metadata":
				enabled := true
				item.Application.Executables[0].CreateBinLink = &enabled
				item.Release.Verification.Source = "https://example.com/release"
				item.ReleaseHistory.Releases[0] = item.Release
			}
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := manager.RepairSubject(ctx, item, nil)
			if err != nil {
				t.Fatal(err)
			}
			after, err := state.LoadForApp(layout, item.ID)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(before, outcome.State) {
				t.Fatalf("state changed: %+v %+v %v", before, after, err)
			}
			content, err := os.ReadFile(filepath.Join(appRoot, "current/bin/run"))
			if err != nil || string(content) != "2.0" {
				t.Fatalf("payload = %q, %v", content, err)
			}
			spec, err := manager.integrationSpec(after, item.Name, item.Desktop.Categories)
			if err != nil {
				t.Fatal(err)
			}
			if err := integration.ValidateOwned(spec); err != nil {
				t.Fatal(err)
			}
			// If the application root itself was removed, its previous payload is
			// also lost. Repair restores the current package, never invents history.
			if damage != "application" {
				previous, _ := layout.PackagePath(item.ID, after.Previous, after.PreviousFingerprint)
				content, err := os.ReadFile(filepath.Join(previous, "bin/run"))
				if err != nil || string(content) != "1.0" {
					t.Fatalf("previous = %q, %v", content, err)
				}
			}
		})
	}
}

func TestRepairRefusesConflictsAndChangedIdentity(t *testing.T) {
	for _, conflict := range []string{"identity", "current", "payload-symlink", "parent-symlink", "bin", "desktop", "icon", "state", "download"} {
		t.Run(conflict, func(t *testing.T) {
			ctx := context.Background()
			layout := testLayout(t)
			server := newArtifactServer(t, fixtureArchive(t, "1.0"))
			manager := managerFor(t, layout, server)
			item := server.manifest("1.0")
			outcome, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := outcome.State
			payload, _ := layout.PackagePath(item.ID, before.Current, before.CurrentFingerprint)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("user data"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch conflict {
			case "identity":
				item.Name = "Changed"
			case "current":
				path := filepath.Join(layout.Apps, item.ID, "current")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outside, path)
			case "payload-symlink", "parent-symlink":
				path := payload
				if conflict == "parent-symlink" {
					path = filepath.Dir(payload)
				}
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outside, path)
			case "bin", "desktop", "icon":
				path := before.Integration.Executables[0].Link
				if conflict == "desktop" {
					path = before.Integration.DesktopEntry
				}
				if conflict == "icon" {
					path = before.Integration.IconFile
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, []byte("user data"), 0o600)
			case "state":
				path, _ := layout.StatePath(item.ID)
				err = os.WriteFile(path, []byte("{}"), 0o600)
			case "download":
				if err := os.RemoveAll(layout.ArtifactCacheRoot()); err != nil {
					t.Fatal(err)
				}
				server.server.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.RepairSubject(ctx, item, nil); err == nil {
				t.Fatal("repair accepted conflict")
			}
			content, err := os.ReadFile(sentinel)
			if err != nil || string(content) != "user data" {
				t.Fatalf("outside file changed: %q %v", content, err)
			}
			if conflict != "state" {
				after, err := state.LoadForApp(layout, item.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("state changed: %+v %v", after, err)
				}
			}
		})
	}
}

func TestRepairPublicationFailureRestoresOriginal(t *testing.T) {
	for _, stage := range []string{"after_download", "after_integration", "after_activation"} {
		for _, missing := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "/existing", true: "/missing"}[missing], func(t *testing.T) {
				ctx := context.Background()
				layout := testLayout(t)
				server := newArtifactServer(t, fixtureArchive(t, "1.0"))
				manager := managerFor(t, layout, server)
				item := server.manifest("1.0")
				outcome, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				payload, _ := layout.PackagePath(item.ID, outcome.State.Current, outcome.State.CurrentFingerprint)
				if missing {
					if err := os.RemoveAll(payload); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(filepath.Join(payload, "bin/run"), []byte("original"), 0o700); err != nil {
					t.Fatal(err)
				}
				bin := outcome.State.Integration.Executables[0].Link
				if err := os.Remove(bin); err != nil {
					t.Fatal(err)
				}
				manager.fail = func(at string) error {
					if at == stage {
						return errors.New("injected failure")
					}
					return nil
				}
				if _, err := manager.RepairSubject(ctx, item, nil); err == nil {
					t.Fatal("expected injected failure")
				}
				if _, err := os.Lstat(bin); !os.IsNotExist(err) {
					t.Fatalf("new integration survived failure: %v", err)
				}
				if missing {
					if _, err := os.Lstat(payload); !os.IsNotExist(err) {
						t.Fatalf("new payload survived: %v", err)
					}
				} else {
					content, err := os.ReadFile(filepath.Join(payload, "bin/run"))
					if err != nil || string(content) != "original" {
						t.Fatalf("original lost: %q %v", content, err)
					}
				}
				after, err := state.LoadForApp(layout, item.ID)
				if err != nil || !reflect.DeepEqual(outcome.State, after) {
					t.Fatalf("state changed: %+v %v", after, err)
				}
			})
		}
	}
}

func TestRepairOpaqueAppImageAndRemoteIcon(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "missing"}[missing], func(t *testing.T) {
			ctx := context.Background()
			layout := testLayout(t)
			image, icon := minimalAppImage(t), minimalPNG(t, 512, 512)
			routes := map[string][]byte{"/fixture.AppImage": image, "/icon.png": icon}
			server := newMultiRouteServer(t, routes)
			imageDigest, iconDigest := sha256.Sum256(image), sha256.Sum256(icon)
			item := appImageManifest(t, server.server, "1.0", server.server.URL+"/icon.png", hex.EncodeToString(iconDigest[:]))
			item.Release.Verification.Digest = hex.EncodeToString(imageDigest[:])
			item.ReleaseHistory.Releases[0] = item.Release
			manager := New(layout, nil)
			manager.Client.HTTP = server.server.Client()
			outcome, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := layout.PackagePath(item.ID, outcome.State.Current, outcome.State.CurrentFingerprint)
			if missing {
				if err := os.RemoveAll(payload); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(payload, "appimage"), []byte("damaged"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(outcome.State.Integration.IconFile); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.RepairSubject(ctx, item, nil); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(payload, "appimage"))
			if err != nil || !bytes.Equal(content, image) {
				t.Fatalf("image: %v", err)
			}
			content, err = os.ReadFile(outcome.State.Integration.IconFile)
			if err != nil || !bytes.Equal(content, icon) {
				t.Fatalf("icon: %v", err)
			}
		})
	}
}

func TestRepairRejectsBadDownloadedBytesAndCancellation(t *testing.T) {
	for _, mode := range []string{"digest", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			layout := testLayout(t)
			routes := map[string][]byte{"/artifact": fixtureArchive(t, "1.0")}
			server := newMultiRouteServer(t, routes)
			source := artifactServer{server: server.server, data: routes["/artifact"]}
			item := source.manifest("1.0")
			item.Release.URL += "/artifact"
			item.ReleaseHistory.Releases[0] = item.Release
			manager := managerFor(t, layout, source)
			before, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(layout.ArtifactCacheRoot()); err != nil {
				t.Fatal(err)
			}
			if mode == "digest" {
				routes["/artifact"] = []byte("hostile substituted bytes")
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := manager.RepairSubject(ctx, item, nil); err == nil {
				t.Fatal("repair accepted bad bytes/cancellation")
			}
			payload, _ := layout.PackagePath(item.ID, before.State.Current, before.State.CurrentFingerprint)
			content, err := os.ReadFile(filepath.Join(payload, "bin/run"))
			if err != nil || string(content) != "1.0" {
				t.Fatalf("original payload changed: %q %v", content, err)
			}
		})
	}
}

func TestRepairRuntimeIdentityIgnoresInformationalSource(t *testing.T) {
	approved := &manifest.Runtime{
		Schema: 1, ID: "steam-linux-runtime-4", Kind: "steam-linux-runtime", Version: "1.0",
		Platform: manifest.Platform{OS: "linux", Arch: "amd64"}, Interface: "valve-v2-entry-point-v1",
		Artifact: manifest.RuntimeArtifact{
			URL: "https://example.com/runtime.tar.xz", Archive: "tar.xz",
			Verification: manifest.Verification{Algorithm: "sha256", Digest: strings.Repeat("a", 64), Source: "https://example.com/release"},
		},
	}
	installed := *approved
	installed.Artifact.Verification.Source = "https://example.com/previous-origin"
	if !repairRuntimeMatches(&installed, approved) {
		t.Fatal("informational source changed runtime identity")
	}
	installed.Artifact.Verification.Digest = strings.Repeat("b", 64)
	if repairRuntimeMatches(&installed, approved) {
		t.Fatal("different runtime bytes accepted")
	}
	if repairRuntimeMatches(nil, approved) || repairRuntimeMatches(approved, nil) {
		t.Fatal("missing runtime accepted")
	}
}
