package cli

import (
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/spf13/cobra"
)

func (r Runner) registryUpdateCommand() *cobra.Command {
	usage := "usage: tarlink registry update [--app <id>] [-n <count>] [--json] | tarlink registry update --apply --registry <path> [--app <id>] [--dry-run] [--json]"
	var jsonOutput bool
	var limit int
	var apply, dryRun bool
	var root, appID string
	command := &cobra.Command{Use: "update", Short: "Check for newer upstream releases or apply local registry updates", Args: noArgs(usage), RunE: func(command *cobra.Command, _ []string) error {
		if (command.Flags().Changed("number") && limit <= 0) || (command.Flags().Changed("app") && appID == "") {
			return invalidCommand(usage)
		}
		if apply {
			if root == "" || command.Flags().Changed("number") {
				return invalidCommand(usage)
			}
			if r.Registry.Apply == nil {
				return errors.New("local registry update is unavailable")
			}
			report, applyErr := r.Registry.Apply.ApplyRegistryUpdates(command.Context(), app.RegistryApplyOptions{Root: root, DryRun: dryRun, App: appID})
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
					if result.Detail != "" {
						if _, err = fmt.Fprintf(r.Stdout, ": %s", result.Detail); err != nil {
							return err
						}
					}
					if _, err = fmt.Fprintln(r.Stdout); err != nil {
						return err
					}
				}
				s := report.Summary
				_, err = fmt.Fprintf(r.Stdout, "Results: %d total, %d updated, %d would-update, %d unchanged, %d skipped, %d errors.\n", s.Total, s.Updated, s.WouldUpdate, s.Unchanged, s.Skipped, s.Errors)
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
		report, scanErr := updater.UpdateRegistry(command.Context(), app.RegistryUpdateOptions{Limit: limit, App: appID}, progress.report)
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
				if result.Detail != "" {
					if _, err = fmt.Fprintf(r.Stdout, ": %s", result.Detail); err != nil {
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
					if a := candidate.Assets; a != nil {
						if _, err = fmt.Fprintf(r.Stdout, "    Assets: %s (%s, %s; filenames only, bytes unverified)\n", a.Status, a.Platform, a.Format); err != nil {
							return err
						}
						for _, name := range a.Matches {
							if _, err = fmt.Fprintf(r.Stdout, "      %q: matches\n", name); err != nil {
								return err
							}
						}
						for _, rejected := range a.Rejected {
							if _, err = fmt.Fprintf(r.Stdout, "      %q: %s\n", rejected.Name, rejected.Reason); err != nil {
								return err
							}
						}
						if len(a.Matches)+len(a.Rejected) == 0 {
							if _, err = fmt.Fprintln(r.Stdout, "      Release has no assets."); err != nil {
								return err
							}
						}
					}
				}
			}
			summary := report.Summary
			if _, err = fmt.Fprintf(r.Stdout, "Results: %d total, %d with candidates, %d without candidates, %d skipped, %d errors.\n", summary.Total, summary.Candidates, summary.NoCandidates, summary.Skipped, summary.Errors); err != nil {
				return err
			}
			if _, err = fmt.Fprintf(r.Stdout, "Candidate assets: %d with one filename match, %d missing or ambiguous; downloads remain unverified.\n", summary.MatchedAssets, summary.BlockedAssets); err != nil {
				return err
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
	command.Flags().StringVar(&appID, "app", "", "check or apply only this registry application")
	command.Flags().BoolVar(&apply, "apply", false, "add the newest missing releases to an explicit local checkout")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "verify and preview local updates without editing manifests")
	command.Flags().StringVar(&root, "registry", "", "local registry checkout for --apply")
	command.Flags().IntVarP(&limit, "number", "n", 0, "show at most this many newest missing releases per app/platform")
	command.Flags().BoolVar(&jsonOutput, "json", false, "write JSON")
	configureCommand(command, usage)
	return command
}
