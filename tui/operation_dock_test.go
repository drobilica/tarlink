package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/drobilica/tarlink/internal/app"
)

func dockFixture() model {
	m := panelFixture()
	m.detail.InstalledVersion = ""
	m.detail.UpdateAvailable = false
	m.installed = nil
	m.available = []app.Application{*m.detail}
	m.returnTo = screenAvailable
	return m
}

func frameRow(frame, value string) int {
	for row, line := range strings.Split(ansi.Strip(frame), "\n") {
		if strings.Contains(line, value) {
			return row
		}
	}
	return -1
}

func assertDockGeometry(t *testing.T, m model) string {
	t.Helper()
	frame := ansi.Strip(m.View().Content)
	rows := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	if len(rows) != m.height {
		t.Fatalf("%dx%d: %d rows:\n%s", m.width, m.height, len(rows), frame)
	}
	for _, line := range rows {
		if ansi.StringWidth(line) > m.width {
			t.Fatalf("%dx%d: overflow %q", m.width, m.height, line)
		}
	}
	if rows[m.height-2] != m.separator() {
		t.Fatalf("dock is not above footer:\n%s", frame)
	}
	return frame
}

func TestOperationDockKeepsDetailsAndActionsStable(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 18}, {30, 18}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := dockFixture()
			m.width, m.height = size[0], size[1]
			initial := m.View().Content
			next, _ := m.startInstall(m.detail.ID)
			m = next.(model)
			defer m.opCancel()
			before := assertDockGeometry(t, m)
			actionRow := frameRow(before, "ACTIONS")
			if actionRow < 0 || frameRow(before, "Fixture") < 0 || frameRow(before, "AVAILABLE") < 0 {
				t.Fatalf("details replaced:\n%s", before)
			}
			if size[1] >= 24 && actionRow != frameRow(initial, "ACTIONS") {
				t.Fatal("starting installation moved menu")
			}
			for _, stage := range []app.ProgressStage{app.ProgressDownloading, app.ProgressVerifying, app.ProgressExtracting, app.ProgressComplete} {
				next, _ = m.Update(progressMsg{generation: m.operationGeneration, event: app.Progress{Stage: stage, AppID: "fixture", BytesDone: 42, BytesTotal: 100}})
				m = next.(model)
				frame := assertDockGeometry(t, m)
				if m.screen != screenDetails || m.detail.ID != "fixture" || frameRow(frame, "ACTIONS") != actionRow {
					t.Fatal("progress replaced or moved details")
				}
				if !strings.Contains(frame, title(string(stage))) {
					t.Fatalf("missing phase %s:\n%s", stage, frame)
				}
				if !strings.Contains(frame, "42%") || size[1] >= 24 && !strings.Contains(frame, "42 B / 100 B") {
					t.Fatalf("missing known metrics:\n%s", frame)
				}
				if strings.Contains(frame, "ETA") {
					t.Fatal("invented ETA")
				}
			}
			for _, result := range []operationMsg{{message: "Installed fixture 2.0"}, {err: errors.New("verification failed")}, {err: context.Canceled}} {
				result.generation = m.operationGeneration
				next, _ = m.Update(result)
				got := next.(model)
				frame := assertDockGeometry(t, got)
				if got.screen != screenDetails || got.detail.ID != "fixture" || frameRow(frame, "ACTIONS") != actionRow {
					t.Fatalf("result discarded context:\n%s", frame)
				}
				want := result.message
				if result.err != nil {
					want = result.err.Error()
				}
				if errors.Is(result.err, context.Canceled) {
					want = "Operation cancelled"
				}
				if !strings.Contains(frame, want) {
					t.Fatalf("missing result %q:\n%s", want, frame)
				}
			}
			t.Logf("%dx%d running frame:\n%s", m.width, m.height, before)
		})
	}
}

type dockInstallService struct {
	fakeService
	failure error
}

