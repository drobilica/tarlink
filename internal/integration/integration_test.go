package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/filesystem"
)

func testSpec(root string) Spec {
	spec := Spec{
		ID: "blender", Name: "Blender", Executables: []ExecutableSpec{{Name: "blender", Path: "bin/blender"}},
		ApplicationRoot:   filepath.Join(root, "apps", "blender"),
		LocalBinDirectory: filepath.Join(root, "bin"),
		DesktopDirectory:  filepath.Join(root, "applications"),
		DesktopEnabled:    true, DesktopCategories: []string{"Graphics"},
	}
	spec.DesktopSHA256 = DesktopDigest(spec, ExpectedPaths(spec).Executables[0].Link)
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
	spec := Spec{ID: "blender", Executables: []ExecutableSpec{{Name: "blender", Path: "bin/blender"}}, LocalBinDirectory: binDir}
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
	spec2 := Spec{ID: "blender", Executables: []ExecutableSpec{{Name: "blender", Path: "bin/blender"}}, LocalBinDirectory: binDir}
	path2 := plain + string(os.PathListSeparator) + binDir
	if conflicts := CheckPath(spec2, path2); len(conflicts) != 0 {
		t.Fatalf("non-executable conflicts = %#v, want none", conflicts)
	}

	// A missing bin directory is reported as not_in_path.
	missing := Spec{ID: "blender", Executables: []ExecutableSpec{{Name: "blender", Path: "bin/blender"}}, LocalBinDirectory: binDir}
	if conflicts := CheckPath(missing, earlier); len(conflicts) != 1 || conflicts[0].Type != "not_in_path" {
		t.Fatalf("missing bin conflicts = %#v, want not_in_path", conflicts)
	}

	// The bin directory itself is never reported as shadowing.
	if conflicts := CheckPath(spec, binDir); len(conflicts) != 0 {
		t.Fatalf("bin-only conflicts = %#v, want none", conflicts)
	}

	// Desktop-only executables do not need the local bin directory and must
	// not produce missing-PATH or shadowing warnings.
	createBinLink := false
	desktopOnly := Spec{ID: "desktop-only", Executables: []ExecutableSpec{{Name: "desktop-only", Path: "bin/desktop-only", CreateBinLink: &createBinLink}}, LocalBinDirectory: binDir}
	if conflicts := CheckPath(desktopOnly, earlier); len(conflicts) != 0 {
		t.Fatalf("desktop-only missing-bin conflicts = %#v, want none", conflicts)
	}
	if err := os.WriteFile(filepath.Join(earlier, "desktop-only"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if conflicts := CheckPath(desktopOnly, pathValue); len(conflicts) != 0 {
		t.Fatalf("desktop-only shadow conflicts = %#v, want none", conflicts)
	}

	// Empty or missing ID/bin is a no-op.
	if conflicts := CheckPath(Spec{}, ""); len(conflicts) != 0 {
		t.Fatalf("empty spec conflicts = %#v, want none", conflicts)
	}
}

func TestDesktopFileWithoutIconPreservesLauncherBehavior(t *testing.T) {
	spec := testSpec(t.TempDir())
	spec.Icon = ""
	spec.DesktopExecutable = "Apotris"
	spec.WorkingDirectory = true
	spec.DesktopCategories = []string{"Game"}
	content := string(DesktopFile(spec, "/home/test/bin/apotris"))
	for _, want := range []string{
		"Exec=Apotris\n",
		"Path=" + filepath.Join(spec.ApplicationRoot, "current") + "\n",
		"Categories=Game;\n",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("desktop file missing %q: %q", want, content)
		}
	}
	if strings.Contains(content, "Icon=") {
		t.Fatalf("iconless desktop file contains Icon entry: %q", content)
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
	for _, path := range []string{paths.Executables[0].Link, paths.DesktopEntry} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("integration remains at %s: %v", path, err)
		}
	}
}

func TestDesktopFileUsesDirectTargetAndApplicationRoot(t *testing.T) {
	spec := Spec{ID: "banjo", Name: "Banjo: Recompiled", ApplicationRoot: "/home/test/.local/share/tarlink/apps/banjo", DesktopEnabled: true, WorkingDirectory: true, DesktopCategories: []string{"Game"}, DesktopExecutable: "/home/test/.local/share/tarlink/apps/banjo/current/BanjoRecompiled"}
	content := string(DesktopFile(spec, "/home/test/.local/bin/BanjoRecompiled"))
	if strings.Contains(content, "/.local/bin/") || !strings.Contains(content, "Exec=/home/test/.local/share/tarlink/apps/banjo/current/BanjoRecompiled\n") || !strings.Contains(content, "TryExec=/home/test/.local/share/tarlink/apps/banjo/current/BanjoRecompiled\n") || !strings.Contains(content, "Path=/home/test/.local/share/tarlink/apps/banjo/current\n") {
		t.Fatalf("desktop content = %q", content)
	}
	if strings.Contains(content, `Exec="/`) {
		t.Fatalf("normal executable path unnecessarily quoted: %q", content)
	}
}

