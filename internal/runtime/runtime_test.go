package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

func runtimeArchive(t *testing.T, entries []tar.Header) string {
	t.Helper()
	var data bytes.Buffer
	writer, err := xz.NewWriter(&data)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(writer)
	for _, header := range entries {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write(make([]byte, header.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.tar.xz")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractValveDeploymentAllowsContainedRelativeLink(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "SteamLinuxRuntime_4/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755}, {Name: "SteamLinuxRuntime_4/link", Typeflag: tar.TypeSymlink, Linkname: "_v2-entry-point"}})
	destination := t.TempDir()
	if err := extractValveDeployment(context.Background(), archive, destination); err != nil {
		t.Fatal(err)
	}
	if err := validateDeployment(filepath.Join(destination, "SteamLinuxRuntime_4")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractValveDeploymentRejectsEscapingLink(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "SteamLinuxRuntime_4/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}})
	if err := extractValveDeployment(context.Background(), archive, t.TempDir()); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
}
