package integrity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T, appimage bool) (string, Identity) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"binary": "original", "library": "library"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("binary", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	identity := Identity{App: "fixture", Version: "1", Fingerprint: "sha256:" + strings.Repeat("a", 64), AppImage: appimage}
	if err := Record(context.Background(), root, identity); err != nil {
		t.Fatal(err)
	}
	return root, identity
}
func TestVerifyDetectsDriftWithoutWriting(t *testing.T) {
	root, id := fixture(t, false)
	ctx := context.Background()
	if changes, err := Verify(ctx, root, id); err != nil || len(changes) != 0 {
		t.Fatalf("healthy: %v %v", changes, err)
	}
	baseline, err := os.ReadFile(filepath.Join(root, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte("modified"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "library")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra"), []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside/not/read", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	want := []Change{{"binary", "changed"}, {"extra", "unexpected"}, {"library", "missing"}, {"link", "changed"}}
	for range 2 {
		changes, err := Verify(ctx, root, id)
		if err != nil || !reflect.DeepEqual(changes, want) {
			t.Fatalf("changes: %v %v", changes, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, Filename))
	if string(after) != string(baseline) {
		t.Fatal("baseline changed")
	}
	content, _ := os.ReadFile(filepath.Join(root, "binary"))
	if string(content) != "modified" {
		t.Fatal("payload changed")
	}
}
func TestModeAndAppImagePortableData(t *testing.T) {
	for _, appimage := range []bool{true, false} {
		root, id := fixture(t, appimage)
		if err := os.Mkdir(filepath.Join(root, "appimage.home"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "appimage.home", "data"), []byte("user"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/not/accessible", filepath.Join(root, "appimage.config")); err != nil {
			t.Fatal(err)
		}
		changes, err := Verify(context.Background(), root, id)
		if err != nil || appimage && len(changes) != 0 || !appimage && len(changes) != 3 {
			t.Fatalf("portable %v: %v %v", appimage, changes, err)
		}
		if err := os.Chmod(filepath.Join(root, "binary"), 0o644); err != nil {
			t.Fatal(err)
		}
		changes, err = Verify(context.Background(), root, id)
		if err != nil || changes[len(changes)-1] != (Change{"binary", "changed"}) {
			t.Fatalf("mode: %v %v", changes, err)
		}
	}
}
func TestUnsafeFilesAndInventories(t *testing.T) {
	for _, damage := range []string{"missing", "symlink", "directory", "fifo", "hardlink", "oversize", "identity", "duplicate", "unknown", "trailing", "path", "duplicate-path", "kind", "digest", "payload-fifo", "payload-hardlink", "large-payload", "deep-payload"} {
		t.Run(damage, func(t *testing.T) {
			root, id := fixture(t, false)
			path := filepath.Join(root, Filename)
			data, _ := os.ReadFile(path)
			switch damage {
			case "missing", "symlink", "directory", "fifo", "hardlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				switch damage {
				case "symlink":
					err = os.Symlink(filepath.Join(root, "binary"), path)
				case "directory":
					err = os.Mkdir(path, 0o700)
				case "fifo":
					err = unix.Mkfifo(path, 0o600)
				case "hardlink":
					err = os.Link(filepath.Join(root, "binary"), path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(path, maxInventory+1); err != nil {
					t.Fatal(err)
				}
			case "identity":
				id.Version = "2"
			case "duplicate":
				data = []byte(strings.Replace(string(data), `"schema":1`, `"schema":0,"schema":1`, 1))
			case "unknown":
				data = append([]byte(`{"unknown":true,`), data[1:]...)
			case "trailing":
				data = append(data, []byte(`{}`)...)
			case "path", "duplicate-path", "kind", "digest":
				var inv inventory
				if err := json.Unmarshal(data, &inv); err != nil {
					t.Fatal(err)
				}
				switch damage {
				case "path":
					inv.Entries[0].Path = "../outside"
				case "duplicate-path":
					inv.Entries = append(inv.Entries, inv.Entries[0])
				case "kind":
					inv.Entries[0].Kind = "device"
				case "digest":
					inv.Entries[0].SHA256 = "invalid"
				}
				data, _ = json.Marshal(inv)
			case "payload-fifo":
				if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "payload-hardlink":
				if err := os.Link(filepath.Join(root, "binary"), filepath.Join(root, "hardlink")); err != nil {
					t.Fatal(err)
				}
			case "large-payload":
				if err := os.Truncate(filepath.Join(root, "binary"), maxBytes+1); err != nil {
					t.Fatal(err)
				}
			case "deep-payload":
				if err := os.MkdirAll(filepath.Join(root, strings.Repeat("a/", 65)), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch damage {
			case "duplicate", "unknown", "trailing", "path", "duplicate-path", "kind", "digest":
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Verify(context.Background(), root, id); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}
func TestRecordRejectsCollisionAndCancellation(t *testing.T) {
	root, id := fixture(t, false)
	if err := Record(context.Background(), root, id); err == nil {
		t.Fatal("baseline replaced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Verify(ctx, root, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("verify cancellation: %v", err)
	}
	if err := os.Remove(filepath.Join(root, Filename)); err != nil {
		t.Fatal(err)
	}
	if err := Record(ctx, root, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("record cancellation: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, Filename)); !os.IsNotExist(err) {
		t.Fatal("cancelled record published baseline")
	}
}
