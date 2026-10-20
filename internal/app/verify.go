package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integrity"
	"github.com/drobilica/tarlink/internal/state"
)

// VerifyService inspects installation baselines without networking or writes.
type VerifyService interface {
	Verify(context.Context, string) (VerifyReport, error)
}
type VerifyPayload struct {
	App       string
	Retention string
	Version   string
	Status    string // ok, changed, unavailable
	Detail    string
	Changes   []integrity.Change
}
type VerifyReport struct {
	Payloads []VerifyPayload
	Issues   int
}

// Verify compares current and retained previous payloads. An empty appID
// selects all state records. It takes no locks and creates no filesystem objects;
// concurrent application writes or lifecycle changes can require a rerun.
func (core *Core) Verify(ctx context.Context, appID string) (VerifyReport, error) {
	var report VerifyReport
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if appID != "" {
		if err := filesystem.ValidateID(appID); err != nil {
			return report, &Error{Code: CodeInvalidArguments, Op: "verify", Err: err}
		}
	}
	ids := []string{appID}
	if err := filesystem.CheckOwnedDirectoryWithin(core.layout.Home, core.layout.States); err != nil {
		if appID == "" && errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
		return report, classify("verify states", err)
	}
	if appID == "" {
		entries, err := os.ReadDir(core.layout.States)
		if err != nil {
			return report, classify("verify states", err)
		}
		ids = nil
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
			}
		}
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := filesystem.ValidateID(id); err != nil {
			report.Payloads = append(report.Payloads, VerifyPayload{App: fmt.Sprintf("%q", id), Status: "unavailable", Detail: err.Error()})
			report.Issues++
			continue
		}
		installed, err := state.LoadForApp(core.layout, id)
		if err == nil {
			err = installed.ValidateForLayout(core.layout)
		}
		if err != nil {
			report.Payloads = append(report.Payloads, VerifyPayload{App: id, Status: "unavailable", Detail: err.Error()})
			report.Issues++
			continue
		}
		for _, ref := range []struct{ retention, version, fingerprint, artifact string }{
			{"current", installed.Current, installed.CurrentFingerprint, installed.Artifact},
			{"previous", installed.Previous, installed.PreviousFingerprint, installed.PreviousArtifact},
		} {
			if ref.version == "" {
				continue
			}
			payload := VerifyPayload{App: id, Retention: ref.retention, Version: ref.version, Status: "ok", Detail: "matches installation baseline"}
			directory, pathErr := core.layout.PackagePath(id, ref.version, ref.fingerprint)
			if pathErr == nil {
				pathErr = filesystem.CheckOwnedDirectoryWithin(core.layout.Home, directory)
			}
			if pathErr == nil {
				payload.Changes, pathErr = integrity.Verify(ctx, directory, integrity.Identity{App: id, Version: ref.version, Fingerprint: ref.fingerprint, AppImage: ref.artifact == "appimage"})
			}
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if pathErr != nil {
				payload.Status = "unavailable"
				payload.Detail = pathErr.Error()
			} else if len(payload.Changes) > 0 {
				payload.Status = "changed"
				payload.Detail = fmt.Sprintf("%d differences from installation baseline", len(payload.Changes))
			}
			if payload.Status != "ok" {
				report.Issues++
			}
			report.Payloads = append(report.Payloads, payload)
		}
	}
	return report, nil
}
