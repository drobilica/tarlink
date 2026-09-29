package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCLIReferenceUsesVisibleCobraHierarchyWithoutExecution(t *testing.T) {
	root := (Runner{Stdout: io.Discard, Stderr: io.Discard}).rootCommand(newProgressRenderer(io.Discard, false), io.Discard, io.Discard)
	root.AddCommand(&cobra.Command{Use: "hidden-reference-fixture", Hidden: true})
	root.AddCommand(&cobra.Command{Use: "reference-fixture <input>", Short: "A fixture command", RunE: func(*cobra.Command, []string) error { panic("reference executed a command") }})
	generated := renderCLIReference(root)
	if strings.Contains(generated, "hidden-reference-fixture") || !strings.Contains(generated, "tarlink reference-fixture <input>") {
		t.Fatalf("visible tree mismatch: %s", generated)
	}
	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		if command.Short == "" || !strings.Contains(generated, fmt.Sprintf("`%s`", command.UseLine())) {
			t.Errorf("reference omits command or description: %s", command.CommandPath())
		}
		for _, child := range command.Commands() {
			visit(child)
		}
	}
	visit(root)
	if CLIReference() != CLIReference() || CLIReference() == generated {
		t.Fatal("reference is nondeterministic or ignores Cobra changes")
	}
}

func TestCLIReferenceFreshnessIsByteExactAndActionable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte(CLIReference()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckCLIReference(path); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{CLIReference() + "\n", strings.Replace(CLIReference(), "candidates discover", "candidates stale", 1)} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := CheckCLIReference(path); err == nil || !strings.Contains(err.Error(), "./scripts/update-cli-readme.sh") {
			t.Fatalf("stale reference lacked actionable error: %v", err)
		}
	}
}
