package main

import (
	"fmt"
	"os"

	"github.com/drobilica/tarlink/cli"
)

func main() {
	args := os.Args[1:]
	check := len(args) > 0 && args[0] == "--check"
	if check {
		args = args[1:]
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cli-reference [--check] PATH")
		os.Exit(2)
	}
	var err error
	if check {
		err = cli.CheckCLIReference(args[0])
	} else {
		err = os.WriteFile(args[0], []byte(cli.CLIReference()), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
