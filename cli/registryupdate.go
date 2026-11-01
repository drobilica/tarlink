package cli

import (
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/spf13/cobra"
)

func (r Runner) registryUpdateCommand() *cobra.Command {
	usage := "usage: tarlink registry update [--json]"
	var jsonOutput bool
	command := &cobra.Command{Use: "update", Short: "Check all registry applications for upstream releases", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
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
		report, scanErr := updater.UpdateRegistry(command.Context(), progress.report)
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
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}
