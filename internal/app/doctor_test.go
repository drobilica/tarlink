package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/state"
)

const testStateFingerprint = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func doctorCore(t *testing.T) (*Core, filesystem.Layout) {
	t.Helper()
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return &Core{layout: layout, goarch: "amd64"}, layout
}

func writeDoctorState(t *testing.T, layout filesystem.Layout, id, artifact string, executables []state.Executable, desktop bool) {
	t.Helper()
	root := filepath.Join(layout.Apps, id)
	packagePath, err := layout.PackagePath(id, "1.0", testStateFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	version := packagePath
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, executable := range executables {
		path := filepath.Join(version, filepath.FromSlash(executable.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/false\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if artifact == "appimage" {
		path := filepath.Join(version, "appimage")
		header := make([]byte, 64)
		copy(header[0:4], []byte{0x7f, 'E', 'L', 'F'})
		header[4], header[5], header[6] = 2, 1, 1
		copy(header[8:11], []byte{'A', 'I', 2})
		header[16], header[17] = 2, 0
		header[18], header[19] = 0x3e, 0
		if err := os.WriteFile(path, header, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	currentTarget, err := filepath.Rel(root, packagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(currentTarget, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.Bin, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := integration.Spec{ID: id, Name: id, ApplicationRoot: root, LocalBinDirectory: layout.Bin, DesktopDirectory: layout.Desktop, IconDirectory: layout.Icons, DesktopEnabled: desktop, DesktopCategories: []string{"Utility"}, Executables: make([]integration.ExecutableSpec, 0, len(executables))}
	for _, executable := range executables {
		spec.Executables = append(spec.Executables, integration.ExecutableSpec{Name: executable.Name, Path: executable.Path})
	}
	if desktop {
		spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).Executables[0].Link)
	}
	paths, _, err := integration.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	desktopEntry := ""
	if desktop {
		desktopEntry = paths.DesktopEntry
	}
	value := state.State{Schema: state.Schema, App: id, Current: "1.0", CurrentFingerprint: testStateFingerprint, Channel: "stable", Artifact: artifact, Executables: executables, DesktopEnabled: desktop, Integration: state.Integration{DesktopEntry: desktopEntry, DesktopSHA256: spec.DesktopSHA256}}
	for _, executable := range executables {
		value.Integration.Executables = append(value.Integration.Executables, state.ExecutableIntegration{Name: executable.Name, Path: executable.Path, Link: filepath.Join(layout.Bin, executable.Name), Target: filepath.Join(root, "current", filepath.FromSlash(executable.Path))})
	}
	if err := state.WriteForApp(layout, value); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorHealthyArchiveMultipleExecutablesAndAppImage(t *testing.T) {
	core, layout := doctorCore(t)
	writeDoctorState(t, layout, "multi", "tar.gz", []state.Executable{{Name: "one", Path: "bin/one"}, {Name: "two", Path: "bin/two"}}, false)
	writeDoctorState(t, layout, "image", "appimage", []state.Executable{{Name: "image", Path: "appimage"}}, false)
	report, err := core.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors != 0 {
		t.Fatalf("healthy doctor errors=%d report=%+v", report.Errors, report)
	}
}

func TestDoctorReportsMissingTargetAndDesktopWithoutMutation(t *testing.T) {
	core, layout := doctorCore(t)
	writeDoctorState(t, layout, "broken", "tar.gz", []state.Executable{{Name: "broken", Path: "bin/broken"}}, true)
	target := filepath.Join(layout.Apps, "broken", "1.0", ".tarlink-package-"+testStateFingerprint, "bin", "broken")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(layout.Bin, "broken")
	beforeLink, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-broken.desktop")
	if err := os.Remove(desktop); err != nil {
		t.Fatal(err)
	}
	report, err := core.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors < 2 {
		t.Fatalf("errors=%d, want missing target and desktop", report.Errors)
	}
	afterLink, err := os.Readlink(link)
	if err != nil || afterLink != beforeLink {
		t.Fatalf("doctor changed link: before=%q after=%q err=%v", beforeLink, afterLink, err)
	}
	if _, err := os.Stat(filepath.Join(layout.States, "broken.json")); err != nil {
		t.Fatalf("doctor changed state: %v", err)
	}
}

func TestDoctorReportsMalformedStateAndPathWarning(t *testing.T) {
	core, layout := doctorCore(t)
	if err := os.WriteFile(filepath.Join(layout.States, "bad.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := core.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors == 0 {
		t.Fatal("malformed state was not an error")
	}
	// A missing ~/.local/bin is an advisory warning when no applications exist.
	if err := os.Remove(layout.Bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(layout.States, "bad.json")); err != nil {
		t.Fatal(err)
	}
	report, err = core.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors != 0 || report.Warnings == 0 {
		t.Fatalf("warning-only report=%+v", report)
	}
}

func TestDoctorValidatesRetainedRemoteIcon(t *testing.T) {
	core, layout := doctorCore(t)
	icon := []byte("retained 512 icon")
	digest := sha256.Sum256(icon)
	root := filepath.Join(layout.Apps, "remote")
	version, err := layout.PackagePath("remote", "1.0", testStateFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, ".tarlink-icon.png"), icon, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(version, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "bin", "run"), []byte("#!/bin/false\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	currentTarget, err := filepath.Rel(root, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(currentTarget, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.Bin, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := integration.Spec{ID: "remote", Name: "remote", ApplicationRoot: root, LocalBinDirectory: layout.Bin, DesktopDirectory: layout.Desktop, IconDirectory: layout.Icons, DesktopEnabled: true, DesktopCategories: []string{"Utility"}, Icon: ".tarlink-icon.png", IconSize: 512, IconSHA256: hex.EncodeToString(digest[:]), IconSourceRoot: version, Executables: []integration.ExecutableSpec{{Name: "run", Path: "bin/run"}}}
	spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).Executables[0].Link)
	paths, _, err := integration.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(filepath.Dir(paths.IconFile))) != "512x512" {
		t.Fatalf("doctor fixture icon path = %q", paths.IconFile)
	}
	value := state.State{Schema: state.Schema, App: "remote", Current: "1.0", CurrentFingerprint: testStateFingerprint, Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "run", Path: "bin/run"}}, DesktopEnabled: true, Integration: state.Integration{
		DesktopEntry: paths.DesktopEntry, DesktopSHA256: spec.DesktopSHA256,
		IconFile: paths.IconFile, IconSHA256: spec.IconSHA256, IconSize: 512, IconSource: ".tarlink-icon.png",
		Executables: []state.ExecutableIntegration{{Name: "run", Path: "bin/run", Link: filepath.Join(layout.Bin, "run"), Target: filepath.Join(root, "current", "bin", "run")}},
	}}
	if err := state.WriteForApp(layout, value); err != nil {
		t.Fatal(err)
	}
	report, err := core.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors != 0 {
		t.Fatalf("healthy remote-icon doctor errors=%d report=%+v", report.Errors, report)
	}
	// A tampered themed icon copy must be reported as an integration error.
	if err := os.WriteFile(paths.IconFile, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = core.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Errors == 0 {
		t.Fatal("tampered themed icon was not reported")
	}
}
