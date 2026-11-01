package cli

import (
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/spf13/cobra"
)

func (r Runner) registryUpdateCommand() *cobra.Command {
	usage := "usage: tarlink registry update [-n <count>] [--json] | tarlink registry update --apply --registry <path> [--dry-run] [--json]"
	var jsonOutput bool
	var limit int
	var apply, dryRun bool
	var root string
	command := &cobra.Command{Use: "update", Short: "Check for newer upstream releases or apply local registry updates", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		if command.Flags().Changed("number") && limit <= 0 {
			return invalidCommand(usage)
		}
		if apply {
			if root == "" || command.Flags().Changed("number") {
				return invalidCommand(usage)
			}
			if r.Registry.Apply == nil {
				return errors.New("local registry update is unavailable")
			}
			report, applyErr := r.Registry.Apply.ApplyRegistryUpdates(command.Context(), app.RegistryApplyOptions{Root: root, DryRun: dryRun})
			var err error
			if jsonOutput {
				err = writeJSON(r.Stdout, report)
			} else {
				for _, result := range report.Results {
					if _, err = fmt.Fprintf(r.Stdout, "%s: %s", result.App, result.Status); err != nil {
						return err
					}
					for _, version := range result.Versions {
						if _, err = fmt.Fprintf(r.Stdout, " %s", version); err != nil {
							return err
						}
					}
					if result.Reason != "" {
						if _, err = fmt.Fprintf(r.Stdout, " (%s)", result.Reason); err != nil {
							return err
						}
					}
					if _, err = fmt.Fprintln(r.Stdout); err != nil {
						return err
					}
				}
			}
			if err != nil {
				return err
			}
			return applyErr
		}
		if dryRun || root != "" {
			return invalidCommand(usage)
		}
		service, err := r.requireService()
		if err != nil {
			return err
		}
		updater, ok := service.(app.RegistryUpdateService)
		if !ok {
			return errors.New("registry update is unavailable")
		}
		progress := r.progress()
		defer progress.finish()
		report, scanErr := updater.UpdateRegistry(command.Context(), app.RegistryUpdateOptions{Limit: limit}, progress.report)
		progress.finish()
		if jsonOutput {
			err = writeJSON(r.Stdout, report)
		} else {
			for _, result := range report.Results {
				if _, err = fmt.Fprintf(r.Stdout, "%s %s: %s", result.App, result.Platform, result.Status); err != nil {
					return err
				}
				if result.Reason != "" {
					if _, err = fmt.Fprintf(r.Stdout, " (%s)", result.Reason); err != nil {
						return err
					}
				}
				if _, err = fmt.Fprintln(r.Stdout); err != nil {
					return err
				}
				for _, candidate := range result.Candidates {
					if _, err = fmt.Fprintf(r.Stdout, "  %s %s %s\n", candidate.Channel, candidate.Version, candidate.UpstreamURL); err != nil {
						return err
					}
				}
			}
			if scanErr == nil {
				_, err = fmt.Fprintln(r.Stdout, "Scan complete. Candidates require registry review; manifests were not changed.")
			}
		}
		if err != nil {
			return err
		}
		return scanErr
	}}
	command.Flags().BoolVar(&apply, "apply", false, "add the newest missing releases to an explicit local checkout")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "verify and preview local updates without editing manifests")
	command.Flags().StringVar(&root, "registry", "", "local registry checkout for --apply")
	command.Flags().IntVarP(&limit, "number", "n", 0, "show at most this many newest missing releases per app/platform")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}
