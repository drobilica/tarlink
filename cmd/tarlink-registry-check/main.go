package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/registrycheck"
)

func main() {
	flags := flag.NewFlagSet("tarlink-registry-check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	appID := flags.String("app", "", "materialize one application")
	all := flags.Bool("all-artifacts", false, "materialize every application artifact")
	oldRoot := flags.String("old-root", "", "previous registry tree for semantic change detection")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 1 || *appID != "" && *oldRoot != "" || *all && (*appID != "" || *oldRoot != "") {
		flags.Usage()
		os.Exit(2)
	}
	root := flags.Arg(0)
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		fail(err)
	}
	selection, err := registrycheck.Select(root, *appID, *all, *oldRoot)
	if err != nil {
		fail(err)
	}
	if *appID == "" && !*all && *oldRoot == "" {
		fmt.Println("registry structure is valid")
		return
	}
	for _, item := range selection.Items {
		fmt.Printf("materializing %s %s/%s\n", item.ID, item.Platform.OS, item.Platform.Arch)
		if err := registrycheck.Materialize(context.Background(), item); err != nil {
			fail(err)
		}
	}
	fmt.Printf("registry structure is valid; materialized %d artifact(s)\n", len(selection.Items))
}

func fail(err error) {
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "tarlink-registry-check:", err)
	}
	os.Exit(1)
}
