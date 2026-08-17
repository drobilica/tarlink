package integration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testSpec(root string) Spec {
	spec := Spec{
		ID: "blender", Name: "Blender", Executable: "bin/blender",
		ApplicationRoot:   filepath.Join(root, "apps", "blender"),
		LocalBinDirectory: filepath.Join(root, "bin"),
		DesktopDirectory:  filepath.Join(root, "applications"),
		DesktopEnabled:    true, DesktopCategories: []string{"Graphics"},
	}
	spec.DesktopSHA256 = DesktopDigest(spec, ExpectedPaths(spec).ExecutableLink)
	return spec
}

func TestEnsureAndRemoveOwned(t *testing.T) {
	spec := testSpec(t.TempDir())
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if err := ValidateOwned(spec); err != nil {
		t.Fatalf("ValidateOwned() error = %v", err)
	}
	if err := RemoveOwned(spec); err != nil {
		t.Fatalf("RemoveOwned() error = %v", err)
	}
	for _, path := range []string{paths.ExecutableLink, paths.DesktopEntry} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("integration remains at %s: %v", path, err)
		}
	}
}

func TestEnsureRefusesUnrelatedFiles(t *testing.T) {
	for _, kind := range []string{"executable", "desktop"} {
		t.Run(kind, func(t *testing.T) {
			spec := testSpec(t.TempDir())
			paths := ExpectedPaths(spec)
			path := paths.ExecutableLink
			if kind == "desktop" {
				path = paths.DesktopEntry
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("user owned"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Ensure(spec); !errors.Is(err, ErrConflict) {
				t.Fatalf("Ensure() error = %v", err)
			}
			content, _ := os.ReadFile(path)
			if string(content) != "user owned" {
				t.Fatalf("unrelated content changed: %q", content)
			}
		})
	}
}

func TestRemoveRefusesReplacedIntegration(t *testing.T) {
	spec := testSpec(t.TempDir())
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.ExecutableLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ExecutableLink, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwned(spec); !errors.Is(err, ErrConflict) {
		t.Fatalf("RemoveOwned() error = %v", err)
	}
	if _, err := os.Stat(paths.DesktopEntry); err != nil {
		t.Fatalf("desktop entry was removed despite conflict: %v", err)
	}
}

func TestRemoveRefusesModifiedDesktopWithOwnershipMarker(t *testing.T) {
	spec := testSpec(t.TempDir())
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(paths.DesktopEntry)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, []byte("Comment=user replacement\n")...)
	if err := os.WriteFile(paths.DesktopEntry, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwned(spec); !errors.Is(err, ErrConflict) {
		t.Fatalf("RemoveOwned() error = %v", err)
	}
	if _, err := os.Lstat(paths.ExecutableLink); err != nil {
		t.Fatalf("executable link was removed despite conflict: %v", err)
	}
}
