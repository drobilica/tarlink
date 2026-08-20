package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/filesystem"
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

func TestCheckPathDetectsShadowingAndMissingBinDir(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	earlier := filepath.Join(root, "usr", "bin")
	// Create an executable that shadows the managed command name.
	if err := os.MkdirAll(earlier, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(earlier, "blender"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := Spec{ID: "blender", LocalBinDirectory: binDir}
	pathValue := earlier + string(os.PathListSeparator) + binDir
	conflicts := CheckPath(spec, pathValue)
	if len(conflicts) != 1 || conflicts[0].Type != "shadowed" || conflicts[0].Candidate != filepath.Join(earlier, "blender") {
		t.Fatalf("conflicts = %#v, want one shadowed conflict", conflicts)
	}

	// Non-executable entries do not shadow.
	plain := filepath.Join(root, "usr", "share")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "blender"), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec2 := Spec{ID: "blender", LocalBinDirectory: binDir}
	path2 := plain + string(os.PathListSeparator) + binDir
	if conflicts := CheckPath(spec2, path2); len(conflicts) != 0 {
		t.Fatalf("non-executable conflicts = %#v, want none", conflicts)
	}

	// A missing bin directory is reported as not_in_path.
	missing := Spec{ID: "blender", LocalBinDirectory: binDir}
	if conflicts := CheckPath(missing, earlier); len(conflicts) != 1 || conflicts[0].Type != "not_in_path" {
		t.Fatalf("missing bin conflicts = %#v, want not_in_path", conflicts)
	}

	// The bin directory itself is never reported as shadowing.
	if conflicts := CheckPath(spec, binDir); len(conflicts) != 0 {
		t.Fatalf("bin-only conflicts = %#v, want none", conflicts)
	}

	// Empty or missing ID/bin is a no-op.
	if conflicts := CheckPath(Spec{}, ""); len(conflicts) != 0 {
		t.Fatalf("empty spec conflicts = %#v, want none", conflicts)
	}
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

func TestAtomicCreateReportsPostLinkSyncFailureAsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop")
	injected := errors.New("injected directory sync failure")
	committed, err := atomicCreateWithSync(path, []byte("owned"), 0o600, func(string) error { return injected })
	if !committed || !errors.Is(err, injected) {
		t.Fatalf("committed=%t error=%v", committed, err)
	}
	if content, readErr := os.ReadFile(path); readErr != nil || string(content) != "owned" {
		t.Fatalf("content=%q error=%v", content, readErr)
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

func TestRemoveOwnedIsRetryableWhenIntegrationIsAlreadyMissing(t *testing.T) {
	spec := testSpec(t.TempDir())
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.ExecutableLink); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwned(spec); err != nil {
		t.Fatalf("RemoveOwned() error = %v", err)
	}
	if _, err := os.Lstat(paths.DesktopEntry); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains: %v", err)
	}
	if err := RemoveOwned(spec); err != nil {
		t.Fatalf("second RemoveOwned() error = %v", err)
	}
}

func TestRemoveOwnedRejectsSymlinkedIntegrationParent(t *testing.T) {
	root := t.TempDir()
	spec := testSpec(root)
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.ExecutableLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(paths.ExecutableLink)); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Dir(paths.ExecutableLink)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(spec.ApplicationRoot, "current", filepath.FromSlash(spec.Executable)), filepath.Join(outside, spec.ID)); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwned(spec); !errors.Is(err, filesystem.ErrSymlink) {
		t.Fatalf("RemoveOwned() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, spec.ID)); err != nil {
		t.Fatalf("outside integration changed: %v", err)
	}
}

