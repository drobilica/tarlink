package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// CLIReference renders the visible Cobra hierarchy without executing a command.
func CLIReference() string {
	r := Runner{Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}
	root := r.rootCommand(newProgressRenderer(io.Discard, false), io.Discard, io.Discard)
	return renderCLIReference(root)
}

func renderCLIReference(root *cobra.Command) string {
	root.InitDefaultHelpCmd()
	var b bytes.Buffer
	fmt.Fprintln(&b, "# CLI reference")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Generated from Cobra. Regenerate with `./scripts/update-cli-readme.sh`.")
	fmt.Fprintln(&b)
	var visit func(*cobra.Command, int)
	visit = func(c *cobra.Command, depth int) {
		if c.Hidden {
			return
		}
		indent := strings.Repeat("  ", depth)
		fmt.Fprintf(&b, "%s- `%s` — %s\n", indent, c.UseLine(), c.Short)
		children := c.Commands()
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			visit(child, depth+1)
		}
	}
	visit(root, 0)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "The `run` command is dispatched before Cobra so it can replace the process using only local installed state; it is documented by the main CLI help and is not part of the visible Cobra tree.")
	return b.String()
}

// CheckCLIReference compares the owned reference byte-for-byte with Cobra.
func CheckCLIReference(path string) error {
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, []byte(CLIReference())) {
		return fmt.Errorf("cli/README.md is missing or stale; run ./scripts/update-cli-readme.sh")
	}
	return nil
}
