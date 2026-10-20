package cli

import (
	"errors"
	"fmt"

	"github.com/drobilica/tarlink/internal/app"
)

func (r Runner) printUpdatePlan(plan app.UpdatePlan, jsonOutput bool) error {
	if jsonOutput {
		if err := writeJSON(r.Stdout, plan); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(r.Stdout, "Update plan (%s, registry %s)\n", plan.Platform, plan.RegistryRevision); err != nil {
			return err
		}
		if len(plan.Applications) == 0 {
			if _, err := fmt.Fprintln(r.Stdout, "No installed applications."); err != nil {
				return err
			}
		}
		for _, entry := range plan.Applications {
			if _, err := fmt.Fprintf(r.Stdout, "%s: %s", entry.ID, entry.Status); err != nil {
				return err
			}
			if entry.Target.Version != "" {
				if _, err := fmt.Fprintf(r.Stdout, " %s [%s] -> %s [%s]", entry.Current.Version, entry.Current.Channel, entry.Target.Version, entry.Target.Channel); err != nil {
					return err
				}
			}
			if entry.Reason != "" {
				if _, err := fmt.Fprintf(r.Stdout, " (%s)", entry.Reason); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(r.Stdout); err != nil {
				return err
			}
			if entry.Status != "update" {
				continue
			}
			if _, err := fmt.Fprintf(r.Stdout, "  Package: %s -> %s\n", entry.Current.Fingerprint, entry.Target.Fingerprint); err != nil {
				return err
			}
			if entry.RuntimeChanged {
				if _, err := fmt.Fprintf(r.Stdout, "  Runtime: %s -> %s\n", plannedRuntime(entry.Current.Runtime), plannedRuntime(entry.Target.Runtime)); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(r.Stdout, "  Rollback retained: %s [%s] %s\n", entry.Rollback.Version, entry.Rollback.Channel, entry.Rollback.Fingerprint); err != nil {
				return err
			}
		}
	}
	for _, entry := range plan.Applications {
		if entry.Status == "error" {
			return &app.Error{Code: entry.Code, Op: "plan updates", Err: errors.New("one or more applications could not be resolved")}
		}
	}
	return nil
}

func plannedRuntime(value *app.UpdateRuntime) string {
	if value == nil {
		return "self-contained"
	}
	return value.ID + "@" + value.Version + " (" + value.Fingerprint + ")"
}
