// Package cli is the thin, presentation-only TarLink command interface.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/research"
)

// RegistryTools is the CLI's composition slot for registry-maintainer
// capabilities. Each capability is optional; a command whose capability is
// absent reports it as unavailable. Maintainer commands must not require the
// application runtime (Service).
type RegistryTools struct {
	Validation app.RegistryValidationService
	Research   app.ResearchService
	Onboarding app.RegistryOnboardingService
	Candidates app.CandidateService
	Blockers   app.BlockerService
	Icons      app.RegistryIconService
}

type Runner struct {
	Service   app.Service
	Registry  RegistryTools
	Stdout    io.Writer
	Stderr    io.Writer
	Stdin     io.Reader
	LaunchTUI func(context.Context, app.Service, io.Writer, io.Writer) error
}

func (r Runner) progress() *progressRenderer {
	return newProgressRenderer(r.Stderr, isTTY(r.Stderr))
}

type progressRenderer struct {
	writer io.Writer
	tty    bool
	last   app.ProgressStage
	count  int
	active bool
}

func newProgressRenderer(writer io.Writer, tty bool) *progressRenderer {
	return &progressRenderer{writer: writer, tty: tty}
}

func (p *progressRenderer) report(event app.Progress) {
	if event.Stage == p.last || event.Stage == "" || p.count >= 16 {
		return
	}
	p.last = event.Stage
	p.count++
	label := event.Description
	if label == "" {
		label = title(string(event.Stage))
	}
	if event.BytesTotal > 0 {
		label = fmt.Sprintf("%s %s / %s", label, bytesLabel(event.BytesDone), bytesLabel(event.BytesTotal))
	} else if event.BytesDone > 0 {
		label = fmt.Sprintf("%s %s", label, bytesLabel(event.BytesDone))
	}
	if p.tty {
		_, _ = fmt.Fprintf(p.writer, "\r%-80s", label)
		p.active = true
		return
	}
	_, _ = fmt.Fprintln(p.writer, label)
}

func (p *progressRenderer) finish() {
	if p.tty && p.active {
		_, _ = fmt.Fprintln(p.writer)
		p.active = false
	}
}

type progressOutput struct {
	io.Writer
	finish func()
}

func (w progressOutput) Write(value []byte) (int, error) {
	w.finish()
	return w.Writer.Write(value)
}

func isTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func bytesLabel(value int64) string {
	if value < 0 {
		value = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	amount := float64(value)
	i := 0
	for amount >= 1024 && i < len(units)-1 {
		amount /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", value, units[i])
	}
	return fmt.Sprintf("%.1f %s", amount, units[i])
}

func (r Runner) printApplications(values []app.Application, jsonOutput bool, emptyMessage string) error {
	values = append([]app.Application{}, values...)
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	if jsonOutput {
		return writeJSON(r.Stdout, values)
	}
	if len(values) == 0 {
		_, err := fmt.Fprintf(r.Stdout, "%s\n", emptyMessage)
		return err
	}
	for _, value := range values {
		status := "available"
		if value.InstalledVersion != "" {
			status = "installed " + value.InstalledVersion
			if value.UpdateAvailable {
				status += ", update available"
			}
		}
		if hasGameData(value) {
			status += " [GAME DATA]"
		}
		if _, err := fmt.Fprintf(r.Stdout, "%-20s %-12s %s\n", value.ID, value.RegistryVersion, status); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) printInfo(value app.Application, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, value)
	}
	update := "none"
	if value.UpdateAvailable {
		update = "available"
	}
	installed := value.InstalledVersion
	if installed == "" {
		installed = "not installed"
	}
	requirements := ""
	if hasGameData(value) {
		requirements = "Requires:     Original game data\n"
	}
	_, err := fmt.Fprintf(r.Stdout, "%s\n\nID:          %s\nVersion:     %s\nInstalled:   %s\nUpdate:      %s\nCategories:  %s\n%sHomepage:    %s\n",
		value.Name, value.ID, value.RegistryVersion, installed, update, strings.Join(value.Categories, ", "), requirements, value.Homepage)
	return err
}

func hasGameData(value app.Application) bool {
	for _, requirement := range value.Requirements {
		if requirement == "original-game-data" {
			return true
		}
	}
	return false
}

func (r Runner) printVersions(id string, values []app.Version, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, values)
	}
	if _, err := fmt.Fprintf(r.Stdout, "%s\n\n", id); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(r.Stdout, "%-20s %s\n", value.Version, value.Status); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) printFreshness(report freshness.Report, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(r.Stdout, report)
	}
	if len(report.Candidates) == 0 {
		_, err := io.WriteString(r.Stdout, "No upstream release candidates found.\n")
		return err
	}
	for _, candidate := range report.Candidates {
		if _, err := fmt.Fprintf(r.Stdout, "%s@%s %s (%s)\n", candidate.App, candidate.Channel, candidate.Version, candidate.UpstreamURL); err != nil {
			return err
		}
	}
	return nil
}

