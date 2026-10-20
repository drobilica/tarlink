package cli

import (
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/spf13/cobra"
)

func (r Runner) verifyCommand() *cobra.Command {
	const usage = "usage: tarlink verify <app> | tarlink verify --all"
	var all bool
	command := &cobra.Command{Use: "verify [app]", Short: "Compare installed payloads with local integrity baselines", Args: func(_ *cobra.Command, args []string) error {
		if all && len(args) == 0 || !all && len(args) == 1 {
			return nil
		}
		return invalidCommand(usage)
	}, RunE: func(command *cobra.Command, args []string) error {
		service, err := r.requireService()
		if err != nil {
			return err
		}
		verifier, ok := service.(app.VerifyService)
		if !ok {
			return errors.New("payload verification is unavailable")
		}
		id := ""
		if len(args) > 0 {
			id = args[0]
		}
		report, err := verifier.Verify(command.Context(), id)
		if err != nil {
			return err
		}
		if len(report.Payloads) == 0 {
			_, err = fmt.Fprintln(r.Stdout, "No installed applications.")
			return err
		}
		for _, payload := range report.Payloads {
			if _, err := fmt.Fprintf(r.Stdout, "%s %s %s: %s — %s\n", payload.App, payload.Retention, payload.Version, payload.Status, payload.Detail); err != nil {
				return err
			}
			for _, change := range payload.Changes {
				if _, err := fmt.Fprintf(r.Stdout, "  %s %q\n", change.Status, change.Path); err != nil {
					return err
				}
			}
		}
		if report.Issues > 0 {
			return &app.Error{Code: app.CodeStateCorrupt, Op: "verify", Err: errors.New("payloads changed or verification unavailable; changed bytes may reflect self-updates; repair explicitly restores the recorded version")}
		}
		return nil
	}}
	command.Flags().BoolVar(&all, "all", false, "verify all installed applications")
	configureCommand(command, usage)
	return command
}
