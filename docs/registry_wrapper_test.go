package docs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryCheckWrapperUsesCompleteImmutableBaseline(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required for the registry wrapper")
	}
	root := t.TempDir()
	registryRoot := filepath.Join(root, "registry")
	for _, kind := range []string{"apps", "runtimes"} {
		path := filepath.Join(registryRoot, kind, "fixture")
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "manifest.yaml"), []byte("baseline\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", registryRoot}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("add", "apps", "runtimes")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "baseline")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(registryRoot, "runtimes", "fixture", "manifest.yaml"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	mock := `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  if [ "$1" = --old-root ]; then
    shift
    test "$(cat "$1/apps/fixture/manifest.yaml")" = baseline
    test "$(cat "$1/runtimes/fixture/manifest.yaml")" = baseline
    exit 0
  fi
  shift
done
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(mock), 0755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "scripts", "check-registry.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", script, registryRoot, "--changed-from", base)
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TARLINK_CHECK_IN_CONTAINER=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("complete runtime baseline missing: %v %s", err, output)
	}
}
