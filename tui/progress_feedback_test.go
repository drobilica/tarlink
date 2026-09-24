package tui

import (
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

func TestLateOperationAndProgressMessagesAreIgnored(t *testing.T) {
	m := model{busy: "Updating", opCancel: func() {}, operationGeneration: 2, progress: app.Progress{Stage: app.ProgressDownloading}}
	updated, command := m.Update(operationMsg{generation: 1, message: "old result"})
	if command != nil || updated.(model).busy != "Updating" {
		t.Fatalf("late operation changed state: %#v", updated)
	}
	updated, command = m.Update(progressMsg{generation: 1, event: app.Progress{Stage: app.ProgressExtracting}})
	if command != nil || updated.(model).progress.Stage != app.ProgressDownloading {
		t.Fatalf("late progress changed state: %#v", updated)
	}
}

func TestLateRefreshCannotClearNewerOperation(t *testing.T) {
	m := model{
		busy: "Updating", opCancel: func() {}, operationGeneration: 2,
		requestGeneration: 2, dataLoaded: true,
	}
	updated, command := m.Update(loadedMsg{generation: 1, err: nil})
	got := updated.(model)
	if command != nil || got.busy != "Updating" || got.opCancel == nil || !got.dataLoaded {
		t.Fatalf("late refresh changed active operation: %#v", got)
	}
	got.busy = ""
	got.opCancel = nil
	got.loading = true
	updated, command = got.Update(loadedMsg{generation: 2, err: nil})
	if command != nil || updated.(model).loading {
		t.Fatal("current refresh did not complete")
	}
}

func TestCompletedOperationRejectsRepeatedMessages(t *testing.T) {
	m := model{operationGeneration: 2, opCancel: func() {}}
	updated, _ := m.Update(operationMsg{generation: 2, message: "done"})
	m = updated.(model)
	updated, command := m.Update(progressMsg{generation: 2, event: app.Progress{Stage: app.ProgressDownloading}})
	if command != nil || updated.(model).progress.Stage != "" {
		t.Fatal("late progress resumed completed operation")
	}
	updated, command = m.Update(operationMsg{generation: 2, message: "late"})
	if command != nil || updated.(model).status != "done" {
		t.Fatal("late result replaced completed operation")
	}
}

func TestBatchPartialFeedbackKeepsFailureMeaning(t *testing.T) {
	m := model{operationGeneration: 1, opCancel: func() {}}
	updated, _ := m.Update(operationMsg{generation: 1, message: batchMessage("Updated", app.BatchResult{
		Completed: []app.Result{{AppID: "one"}}, Failed: map[string]string{"two": "timeout"},
	}), partial: true})
	got := updated.(model)
	if !strings.Contains(got.status, "1 failed") || !strings.Contains(got.status, "two: timeout") || !got.feedbackPartial {
		t.Fatalf("partial feedback lost failure: %q", got.status)
	}
}

func TestProgressLineShowsOnlyEventIdentityAndKnownValues(t *testing.T) {
	m := model{width: 80, color: false, theme: newTheme(false), progressBar: newProgress(false), progress: app.Progress{Stage: app.ProgressExtracting, AppID: "blender", Item: 3, Total: 7}}
	line := m.progressLine()
	if !strings.Contains(line, "Extracting 3/7 · blender") || strings.Contains(line, "ETA") {
		t.Fatalf("progress invented or lost event data: %q", line)
	}
	if got := m.progressLine(); got != line {
		t.Fatalf("progress changed for unrelated input: %q", got)
	}
}