func TestIconLifecycleUsesHicolorAndValidatesSource(t *testing.T) {
	root := t.TempDir()
	spec := testSpec(root)
	spec.IconDirectory = filepath.Join(root, "data", "icons", "hicolor")
	spec.Icon = "share/icon.png"
	spec.IconSourceRoot = spec.ApplicationRoot
	if err := os.MkdirAll(filepath.Join(spec.ApplicationRoot, "share"), 0o700); err != nil {
		t.Fatal(err)
	}
	icon := []byte("png fixture")
	if err := os.WriteFile(filepath.Join(spec.ApplicationRoot, spec.Icon), icon, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(icon)
	spec.IconSHA256 = hex.EncodeToString(digest[:])
	spec.DesktopSHA256 = DesktopDigest(spec, ExpectedPaths(spec).ExecutableLink)
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(spec.IconDirectory, "48x48", "apps", "tarlink-"+spec.ID+".png")
	if paths.IconFile != want {
		t.Fatalf("icon path = %q, want %q", paths.IconFile, want)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != string(icon) {
		t.Fatalf("installed icon = %q, %v", got, err)
	}
	desktop, err := os.ReadFile(paths.DesktopEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desktop), "Icon=tarlink-blender\n") {
		t.Fatalf("desktop entry does not refer to the installed themed icon: %q", desktop)
	}
	if err := ValidateOwned(spec); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwned(spec); err != nil {
		t.Fatal(err)
	}
}

func TestIconSourceRejectsSymlinkAndDirectory(t *testing.T) {
	for _, name := range []string{"link", "directory"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			spec := testSpec(root)
			spec.Icon = name
			spec.IconSourceRoot = spec.ApplicationRoot
			spec.IconDirectory = filepath.Join(root, "icons")
			if err := os.MkdirAll(spec.ApplicationRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if name == "link" {
				if err := os.Symlink(t.TempDir(), filepath.Join(spec.ApplicationRoot, name)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Join(spec.ApplicationRoot, name), 0o700); err != nil {
				t.Fatal(err)
			}
			_, _, err := Ensure(spec)
			if err == nil {
				t.Fatal("unsafe icon source accepted")
			}
			if name == "link" && !errors.Is(err, filesystem.ErrSymlink) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDesktopFilePassesDesktopFileValidate(t *testing.T) {
	validator, err := exec.LookPath("desktop-file-validate")
	if err != nil {
		t.Skip("desktop-file-validate is not installed")
	}
	spec := testSpec(t.TempDir())
	path := filepath.Join(t.TempDir(), "tarlink-blender.desktop")
	if err := os.WriteFile(path, DesktopFile(spec, ExpectedPaths(spec).ExecutableLink), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(validator, path).CombinedOutput(); err != nil {
		t.Fatalf("desktop-file-validate: %v\n%s", err, output)
	}
}

func TestDesktopFileUsesTheCorrectEncodingForExecAndTryExec(t *testing.T) {
	spec := testSpec(t.TempDir())
	executable := filepath.Join(spec.LocalBinDirectory, `app name\with"quote`)
	content := string(DesktopFile(spec, executable))
	var execLine, tryExecLine string
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "Exec="):
			execLine = line
		case strings.HasPrefix(line, "TryExec="):
			tryExecLine = line
		}
	}

	execValue := strings.NewReplacer("\\", "\\\\\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%").Replace(executable)
	if want := `Exec="` + execValue + `"`; execLine != want {
		t.Fatalf("Exec = %q, want %q", execLine, want)
	}
	if want := "TryExec=" + strings.ReplaceAll(executable, "\\", "\\\\"); tryExecLine != want {
		t.Fatalf("TryExec = %q, want %q", tryExecLine, want)
	}
	if strings.HasPrefix(tryExecLine, `TryExec="`) {
		t.Fatal("TryExec contains shell-style quote delimiters")
	}

	validator, err := exec.LookPath("desktop-file-validate")
	if err == nil {
		path := filepath.Join(t.TempDir(), "tarlink-unusual-path.desktop")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(validator, path).CombinedOutput(); err != nil {
			t.Fatalf("desktop-file-validate: %v\n%s", err, output)
		}
	}
}

func TestDesktopFileUsesDeclaredOrExplicitFallbackIcon(t *testing.T) {
	spec := testSpec(t.TempDir())
	declared := string(DesktopFile(spec, ExpectedPaths(spec).ExecutableLink))
	if !strings.Contains(declared, "Icon=application-x-executable\n") {
		t.Fatalf("missing-icon desktop entry has unexpected icon: %q", declared)
	}
	spec.Icon = "share/icon.svg"
	withIcon := string(DesktopFile(spec, ExpectedPaths(spec).ExecutableLink))
	if !strings.Contains(withIcon, "Icon=tarlink-blender\n") {
		t.Fatalf("declared-icon desktop entry has unexpected icon: %q", withIcon)
	}
}

func TestUpdateReplacesIconAndRollbackRestoresIt(t *testing.T) {
	root := t.TempDir()
	previous := testSpec(root)
	previous.IconDirectory = filepath.Join(root, "data", "icons", "hicolor")
	previous.Icon = "icon.png"
	previous.IconSourceRoot = previous.ApplicationRoot
	if err := os.MkdirAll(previous.ApplicationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldContent := []byte("old icon")
	if err := os.WriteFile(filepath.Join(previous.ApplicationRoot, previous.Icon), oldContent, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256(oldContent)
	previous.IconSHA256 = hex.EncodeToString(oldDigest[:])
	previous.DesktopSHA256 = DesktopDigest(previous, ExpectedPaths(previous).ExecutableLink)
	oldPaths, _, err := Ensure(previous)
	if err != nil {
		t.Fatal(err)
	}

	next := previous
	next.Icon = "icon.svg"
	next.IconSourceRoot = previous.ApplicationRoot
	newContent := []byte("new icon")
	if err := os.WriteFile(filepath.Join(next.ApplicationRoot, next.Icon), newContent, 0o600); err != nil {
		t.Fatal(err)
	}
	newDigest := sha256.Sum256(newContent)
	next.IconSHA256 = hex.EncodeToString(newDigest[:])
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).ExecutableLink)
	newPaths, rollback, err := Update(next, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPaths.IconFile); !os.IsNotExist(err) {
		t.Fatalf("old icon remains: %v", err)
	}
	if got, err := os.ReadFile(newPaths.IconFile); err != nil || string(got) != string(newContent) {
		t.Fatalf("new icon = %q, %v", got, err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(oldPaths.IconFile); err != nil || string(got) != string(oldContent) {
		t.Fatalf("restored icon = %q, %v", got, err)
	}
}

func TestUpdateCanAddAndRemoveAnIcon(t *testing.T) {
	root := t.TempDir()
	previous := testSpec(root)
	previous.IconDirectory = filepath.Join(root, "data", "icons", "hicolor")
	previous.DesktopSHA256 = DesktopDigest(previous, ExpectedPaths(previous).ExecutableLink)
	if _, _, err := Ensure(previous); err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Icon = "icon.svg"
	next.IconSourceRoot = previous.ApplicationRoot
	content := []byte("icon")
	if err := os.MkdirAll(previous.ApplicationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous.ApplicationRoot, next.Icon), content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	next.IconSHA256 = hex.EncodeToString(digest[:])
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).ExecutableLink)
	added, undoAdd, err := Update(next, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(added.IconFile); err != nil {
		t.Fatal(err)
	}

	removed, undo, err := Update(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if removed.IconFile != "" {
		t.Fatalf("removed icon path = %q", removed.IconFile)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(added.IconFile); err != nil {
		t.Fatalf("removed icon was not restored: %v", err)
	}
	if err := undoAdd(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(added.IconFile); !os.IsNotExist(err) {
		t.Fatalf("added icon remains after rollback: %v", err)
	}
}
