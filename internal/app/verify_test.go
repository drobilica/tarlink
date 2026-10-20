package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/integrity"
	"github.com/drobilica/tarlink/internal/state"
)

type verifyNoNetwork struct{ calls int }

func (v *verifyNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	v.calls++
	return nil, errors.New("network forbidden")
}

func TestVerifyInstallModifyRepairAndReadOnly(t *testing.T) {
	ctx := context.Background()
	artifacts := map[string][]byte{"bravo": repairServiceArtifact(t, "approved")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	if _, err := core.Install(ctx, "bravo", nil); err != nil {
		t.Fatal(err)
	}
	before, err := state.LoadForApp(core.layout, "bravo")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := core.layout.PackagePath("bravo", before.Current, before.CurrentFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(filepath.Join(payload, integrity.Filename))
	if err != nil {
		t.Fatal(err)
	}
	originalTransport := core.installer.Client.HTTP.Transport
	denied := &verifyNoNetwork{}
	core.installer.Client.HTTP.Transport = denied
	for _, id := range []string{"bravo", ""} {
		report, err := core.Verify(ctx, id)
		if err != nil || report.Issues != 0 || len(report.Payloads) != 1 || report.Payloads[0].Status != "ok" {
			t.Fatalf("healthy: %+v %v", report, err)
		}
	}
	if err := os.WriteFile(filepath.Join(payload, "bravo"), []byte("self-updated"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := core.Verify(ctx, "")
	if err != nil || report.Issues != 1 || report.Payloads[0].Status != "changed" || len(report.Payloads[0].Changes) != 1 {
		t.Fatalf("changed: %+v %v", report, err)
	}
	after, _ := os.ReadFile(filepath.Join(payload, integrity.Filename))
	if string(after) != string(baseline) {
		t.Fatal("baseline rewritten")
	}
	content, _ := os.ReadFile(filepath.Join(payload, "bravo"))
	if string(content) != "self-updated" {
		t.Fatal("payload repaired implicitly")
	}
	if denied.calls != 0 {
		t.Fatalf("network requests: %d", denied.calls)
	}
	core.installer.Client.HTTP.Transport = originalTransport
	if _, err := core.Repair(ctx, "bravo", nil); err != nil {
		t.Fatal(err)
	}
	report, err = core.Verify(ctx, "bravo")
	if err != nil || report.Issues != 0 {
		t.Fatalf("after repair: %+v %v", report, err)
	}
}
func TestVerifyUnavailableBaselinesAndUnsafeRoots(t *testing.T) {
	for _, damage := range []string{"old-install", "identity", "symlink-payload", "symlink-parent", "bad-state"} {
		t.Run(damage, func(t *testing.T) {
			core, layout := doctorCore(t)
			writeDoctorState(t, layout, "fixture", "tar.gz", []state.Executable{{Name: "fixture", Path: "bin/run"}}, false)
			installed, err := state.LoadForApp(layout, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := layout.PackagePath("fixture", installed.Current, installed.CurrentFingerprint)
			switch damage {
			case "identity":
				if err := integrity.Record(context.Background(), payload, integrity.Identity{App: "other"}); err != nil {
					t.Fatal(err)
				}
			case "symlink-payload":
				if err := os.RemoveAll(payload); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), payload); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				parent := filepath.Join(layout.Apps, "fixture")
				if err := os.RemoveAll(parent); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), parent); err != nil {
					t.Fatal(err)
				}
			case "bad-state":
				if err := os.WriteFile(filepath.Join(layout.States, "fixture.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := core.Verify(context.Background(), "fixture")
			if err != nil || report.Issues != 1 || report.Payloads[0].Status != "unavailable" {
				t.Fatalf("unavailable: %+v %v", report, err)
			}
		})
	}
}
func TestVerifyAppImageSelfUpdateAndPortableData(t *testing.T) {
	core, layout := doctorCore(t)
	writeDoctorState(t, layout, "image", "appimage", []state.Executable{{Name: "image", Path: "appimage"}}, false)
	installed, err := state.LoadForApp(layout, "image")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := layout.PackagePath("image", installed.Current, installed.CurrentFingerprint)
	if err := integrity.Record(context.Background(), payload, integrity.Identity{App: installed.App, Version: installed.Current, Fingerprint: installed.CurrentFingerprint, AppImage: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(payload, "appimage.home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "appimage.home", "preferences"), []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report, err := core.Verify(context.Background(), "image"); err != nil || report.Issues != 0 {
		t.Fatalf("portable: %+v %v", report, err)
	}
	if err := os.WriteFile(filepath.Join(payload, "appimage"), []byte("new appimage"), 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := core.Verify(context.Background(), "image")
	if err != nil || report.Issues != 1 || report.Payloads[0].Status != "changed" || strings.Contains(report.Payloads[0].Detail, "corrupt") {
		t.Fatalf("self update: %+v %v", report, err)
	}
}
func TestVerifyAllContinuesAndCancellation(t *testing.T) {
	core, layout := doctorCore(t)
	if report, err := core.Verify(context.Background(), ""); err != nil || len(report.Payloads) != 0 {
		t.Fatalf("empty: %+v %v", report, err)
	}
	writeDoctorState(t, layout, "alpha", "tar.gz", []state.Executable{{Name: "alpha", Path: "bin/run"}}, false)
	writeDoctorState(t, layout, "bravo", "tar.gz", []state.Executable{{Name: "bravo", Path: "bin/run"}}, false)
	if err := os.WriteFile(filepath.Join(layout.States, "alpha.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := core.Verify(context.Background(), "")
	if err != nil || report.Issues != 2 || len(report.Payloads) != 2 || report.Payloads[1].App != "bravo" {
		t.Fatalf("all: %+v %v", report, err)
	}
	if _, err := core.Verify(context.Background(), "../unsafe"); CodeOf(err) != CodeInvalidArguments {
		t.Fatalf("invalid: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := core.Verify(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestVerifyEscapesInvalidStateNames(t *testing.T) {
	core, layout := doctorCore(t)
	if err := os.MkdirAll(layout.States, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.States, "bad\x1bname.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := core.Verify(context.Background(), "")
	if err != nil || report.Issues != 1 || strings.Contains(report.Payloads[0].App, "\x1b") {
		t.Fatalf("unsafe report: %+v %v", report, err)
	}
}