func splitCSV(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func (r Runner) promptRegistryAdd(options app.RegistryAddOptions, candidate app.RegistryCandidate, required []app.RegistryRequiredInput) (app.RegistryAddOptions, app.RegistryCandidate, error) {
	reader := bufio.NewReader(r.Stdin)
	needs := map[string]bool{}
	for _, value := range required {
		needs[value.Field] = true
	}
	if needs["executable"] {
		selected, err := r.promptCandidate(reader, "Executable", candidate.Executables, false)
		if err != nil {
			return options, candidate, err
		}
		candidate.Executable = selected
	}
	if needs["icon"] {
		selected, err := r.promptCandidate(reader, "Archive icon", candidate.Icons, true)
		if err != nil {
			return options, candidate, err
		}
		candidate.Icon = selected
	}
	if needs["platform"] || needs["archive"] || needs["artifact"] || needs["nested-archive"] {
		return options, candidate, errors.New("candidate has unresolved artifact facts; supply a less ambiguous official release asset")
	}
	if len(options.Categories) == 0 {
		value, err := r.promptLine(reader, "Category (comma-separated): ")
		if err != nil {
			return options, candidate, err
		}
		options.Categories = splitCSV(value)
		if len(options.Categories) == 0 {
			return options, candidate, errors.New("at least one category is required")
		}
	}
	if options.CreateBinLink == nil && (containsCategory(options.Categories, "games") || containsCategory(options.Categories, "recompilation")) {
		value, err := r.promptLine(reader, "Create CLI bin link? [y/N]: ")
		if err != nil {
			return options, candidate, err
		}
		answer := strings.ToLower(strings.TrimSpace(value))
		selected := answer == "y" || answer == "yes"
		options.CreateBinLink = &selected
	}
	return options, candidate, nil
}

func (r Runner) promptLine(reader *bufio.Reader, label string) (string, error) {
	if _, err := io.WriteString(r.Stdout, label); err != nil {
		return "", err
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
func (r Runner) promptCandidate(reader *bufio.Reader, label string, candidates []string, optional bool) (string, error) {
	for index, value := range candidates {
		if _, err := fmt.Fprintf(r.Stdout, "%s %d: %s\n", label, index+1, value); err != nil {
			return "", err
		}
	}
	suffix := ""
	if optional {
		suffix = " (0 for none)"
	}
	value, err := r.promptLine(reader, label+" selection"+suffix+": ")
	if err != nil {
		return "", err
	}
	if optional && (value == "" || value == "0") {
		return "", nil
	}
	var index int
	if _, err := fmt.Sscan(value, &index); err != nil || index < 1 || index > len(candidates) {
		return "", errors.New("invalid candidate selection")
	}
	return candidates[index-1], nil
}
func containsCategory(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (r Runner) printCandidate(value app.RegistryCandidate) error {
	_, err := fmt.Fprintf(r.Stdout, "GitHub repository       ✓ %s\nRelease                 ✓ %s\nAsset                   ✓ %s\nPlatform                %s\nArchive                 %s\nSHA-256                 %s\nExecutable              %s\nIcon                    %s\n\n", value.Repository, value.Release, value.Asset, checkValue(value.Platform), checkValue(value.Archive), checkValue(value.SHA256), checkValue(value.Executable), checkValue(value.Icon))
	return err
}
func checkValue(value string) string {
	if value == "" {
		return "?"
	}
	return "✓ " + value
}
func requiredFields(values []app.RegistryRequiredInput) string {
	fields := make([]string, 0, len(values))
	for _, value := range values {
		fields = append(fields, value.Field)
	}
	return strings.Join(fields, ", ")
}

func (r Runner) printRegistryInspection(value app.RegistryInspectionResult) error {
	if value.Manifest != nil {
		mark := "✓"
		if !value.Manifest.Valid {
			mark = "✗"
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s\n\n", value.Manifest.ID); err != nil {
			return err
		}
		if !value.Manifest.Valid {
			_, err := fmt.Fprintf(r.Stdout, "%s %s\n", mark, value.Manifest.Error)
			return err
		}
		for _, check := range value.Manifest.Checks {
			if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", mark, check); err != nil {
				return err
			}
		}
		return nil
	}
	if value.Directory != nil {
		if _, err := fmt.Fprintf(r.Stdout, "Registry inspection\n\nManifests: %d\nValid:     %d\nWarnings:  %d\nInvalid:   %d\n\n", value.Directory.Manifests, value.Directory.Valid, value.Directory.Warnings, value.Directory.Invalid); err != nil {
			return err
		}
		for _, result := range value.Directory.Results {
			mark := "✓"
			if !result.Valid {
				mark = "✗"
			}
			if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", mark, result.Path); err != nil {
				return err
			}
		}
		return nil
	}
	if value.Candidate != nil {
		if err := r.printCandidate(*value.Candidate); err != nil {
			return err
		}
		if len(value.Required) != 0 {
			_, err := fmt.Fprintf(r.Stdout, "Needs input: %s\n", requiredFields(value.Required))
			return err
		}
		return nil
	}
	return nil
}

func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func printChanges(w io.Writer, v research.CandidateChanges) error {
	_, e := fmt.Fprintf(w, "RECHECK %d\nUNCHANGED %d\nERROR %d\n", v.Summary["RECHECK"], v.Summary["UNCHANGED"], v.Summary["ERROR"])
	if e != nil {
		return e
	}
	for _, x := range v.Results {
		if x.Decision == "RECHECK" || x.Decision == "ERROR" {
			old := fmt.Sprintf("%s (%d)", x.Old.ReleaseTag, x.Old.ReleaseID)
			current := ""
			if x.Current != nil {
				current = fmt.Sprintf(" -> %s (%d)", x.Current.ReleaseTag, x.Current.ReleaseID)
			}
			if _, e = fmt.Fprintf(w, "%-10s %s %s%s %s\n", x.Decision, x.ID, old, current, x.Reason); e != nil {
				return e
			}
		}
	}
	return nil
}
func printCapability(w io.Writer, v []research.CapabilityResult) error {
	for _, x := range v {
		if _, e := fmt.Fprintf(w, "%s\n  removed: %s\n  remaining: %s\n  fully unlocked: %t\n", x.ID, strings.Join(x.Removed, ", "), strings.Join(x.Remaining, ", "), x.FullyUnlocked); e != nil {
			return e
		}
	}
	return nil
}

func (r Runner) printResearch(value app.ResearchResult, jsonOutput bool) error {
	if jsonOutput {
		// ResearchResult is the sole JSON model for both commands. In
		// particular, status is derived by the application facade on every
		// invocation and is never treated as cached approval.
		return writeJSON(r.Stdout, value)
	}
	_, err := fmt.Fprintf(r.Stdout, "Repository: %s\nRelease tag: %s\nRelease ID: %d\nAsset: %s\nAsset ID: %d\nAsset size: %d\nGitHub digest: %s\nAlgorithm: %s\nVerdict: %s\nReason: %s\n", value.Repository, value.Release.Tag, value.Release.ID, value.Asset.Name, value.Asset.ID, value.Asset.Size, value.Asset.Digest, value.Provenance.Algorithm, value.Provenance.Verdict, value.Provenance.Message)
	if err != nil || value.Inspection == nil {
		return err
	}
	if len(value.Inspection.ComputedDigests) != 0 {
		if _, err = fmt.Fprintf(r.Stdout, "Computed digests: sha256=%s sha512=%s\n", value.Inspection.ComputedDigests["sha256"], value.Inspection.ComputedDigests["sha512"]); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(r.Stdout, "Status: %s\nArtifact type: %s\nExecutables: %s\nNested archives: %s\nBlockers: %s\n", value.Status, value.Inspection.ArtifactType, strings.Join(value.Inspection.Executables, ", "), strings.Join(value.Inspection.Nested, ", "), strings.Join(value.Inspection.Blockers, ", ")); err != nil {
		return err
	}
	if value.Inspection.Dependencies != nil && len(value.Inspection.Dependencies.Files) > 0 {
		if _, err = fmt.Fprintln(r.Stdout, value.Inspection.Dependencies.String()); err != nil {
			return err
		}
	}
	return err
}

func (r Runner) printUpdateAll(result app.UpdateAllResult) error {
	for _, value := range result.Updated {
		if _, err := fmt.Fprintf(r.Stdout, "Updated %s to %s\n", value.AppID, value.Version); err != nil {
			return err
		}
		for _, warning := range value.Warnings {
			if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
				return err
			}
		}
	}
	for _, id := range result.Skipped {
		label := "No update for"
		for _, pinned := range result.Pinned {
			if pinned == id {
				label = "Skipped pinned"
				break
			}
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", label, id); err != nil {
			return err
		}
	}
	failedIDs := make([]string, 0, len(result.Failed))
	for id := range result.Failed {
		failedIDs = append(failedIDs, id)
	}
	sort.Strings(failedIDs)
	for _, id := range failedIDs {
		message := result.Failed[id]
		if _, err := fmt.Fprintf(r.Stderr, "Failed %s: %s\n", id, message); err != nil {
			return err
		}
	}
	if len(result.Failed) != 0 {
		return &app.Error{Code: result.FailureCodes[failedIDs[0]], Op: "update all", Err: errors.New("one or more applications failed to update")}
	}
	return nil
}

func (r Runner) printBatch(action string, result app.BatchResult) error {
	for _, outcome := range result.Outcomes {
		switch outcome.Status {
		case "completed":
			if outcome.Result != nil {
				if action == "Uninstalled" && outcome.Result.Version != "" {
					if _, err := fmt.Fprintf(r.Stdout, "Uninstalled %s %s\n", outcome.Result.AppID, outcome.Result.Version); err != nil {
						return err
					}
					for _, warning := range outcome.Result.Warnings {
						if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
							return err
						}
					}
				} else if err := r.printResult(action, *outcome.Result); err != nil {
					return err
				}
			}
		case "skipped":
			if _, err := fmt.Fprintf(r.Stdout, "Skipped %s (%s)\n", outcome.AppID, outcome.Reason); err != nil {
				return err
			}
		case "failed":
			if _, err := fmt.Fprintf(r.Stderr, "Failed %s: %s\n", outcome.AppID, outcome.Reason); err != nil {
				return err
			}
		}
	}
	if len(result.Failed) == 0 {
		return nil
	}
	for _, outcome := range result.Outcomes {
		if outcome.Status == "failed" {
			return &app.Error{Code: outcome.Code, Op: "batch operation", Err: errors.New("one or more applications failed")}
		}
	}
	return &app.Error{Code: app.CodeConflict, Op: "batch operation", Err: errors.New("one or more applications failed")}
}

func (r Runner) printUninstallAll(result app.UninstallAllResult) error {
	batch := app.BatchResult{Completed: result.Completed, Failed: result.Failed, FailureCodes: result.FailureCodes, Outcomes: result.Outcomes}
	batchErr := r.printBatch("Uninstalled", batch)
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	if len(result.Outcomes) == 0 {
		_, err := io.WriteString(r.Stdout, "Uninstalled all applications\n")
		return err
	}
	return batchErr
}

func (r Runner) printDoctor(report app.DoctorReport) error {
	if _, err := io.WriteString(r.Stdout, "TarLink doctor\n\nGlobal\n"); err != nil {
		return err
	}
	print := func(check app.DoctorCheck, indent string) error {
		mark := "✓"
		if check.Status == "warning" {
			mark = "⚠"
		}
		if check.Status == "error" {
			mark = "✗"
		}
		_, err := fmt.Fprintf(r.Stdout, "%s%s %s", indent, mark, check.Label)
		if err != nil {
			return err
		}
		if check.Detail != "" {
			_, err = fmt.Fprintf(r.Stdout, ": %s", check.Detail)
		}
		if err == nil {
			_, err = io.WriteString(r.Stdout, "\n")
		}
		return err
	}
	for _, check := range report.Global {
		if err := print(check, "  "); err != nil {
			return err
		}
	}
	for _, application := range report.Applications {
		if _, err := fmt.Fprintf(r.Stdout, "\n%s\n", application.ID); err != nil {
			return err
		}
		for _, check := range application.Checks {
			if err := print(check, "  "); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(r.Stdout, "\nSummary: %d error(s), %d warning(s)\n", report.Errors, report.Warnings)
	return err
}

func (r Runner) printResult(action string, result app.Result) error {
	if result.Version != "" {
		preposition := " "
		if action == "Updated" || action == "Rolled back" {
			preposition = " to "
		}
		if _, err := fmt.Fprintf(r.Stdout, "%s %s%s%s\n", action, result.AppID, preposition, result.Version); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(r.Stdout, "%s %s\n", action, result.AppID); err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(r.Stderr, "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func (r Runner) invalid(message string) int {
	_, _ = fmt.Fprintln(r.Stderr, message)
	return exitInvalidArguments
}

func (r Runner) fail(err error) int {
	_, _ = fmt.Fprintf(r.Stderr, "tarlink: %s\n", err)
	return exitCode(err)
}

// Fail presents a startup error with the same stable exit mapping as command
// failures. It exists so the executable can enforce platform and root policy
// before constructing the application service.
func (r Runner) Fail(err error) int {
	if r.Stderr == nil {
		r.Stderr = io.Discard
	}
	return r.fail(err)
}

func writeJSON(destination io.Writer, value any) error {
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func (r Runner) printPathConflicts(appID string, conflicts []app.PathConflict) error {
	for _, conflict := range conflicts {
		switch conflict.Type {
		case "not_in_path":
			_, err := fmt.Fprintf(r.Stderr, "Warning: %s is not in your PATH, so %s would not be runnable as a command.\n", conflict.Directory, appID)
			if err != nil {
				return err
			}
		case "shadowed":
			_, err := fmt.Fprintf(r.Stderr, "Warning: %s shadows %s; running %q would use %s instead of the TarLink-installed command.\n", conflict.Directory, appID, appID, conflict.Candidate)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