func TestEnsureRefusesUnrelatedFiles(t *testing.T) {
	for _, kind := range []string{"executable", "desktop"} {
		t.Run(kind, func(t *testing.T) {
			spec := testSpec(t.TempDir())
			paths := ExpectedPaths(spec)
			path := paths.Executables[0].Link
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
	if err := os.Remove(paths.Executables[0].Link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Executables[0].Link, []byte("mine"), 0o600); err != nil {
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
	if _, err := os.Lstat(paths.Executables[0].Link); err != nil {
		t.Fatalf("executable link was removed despite conflict: %v", err)
	}
}

func TestRemoveOwnedIsRetryableWhenIntegrationIsAlreadyMissing(t *testing.T) {
	spec := testSpec(t.TempDir())
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.Executables[0].Link); err != nil {
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
	if err := os.Remove(paths.Executables[0].Link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(paths.Executables[0].Link)); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Dir(paths.Executables[0].Link)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(spec.ApplicationRoot, "current", filepath.FromSlash(spec.Executables[0].Path)), filepath.Join(outside, spec.ID)); err != nil {
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
	spec.DesktopSHA256 = DesktopDigest(spec, ExpectedPaths(spec).Executables[0].Link)
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

func TestIconLifecyclePlacesRemoteRasterAtInferredSize(t *testing.T) {
	root := t.TempDir()
	spec := testSpec(root)
	spec.IconDirectory = filepath.Join(root, "data", "icons", "hicolor")
	spec.Icon = ".tarlink-icon.png"
	spec.IconSize = 512
	spec.IconSourceRoot = spec.ApplicationRoot
	if err := os.MkdirAll(spec.ApplicationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	icon := []byte("remote png fixture")
	if err := os.WriteFile(filepath.Join(spec.ApplicationRoot, spec.Icon), icon, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(icon)
	spec.IconSHA256 = hex.EncodeToString(digest[:])
	spec.DesktopSHA256 = DesktopDigest(spec, ExpectedPaths(spec).Executables[0].Link)
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(spec.IconDirectory, "512x512", "apps", "tarlink-"+spec.ID+".png")
	if paths.IconFile != want {
		t.Fatalf("icon path = %q, want %q", paths.IconFile, want)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != string(icon) {
		t.Fatalf("installed icon = %q, %v", got, err)
	}
	if err := ValidateOwned(spec); err != nil {
		t.Fatalf("ValidateOwned() error = %v", err)
	}
	if err := RemoveOwned(spec); err != nil {
		t.Fatal(err)
	}
}

func TestIconSizeDerivesExtensionOrExplicitRasterDirectory(t *testing.T) {
	root := t.TempDir()
	spec := testSpec(root)
	spec.IconDirectory = filepath.Join(root, "icons")
	sizeDir := func(got string) string { return filepath.Base(filepath.Dir(filepath.Dir(got))) }
	spec.Icon = "share/icon.svg"
	if got := sizeDir(ExpectedPaths(spec).IconFile); got != "scalable" {
		t.Fatalf("svg icon directory = %q", got)
	}
	spec.Icon = "share/icon.png"
	if got := sizeDir(ExpectedPaths(spec).IconFile); got != "48x48" {
		t.Fatalf("raster icon directory = %q", got)
	}
	spec.IconSize = 256
	if got := sizeDir(ExpectedPaths(spec).IconFile); got != "256x256" {
		t.Fatalf("remote raster icon directory = %q", got)
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
	if err := os.WriteFile(path, DesktopFile(spec, ExpectedPaths(spec).Executables[0].Link), 0o644); err != nil {
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

func TestDesktopFileOmitsMissingIconAndUsesDeclaredIcon(t *testing.T) {
	spec := testSpec(t.TempDir())
	declared := string(DesktopFile(spec, ExpectedPaths(spec).Executables[0].Link))
	if strings.Contains(declared, "Icon=") {
		t.Fatalf("missing-icon desktop entry has Icon entry: %q", declared)
	}
	spec.Icon = "share/icon.svg"
	withIcon := string(DesktopFile(spec, ExpectedPaths(spec).Executables[0].Link))
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
	previous.DesktopSHA256 = DesktopDigest(previous, ExpectedPaths(previous).Executables[0].Link)
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
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)
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
	previous.DesktopSHA256 = DesktopDigest(previous, ExpectedPaths(previous).Executables[0].Link)
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
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)
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

func TestUpdateCanToggleDesktopIntegrationAndRollback(t *testing.T) {
	root := t.TempDir()
	previous := testSpec(root)
	previous.DesktopEnabled = false
	previous.DesktopSHA256 = ""
	if _, _, err := Ensure(previous); err != nil {
		t.Fatal(err)
	}

	next := previous
	next.DesktopEnabled = true
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)
	paths, rollback, err := Update(next, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.DesktopEntry); err != nil {
		t.Fatalf("desktop entry was not created: %v", err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(paths.DesktopEntry); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains after rollback: %v", err)
	}

	if _, _, err := Update(next, previous); err != nil {
		t.Fatal(err)
	}
	removed, undo, err := Update(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(removed.DesktopEntry); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains after disable: %v", err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed.DesktopEntry); err != nil {
		t.Fatalf("desktop entry was not restored: %v", err)
	}
}

func TestUpdateDesktopEnableRejectsConflict(t *testing.T) {
	root := t.TempDir()
	previous := testSpec(root)
	previous.DesktopEnabled = false
	previous.DesktopSHA256 = ""
	if _, _, err := Ensure(previous); err != nil {
		t.Fatal(err)
	}
	next := previous
	next.DesktopEnabled = true
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)
	path := ExpectedPaths(next).DesktopEntry
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user-owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Update(next, previous); !errors.Is(err, ErrConflict) {
		t.Fatalf("Update error = %v, want conflict", err)
	}
}

func observePublication(t *testing.T) func() []string {
	t.Helper()
	original := afterPublication
	order := []string{}
	afterPublication = func(stage string) { order = append(order, stage) }
	t.Cleanup(func() { afterPublication = original })
	return func() []string { return order }
}

func iconSpec(t *testing.T, root string) Spec {
	t.Helper()
	spec := testSpec(root)
	spec.IconDirectory = filepath.Join(root, "data", "icons", "hicolor")
	spec.Icon = "share/icon.png"
	spec.IconSourceRoot = spec.ApplicationRoot
	if err := os.MkdirAll(filepath.Join(spec.ApplicationRoot, "share"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spec.ApplicationRoot, spec.Icon), []byte("png fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("png fixture"))
	spec.IconSHA256 = hex.EncodeToString(digest[:])
	spec.DesktopSHA256 = DesktopDigest(spec, ExpectedPaths(spec).Executables[0].Link)
	return spec
}

func TestEnsurePublishesIconBeforeDesktopEntry(t *testing.T) {
	spec := iconSpec(t, t.TempDir())
	order := observePublication(t)
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := order(); len(got) != 2 || got[0] != "icon" || got[1] != "desktop-entry" {
		t.Fatalf("publication order = %v, want [icon desktop-entry]", got)
	}
	if got, err := os.ReadFile(paths.IconFile); err != nil || string(got) != "png fixture" {
		t.Fatalf("installed icon = %q, %v", got, err)
	}
	if _, err := os.Stat(paths.DesktopEntry); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDesktopConflictLeavesNoCreatedIcon(t *testing.T) {
	spec := iconSpec(t, t.TempDir())
	paths := ExpectedPaths(spec)
	if err := os.MkdirAll(filepath.Dir(paths.DesktopEntry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DesktopEntry, []byte("user owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Ensure(spec); !errors.Is(err, ErrConflict) {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := os.Lstat(paths.IconFile); !os.IsNotExist(err) {
		t.Fatalf("created icon remains after desktop conflict: %v", err)
	}
	if content, err := os.ReadFile(paths.DesktopEntry); err != nil || string(content) != "user owned" {
		t.Fatalf("unrelated desktop entry changed: %q, %v", content, err)
	}
}

func TestUpdatePublishesIconBeforeDesktopEntryWhenAddingIcon(t *testing.T) {
	root := t.TempDir()
	previous := testSpec(root)
	previous.DesktopSHA256 = DesktopDigest(previous, ExpectedPaths(previous).Executables[0].Link)
	if _, _, err := Ensure(previous); err != nil {
		t.Fatal(err)
	}
	next := iconSpec(t, root)
	order := observePublication(t)
	paths, _, err := Update(next, previous)
	if err != nil {
		t.Fatal(err)
	}
	if got := order(); len(got) != 2 || got[0] != "icon" || got[1] != "desktop-entry" {
		t.Fatalf("publication order = %v, want [icon desktop-entry]", got)
	}
	if got, err := os.ReadFile(paths.IconFile); err != nil || string(got) != "png fixture" {
		t.Fatalf("installed icon = %q, %v", got, err)
	}
	desktop, err := os.ReadFile(paths.DesktopEntry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(desktop), "Icon=tarlink-"+next.ID+"\n") {
		t.Fatalf("updated desktop entry does not reference the themed icon: %q", desktop)
	}
}

func TestUpdateKeepsIdenticalIconAndDesktopEntry(t *testing.T) {
	spec := iconSpec(t, t.TempDir())
	if _, _, err := Ensure(spec); err != nil {
		t.Fatal(err)
	}
	paths := ExpectedPaths(spec)
	iconBefore, err := os.ReadFile(paths.IconFile)
	if err != nil {
		t.Fatal(err)
	}
	desktopBefore, err := os.ReadFile(paths.DesktopEntry)
	if err != nil {
		t.Fatal(err)
	}
	updated, rollback, err := Update(spec, spec)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IconFile != paths.IconFile || updated.DesktopEntry != paths.DesktopEntry {
		t.Fatalf("updated paths = %#v, want unchanged", updated)
	}
	if got, err := os.ReadFile(updated.IconFile); err != nil || string(got) != string(iconBefore) {
		t.Fatalf("icon changed during identical update: %q, %v", got, err)
	}
	if got, err := os.ReadFile(updated.DesktopEntry); err != nil || string(got) != string(desktopBefore) {
		t.Fatalf("desktop entry changed during identical update: %q, %v", got, err)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwned(spec); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOwnedRemovesOnlyDesktopEntryAndIcon(t *testing.T) {
	root := t.TempDir()
	spec := iconSpec(t, root)
	paths, _, err := Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedDesktop := filepath.Join(filepath.Dir(paths.DesktopEntry), "unrelated.desktop")
	if err := os.WriteFile(unrelatedDesktop, []byte("[Desktop Entry]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelatedIcon := filepath.Join(filepath.Dir(paths.IconFile), "unrelated.png")
	if err := os.WriteFile(unrelatedIcon, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwned(spec); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Executables[0].Link, paths.DesktopEntry, paths.IconFile} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned integration remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{unrelatedDesktop, unrelatedIcon} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("unrelated file %s was removed: %v", path, err)
		}
	}
}

func TestJoinRollbackKeepsCauseAndUndoFailure(t *testing.T) {
	cause := errors.New("cause")
	if got := JoinRollback(cause, nil); got != cause {
		t.Fatalf("JoinRollback(cause, nil) = %v, want cause unchanged", got)
	}
	undo := errors.Join(errors.New("first undo"), errors.New("second undo"))
	got := JoinRollback(cause, undo)
	if !errors.Is(got, cause) || !errors.Is(got, undo) || !errors.Is(got, ErrRollbackIncomplete) {
		t.Fatalf("JoinRollback(cause, undo) = %v, want cause, undo and ErrRollbackIncomplete", got)
	}
	want := "cause\n  " + ErrRollbackIncomplete.Error() + ": first undo\n  second undo"
	if got.Error() != want {
		t.Fatalf("message = %q, want %q", got.Error(), want)
	}
	outer := JoinRollback(fmt.Errorf("activate: %w", got), errors.New("outer undo"))
	var rollback *RollbackError
	if !errors.As(outer, &rollback) || rollback.Cause() != cause {
		t.Fatalf("Cause() of nested rollback error = %v, want the original cause", rollback)
	}
}

func TestUpdateRollsBackIconWhenDesktopReplacementFails(t *testing.T) {
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
	previous.DesktopSHA256 = DesktopDigest(previous, ExpectedPaths(previous).Executables[0].Link)
	oldPaths, _, err := Ensure(previous)
	if err != nil {
		t.Fatal(err)
	}

	next := previous
	next.IconSourceRoot = filepath.Join(root, "next")
	if err := os.MkdirAll(next.IconSourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	newContent := []byte("new icon")
	if err := os.WriteFile(filepath.Join(next.IconSourceRoot, next.Icon), newContent, 0o600); err != nil {
		t.Fatal(err)
	}
	newDigest := sha256.Sum256(newContent)
	next.IconSHA256 = hex.EncodeToString(newDigest[:])
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)

	// Changing the name changes the desktop entry, which is published after
	// the icon. A read-only applications directory makes that later step fail
	// once the icon was already replaced, so Update must undo the icon itself.
	next.Name = "Blender Next"
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(next.DesktopDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(next.DesktopDirectory, 0o700) })
	paths, cleanup, err := Update(next, previous)
	if err == nil {
		t.Fatalf("Update succeeded with a read-only desktop directory: %+v", paths)
	}
	if cleanup != nil {
		t.Fatal("failed Update returned a cleanup function")
	}
	if errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("Update error = %v, want a complete rollback", err)
	}
	if got, readErr := os.ReadFile(oldPaths.IconFile); readErr != nil || string(got) != string(oldContent) {
		t.Fatalf("icon after failed Update = %q, %v; want the previous icon restored", got, readErr)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
}

func TestUpdateRemovesNewBinLinkWhenALaterStepFails(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	previous := testSpec(root)
	if _, _, err := Ensure(previous); err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Name = "Blender Next"
	next.Executables = append(append([]ExecutableSpec(nil), previous.Executables...), ExecutableSpec{Name: "blender-cli", Path: "bin/blender-cli"})
	next.DesktopSHA256 = DesktopDigest(next, ExpectedPaths(next).Executables[0].Link)
	newLink := ExpectedPaths(next).Executables[1].Link
	if err := os.Chmod(next.DesktopDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(next.DesktopDirectory, 0o700) })
	if _, _, err := Update(next, previous); err == nil || errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("Update error = %v, want a failure with a complete rollback", err)
	}
	if _, err := os.Lstat(newLink); !os.IsNotExist(err) {
		t.Fatalf("new bin link remains after failed Update: %v", err)
	}
	if _, err := os.Lstat(ExpectedPaths(previous).Executables[0].Link); err != nil {
		t.Fatalf("existing bin link was removed: %v", err)
	}
}

func TestEnsureReportsFailedCleanup(t *testing.T) {
	skipIfRoot(t)
	spec := iconSpec(t, t.TempDir())
	// A user-owned file at the desktop entry path makes the last step fail.
	if err := os.MkdirAll(spec.DesktopDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ExpectedPaths(spec).DesktopEntry, []byte("user-owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spec.LocalBinDirectory, 0o700) })
	original := afterPublication
	afterPublication = func(stage string) {
		if stage == "icon" {
			if err := os.Chmod(spec.LocalBinDirectory, 0o500); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { afterPublication = original })
	_, cleanup, err := Ensure(spec)
	if err == nil || cleanup != nil {
		t.Fatalf("Ensure error = %v, want failure without cleanup", err)
	}
	if !errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("Ensure error = %v, want ErrRollbackIncomplete when a created link cannot be removed", err)
	}
}

func TestSwitchIconUndoReportsFailureToRemoveNewIcon(t *testing.T) {
	root := t.TempDir()
	current := iconSpec(t, root)
	if _, _, err := Ensure(current); err != nil {
		t.Fatal(err)
	}
	next := current
	next.Icon = "share/icon.svg"
	content := []byte("<svg/>")
	if err := os.WriteFile(filepath.Join(next.ApplicationRoot, next.Icon), content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	next.IconSHA256 = hex.EncodeToString(digest[:])
	undo, err := SwitchIcon(current, next)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the new icon with a non-empty directory so removing it fails
	// while restoring the previous icon still succeeds.
	nextIcon := ExpectedPaths(next).IconFile
	if err := os.Remove(nextIcon); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(nextIcon, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err == nil {
		t.Fatal("icon undo reported success although the new icon could not be removed")
	}
	if got, err := os.ReadFile(ExpectedPaths(current).IconFile); err != nil || string(got) != "png fixture" {
		t.Fatalf("previous icon after undo = %q, %v", got, err)
	}
}

func TestDetachOwnedReportsFileLeftDetached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry")
	if err := os.WriteFile(path, []byte("user file"), 0o644); err != nil {
		t.Fatal(err)
	}
	var detached string
	rejected := errors.New("not owned")
	err := detachOwned(path, func(temporary string) error {
		detached = temporary
		// Something recreates the path before the file can be moved back.
		if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
		return rejected
	})
	if !errors.Is(err, rejected) || !errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("detachOwned error = %v, want the rejection and ErrRollbackIncomplete", err)
	}
	if got, readErr := os.ReadFile(detached); readErr != nil || string(got) != "user file" || !strings.Contains(err.Error(), detached) {
		t.Fatalf("detached file %q = %q, %v; error %v should name it", detached, got, readErr, err)
	}
}