func (f *dockInstallService) Install(ctx context.Context, id string, sink app.ProgressSink) (app.Result, error) {
	sink(app.Progress{Stage: app.ProgressDownloading, AppID: id, BytesDone: 50, BytesTotal: 100})
	sink(app.Progress{Stage: app.ProgressVerifying, AppID: id})
	if f.failure != nil {
		return app.Result{}, f.failure
	}
	value := f.available[0]
	value.InstalledVersion = value.RegistryVersion
	f.available, f.installed = []app.Application{value}, []app.Application{value}
	return app.Result{AppID: id, Version: value.InstalledVersion}, nil
}

func TestInstallDockThroughCommandAndRefresh(t *testing.T) {
	for _, failure := range []error{nil, errors.New("digest mismatch")} {
		m := dockFixture()
		service := &dockInstallService{fakeService: fakeService{available: []app.Application{*m.detail}}, failure: failure}
		m.service = service
		next, cmd := m.Update(key("enter"))
		m = next.(model)
		if cmd == nil || m.screen != screenDetails || !strings.Contains(m.View().Content, "ACTIONS") {
			t.Fatal("path check replaced workspace")
		}
		next, cmd = m.Update(cmd())
		m = next.(model)
		if m.screen != screenDetails || cmd == nil {
			t.Fatal("install did not begin in place")
		}
		sawDownload, sawVerify := false, false
		for cmd != nil {
			msg := cmd()
			if p, ok := msg.(progressMsg); ok {
				sawDownload = sawDownload || p.event.Stage == app.ProgressDownloading
				sawVerify = sawVerify || p.event.Stage == app.ProgressVerifying
			}
			next, cmd = m.Update(msg)
			m = next.(model)
		}
		if !sawDownload || !sawVerify || m.screen != screenDetails || m.busy != "" {
			t.Fatal("lost operation events or details")
		}
		if failure == nil {
			if m.detail.InstalledVersion != "2.0" || m.detailActions()[0].id != actionVersions || !strings.Contains(m.View().Content, "Installed fixture 2.0") {
				t.Fatalf("completion did not refresh details: %#v", m.detail)
			}
		} else if m.detail.InstalledVersion != "" || !strings.Contains(m.View().Content, "digest mismatch") {
			t.Fatal("failure changed installation state or hid error")
		}
	}
}

func TestDockErrorScrollAndResize(t *testing.T) {
	m := dockFixture()
	m.busy = "Installing fixture"
	m.startOperation()
	m.busy = ""
	m.err = errors.New(strings.Repeat("界 long error details\n", 30) + "FINAL FAILURE")
	m.detail.Name = strings.Repeat("界 very long name ", 20)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 18}, {30, 12}, {80, 24}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		before := assertDockGeometry(t, m)
		if !m.dockScrollable() {
			t.Fatal("long error not scrollable")
		}
		if !strings.Contains(m.helpView(), "PgUp/PgDn") {
			t.Fatal("result scroll shortcut missing")
		}
		offset := m.reviewScroll
		for i := 0; i < 100; i++ {
			next, _ = m.Update(key("pgdown"))
			m = next.(model)
		}
		after := assertDockGeometry(t, m)
		if !strings.Contains(after, "FINAL FAILURE") || m.reviewScroll != offset || frameRow(before, "ACTIONS") != frameRow(after, "ACTIONS") {
			t.Fatalf("scroll lost error or moved workspace:\n%s", after)
		}
		next, _ = m.Update(key("pgup"))
		if next.(model).dockScroll >= m.dockScroll {
			t.Fatal("page up did not scroll result")
		}
	}
}

