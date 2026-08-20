package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckInstallPathDetectsShadowing(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	layout := core.layout
	if err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	earlier := filepath.Join(home, "earlier-bin")
	if err := os.MkdirAll(earlier, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(earlier, "fixture"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", earlier+string(os.PathListSeparator)+layout.Bin)
	conflicts, err := core.CheckInstallPath("fixture")
	if err != nil {
		t.Fatalf("CheckInstallPath() error = %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Type != "shadowed" || conflicts[0].Candidate != filepath.Join(earlier, "fixture") {
		t.Fatalf("conflicts = %#v, want one shadowed conflict", conflicts)
	}

	t.Setenv("PATH", layout.Bin)
	conflicts, err = core.CheckInstallPath("fixture")
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("clean PATH conflicts = %#v, error = %v", conflicts, err)
	}

	if _, err := core.CheckInstallPath("../unsafe"); CodeOf(err) != CodeInvalidArguments {
		t.Fatalf("invalid id error = %v, code = %q", err, CodeOf(err))
	}
}
