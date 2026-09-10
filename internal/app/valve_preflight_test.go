package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/manifest"
	valveruntime "github.com/drobilica/tarlink/internal/runtime"
	"github.com/drobilica/tarlink/internal/state"
)

func TestValveRuntimePreflight(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("Valve runtime preflight requires Linux")
	}
	archivePath := os.Getenv("TARLINK_REAL_VALVE_ARTIFACT")
	if archivePath == "" {
		t.Skip("set TARLINK_REAL_VALVE_ARTIFACT for the explicit Valve execution preflight")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		t.Fatal(err)
	}
	layout := preflightLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	client := &download.Client{HTTP: &http.Client{Transport: preflightTransport{file: archive, size: info.Size()}}}
	runtime := preflightRuntime()
	if _, _, err := valveruntime.Ensure(context.Background(), layout, client, runtime, nil); err != nil {
		t.Fatal(err)
	}

	const fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	packageRoot, err := layout.PackagePath("fixture", "1.0", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable("/bin/echo", filepath.Join(packageRoot, "fixture")); err != nil {
		t.Fatal(err)
	}
	noBinLink := false
	if err := state.Write(filepath.Join(layout.States, "fixture.json"), state.State{Schema: state.Schema, App: "fixture", Current: "1.0", CurrentFingerprint: fingerprint, Channel: "stable", Artifact: "tar.xz", Runtime: runtime, Executables: []state.Executable{{Name: "fixture", Path: "fixture", CreateBinLink: &noBinLink}}, Integration: state.Integration{Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Target: filepath.Join(layout.Apps, "fixture", "current", "fixture"), CreateBinLink: &noBinLink}}}}); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, client)}
	arguments := []string{"space value", `quote"value`, "-leading", "unicode-zażółć"}
	launch, err := core.PrepareRun("fixture", arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(launch.Program, "_v2-entry-point") || len(launch.Args) < 5 || launch.Args[1] != "--verb=waitforexitandrun" || launch.Args[2] != "--" {
		t.Fatalf("unexpected Valve launch closure: %#v", launch)
	}
	command := exec.Command(launch.Program, launch.Args[1:]...)
	command.Dir = launch.Dir
	command.Env = launch.Env
	var output, diagnostics bytes.Buffer
	command.Stdout = &output
	command.Stderr = &diagnostics
	err = command.Run()
	if err != nil {
		t.Fatalf("Valve execution failed: %v: %s", err, diagnostics.String())
	}
	if output.String() != "space value quote\"value -leading unicode-zażółć\n" {
		t.Fatalf("unexpected Valve argument output %q", output.String())
	}
	if _, err := os.Stat(launch.Env[len(launch.Env)-1][len("PRESSURE_VESSEL_VARIABLE_DIR="):]); err != nil {
		t.Fatal(err)
	}
	falseRoot, err := layout.PackagePath("fixture-false", "1.0", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(falseRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable("/bin/false", filepath.Join(falseRoot, "fixture-false")); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(filepath.Join(layout.States, "fixture-false.json"), state.State{Schema: state.Schema, App: "fixture-false", Current: "1.0", CurrentFingerprint: fingerprint, Channel: "stable", Artifact: "tar.xz", Runtime: runtime, Executables: []state.Executable{{Name: "fixture-false", Path: "fixture-false", CreateBinLink: &noBinLink}}, Integration: state.Integration{Executables: []state.ExecutableIntegration{{Name: "fixture-false", Path: "fixture-false", Target: filepath.Join(layout.Apps, "fixture-false", "current", "fixture-false"), CreateBinLink: &noBinLink}}}}); err != nil {
		t.Fatal(err)
	}
	falseLaunch, err := core.PrepareRun("fixture-false", nil)
	if err != nil {
		t.Fatal(err)
	}
	falseCommand := exec.Command(falseLaunch.Program, falseLaunch.Args[1:]...)
	falseCommand.Dir = falseLaunch.Dir
	falseCommand.Env = falseLaunch.Env
	if err := falseCommand.Run(); err == nil {
		t.Fatal("Valve execution swallowed non-zero child status")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("Valve execution returned %v, want child exit status 1", err)
	}
}

func preflightRuntime() *manifest.Runtime {
	return &manifest.Runtime{Schema: manifest.RuntimeSchemaV1, ID: "steam-linux-runtime-4", Kind: manifest.RuntimeKindSteamLinuxRuntime, Version: "4.0.20260805.254769", Platform: manifest.Platform{OS: "linux", Arch: "amd64"}, Artifact: manifest.RuntimeArtifact{URL: "https://repo.steampowered.com/steamrt4/images/4.0.20260805.254769/SteamLinuxRuntime_4.tar.xz", Archive: "tar.xz", Verification: manifest.Verification{Algorithm: "sha256", Digest: "3226d8234e7c0542ee767837832bfb1dad5e5e2dc944ec97eb221b437f6b9349", Source: "https://repo.steampowered.com/steamrt4/images/4.0.20260805.254769/SHA256SUMS"}}, Interface: manifest.RuntimeInterfaceValveV2}
}

func copyExecutable(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o755)
}

type preflightTransport struct {
	file *os.File
	size int64
}

func (r preflightTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(r.file), Header: make(http.Header), Request: request, ContentLength: r.size}, nil
}

func preflightLayout(t *testing.T) filesystem.Layout {
	t.Helper()
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, func(name string) string {
		switch name {
		case "XDG_DATA_HOME":
			return filepath.Join(home, "data")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "XDG_CACHE_HOME":
			return filepath.Join(home, "cache")
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return layout
}