func TestBusyDockKeyboardAndConfirmation(t *testing.T) {
	m := dockFixture()
	next, cmd := m.Update(pathCheckMsg{appID: "fixture", conflicts: []app.PathConflict{{Type: "PATH", Candidate: "fixture"}}})
	m = next.(model)
	if cmd != nil || m.screen != screenInstallConfirm || !strings.Contains(m.View().Content, "Install anyway?") {
		t.Fatal("confirmation bypassed")
	}
	next, cmd = m.Update(key("enter"))
	m = next.(model)
	if cmd == nil || m.screen != screenDetails || !strings.Contains(m.View().Content, "ACTIONS") {
		t.Fatal("confirmed operation replaced details")
	}
	m.opCancel()
	cancelled := false
	m.opCancel = func() { cancelled = true }
	for _, k := range []string{"enter", "x", "v", "r", "b", "i", "n", "u", "?", "down", "pgdown"} {
		next, cmd = m.Update(key(k))
		got := next.(model)
		if cmd != nil || got.screen != screenDetails || got.busy != m.busy || got.detailSelected != m.detailSelected || got.helpOverlay {
			t.Fatalf("conflicting key %s accepted", k)
		}
	}
	next, cmd = m.Update(key("esc"))
	if !cancelled || cmd != nil || next.(model).screen != screenDetails {
		t.Fatal("Esc did not cancel in place")
	}
	_, cmd = m.Update(key("q"))
	if cmd == nil {
		t.Fatal("Quit disabled")
	}
}

func TestBatchDockRetainsReviewAndPartialFailures(t *testing.T) {
	m := dockFixture()
	m.service = &fakeService{}
	m.selectedIDs = map[string]bool{"fixture": true, "two": true}
	m.batchIDs = []string{"fixture", "two"}
	m.batchTargets = []app.BatchTarget{{AppID: "fixture", Name: "Fixture", Version: "2.0"}, {AppID: "two", Name: "Two", Version: "1.0"}}
	next, cmd := m.Update(key("enter"))
	m = next.(model)
	if cmd == nil {
		t.Fatal("batch install did not start")
	}
	defer m.opCancel()
	next, _ = m.Update(progressMsg{generation: m.operationGeneration, event: app.Progress{Stage: app.ProgressDownloading, AppID: "two", Item: 2, Total: 2}})
	m = next.(model)
	frame := assertDockGeometry(t, m)
	if !strings.Contains(frame, "Review selection") || !strings.Contains(frame, "Downloading 2/2 · two") {
		t.Fatalf("batch context or identity lost:\n%s", frame)
	}
	result := cmd().(operationMsg)
	if result.move {
		t.Fatal("batch result navigates away")
	}
	result.message = batchMessage("Installed", app.BatchResult{Completed: []app.Result{{AppID: "fixture"}}, Failed: map[string]string{"two": "timeout"}})
	result.partial = true
	next, _ = m.Update(result)
	m = next.(model)
	frame = assertDockGeometry(t, m)
	if m.screen != screenDetails || len(m.selectedIDs) != 2 || !strings.Contains(frame, "two: timeout") || !strings.Contains(frame, "COMPLETED WITH FAILURES") {
		t.Fatalf("batch result discarded context:\n%s", frame)
	}
}

func TestDockMetricsUseExistingEstimator(t *testing.T) {
	m := dockFixture()
	m.busy = "Installing fixture"
	m.progress = app.Progress{Stage: app.ProgressDownloading, BytesDone: 7 << 20, BytesTotal: 17 << 20}
	now := time.Now()
	m.estimator.Add(now, 1<<20)
	m.progressSpeed = m.estimator.Add(now.Add(3*time.Second), 7<<20)
	frame := assertDockGeometry(t, m)
	if !strings.Contains(frame, "2.0 MiB/s") || !strings.Contains(frame, "ETA ~5s") {
		t.Fatalf("existing rate/ETA missing:\n%s", frame)
	}
	m.progress = app.Progress{Stage: app.ProgressVerifying}
	m.progressSpeed = 0
	frame = assertDockGeometry(t, m)
	if strings.Contains(frame, "%") || strings.Contains(frame, "ETA") || strings.Contains(frame, "MiB/s") {
		t.Fatal("unknown metrics invented")
	}
}

