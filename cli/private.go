package cli

import (
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/spf13/cobra"
)

func (r Runner) privateCommand(progress *progressRenderer) *cobra.Command {
	command := &cobra.Command{Use: "private", Short: "Explicit private Recit enrollment and lifecycle"}
	configureCommand(command, "tarlink private enroll --repository-id 1442652140\ntarlink private install recit[@version]\ntarlink private update recit[@version]\ntarlink private repair recit\nCredentials: TARLINK_PRIVATE_GITHUB_TOKEN (read-only Recit repository access)")
	var repositoryID int64
	enroll := &cobra.Command{Use: "enroll --repository-id ID", Short: "Enroll the approved private Recit GitHub authority", Args: noArgs("tarlink private enroll --repository-id 1442652140"), RunE: func(cmd *cobra.Command, _ []string) error {
		service, ok := r.Service.(app.PrivateService)
		if !ok {
			return errors.New("private lifecycle unavailable")
		}
		if err := service.EnrollPrivate(cmd.Context(), repositoryID); err != nil {
			return err
		}
		_, err := fmt.Fprintln(r.Stdout, "Enrolled private drobilica/recit authority for recit")
		return err
	}}
	configureCommand(enroll, "tarlink private enroll --repository-id 1442652140")
	enroll.Flags().Int64Var(&repositoryID, "repository-id", 0, "explicit immutable GitHub repository ID")
	command.AddCommand(enroll)
	for _, operation := range []string{"install", "update", "repair"} {
		operation := operation
		child := &cobra.Command{Use: operation + " recit[@version]", Short: operation + " Recit from its enrolled private authority", Args: exactArgs(1, "tarlink private "+operation+" recit[@version]"), RunE: func(cmd *cobra.Command, args []string) error {
			service, ok := r.Service.(app.PrivateService)
			if !ok {
				return errors.New("private lifecycle unavailable")
			}
			result, err := service.PrivateOperation(cmd.Context(), operation, args[0], progress.report)
			if err != nil {
				return err
			}
			return r.printResult("Private "+operation, result)
		}}
		configureCommand(child, "tarlink private "+operation+" recit[@version]")
		command.AddCommand(child)
	}
	return command
}
