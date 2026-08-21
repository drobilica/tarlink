package docs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drobilica/tarlink/internal/manifest"
)

func TestCanonicalManifestExampleParsesAsDocumented(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "schema", "manifest-v2.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := manifest.ParseBytes(data)
	if err != nil {
		t.Fatalf("canonical manifest example does not parse: %v", err)
	}
	if value.ID != "blender" || value.Platform.OS != "linux" || value.Platform.Arch != "amd64" {
		t.Fatalf("canonical manifest identity/platform = %s %s/%s", value.ID, value.Platform.OS, value.Platform.Arch)
	}
	if value.Release.Verification.Algorithm != "sha256" || len(value.Release.Verification.Digest) != 64 {
		t.Fatalf("canonical manifest verification = %#v", value.Release.Verification)
	}
}