func TestUpdatePlanDockRetainsReviewThroughProgress(t *testing.T) {
	for _, failure := range []error{nil, context.Canceled, errors.New("stale plan")} {
		m := panelFixture()
		m.service = &updatePlanService{}
		m.updatePlan = &app.UpdatePlan{Applications: []app.UpdatePlanEntry{{ID: "fixture", Name: "Fixture", Status: "update", Target: app.UpdatePackage{Version: "2.0"}}}}
		next, _ := m.Update(key("enter"))
		m = next.(model)
		defer m.opCancel()
		next, _ = m.Update(progressMsg{generation: m.operationGeneration, event: app.Progress{Stage: app.ProgressVerifying, AppID: "fixture"}})
		m = next.(model)
		frame := assertDockGeometry(t, m)
		if m.updatePlan == nil || !strings.Contains(frame, "Review update plan") || !strings.Contains(frame, "Verifying") {
			t.Fatalf("lost reviewed plan:\n%s", frame)
		}
		next, _ = m.Update(operationMsg{generation: m.operationGeneration, message: "Update result", err: failure})
		got := next.(model)
		if got.updatePlan != nil || got.screen != screenDetails {
			t.Fatal("completed plan remained actionable or navigated away")
		}
	}
}

func TestFinishedBatchCannotRepeatLifecycleAction(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, uninstall := range []bool{false, true} {
			m := dockFixture()
			if uninstall {
				m.returnTo = screenInstalled
			}
			m.selectedIDs = map[string]bool{"fixture": true}
			m.batchIDs = []string{"fixture"}
			m.batchTargets = []app.BatchTarget{{AppID: "fixture", Name: "Fixture", Version: "2.0"}}
			m.busy = "Batch operation"
			m.startOperation()
			m.opCancel = func() {}
			next, _ := m.Update(operationMsg{generation: m.operationGeneration, message: "Batch result", partial: partial})
			m = next.(model)
			for _, k := range []string{"enter", "down", "enter", "up", "enter"} {
				next, cmd := m.Update(key(k))
				m = next.(model)
				if cmd != nil || m.screen != screenDetails || !m.batchFinished {
					t.Fatalf("completed batch accepts %q", k)
				}
			}
			if strings.Contains(m.helpView(), "Install selected") || strings.Contains(m.helpView(), "Uninstall selected") {
				t.Fatal("completed batch advertises a mutation")
			}
			next, _ = m.Update(key("esc"))
			m = next.(model)
			if len(m.selectedIDs) > 0 || len(m.batchTargets) > 0 || m.batchFinished {
				t.Fatal("leaving completed review retained stale batch")
			}
		}
	}
}

func TestDockHelpAndMinimumTerminalGeometry(t *testing.T) {
	for _, size := range [][2]int{{30, 10}, {30, 12}, {40, 18}, {29, 9}} {
		m := dockFixture()
		m.width, m.height = size[0], size[1]
		m.busy = "Installing fixture"
		m.startOperation()
		m.busy = ""
		m.err = errors.New(strings.Repeat("long failure ", 50))
		if size[0] < 30 || size[1] < 10 {
			if m.dockScrollable() {
				t.Fatal("hidden dock advertises unusable scroll action")
			}
		}
		m.helpOverlay = true
		rows := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
		if len(rows) != size[1] {
			t.Fatalf("%v: help overflowed dock: %d rows", size, len(rows))
		}
	}
}

func TestLifecycleShortcutsKeepActionLabelsAccurate(t *testing.T) {
	for _, shortcut := range []string{"r", "x"} {
		m := panelFixture()
		next, _ := m.Update(key(shortcut))
		m = next.(model)
		next, cmd := m.Update(key("enter"))
		m = next.(model)
		defer m.opCancel()
		if cmd == nil || m.busy == "" {
			t.Fatal("confirmed shortcut did not start operation")
		}
		// The selected Review-update row is not the operation invoked by a shortcut.
		// Keep installed action labels intact; the identity status and dock show work.
		workspace, _ := m.workspaceLinesFor(20)
		lines := strings.Join(workspace, "\n")
		if !strings.Contains(lines, "› Review update") || !strings.Contains(lines, "Uninstall") {
			t.Fatalf("shortcut relabeled the wrong action:\n%s", lines)
		}
	}
}
