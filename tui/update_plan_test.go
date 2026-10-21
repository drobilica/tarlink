package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/drobilica/tarlink/internal/app"
)

type updatePlanService struct {
	fakeService
	plan      app.UpdatePlan
	requested []string
	applied   bool
}

func (s *updatePlanService) PlanUpdates(_ context.Context, ids []string) (app.UpdatePlan, error) {
	s.requested = ids
	return s.plan, nil
}
func (s *updatePlanService) ApplyUpdatePlan(context.Context, app.UpdatePlan, app.ProgressSink) (app.UpdateAllResult, error) {
	s.applied = true
	return app.UpdateAllResult{Updated: []app.Result{{AppID: "fixture", Version: "2.0"}}}, nil
}

func TestUpdateRequiresPlanConfirmationAndSupportsCancel(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		service := &updatePlanService{plan: app.UpdatePlan{Schema: 1, Applications: []app.UpdatePlanEntry{{ID: "fixture", Name: "Fixture", Status: "update", Current: app.UpdatePackage{Version: "1.0", Channel: "stable", Fingerprint: "old"}, Target: app.UpdatePackage{Version: "2.0", Channel: "stable", Fingerprint: "new"}, Rollback: app.UpdatePackage{Version: "1.0", Channel: "stable", Fingerprint: "old"}, PackageChanged: true}}}}
		detail := app.Application{ID: "fixture", Name: "Fixture", InstalledVersion: "1.0", RegistryVersion: "2.0", UpdateAvailable: true}
		m := model{ctx: context.Background(), service: service, screen: screenDetails, returnTo: screenUpdates, detail: &detail, width: 80, height: 24}
		next, cmd := m.Update(key("enter"))
		if cmd == nil || service.applied {
			t.Fatal("update did not plan first")
		}
		next, _ = next.(model).Update(cmd())
		review := next.(model)
		if review.updatePlan == nil || service.applied || len(service.requested) != 1 {
			t.Fatalf("no pending review: %+v", review.updatePlan)
		}
		for _, want := range []string{"Review update plan", "1.0 [stable] → 2.0 [stable]", "Rollback retained:"} {
			if !strings.Contains(strings.Join(review.reviewLines(), "\n"), want) {
				t.Fatalf("missing %q", want)
			}
		}
		if cancel {
			next, cmd = review.Update(key("esc"))
			if cmd != nil || service.applied || next.(model).updatePlan != nil {
				t.Fatal("cancel applied plan or retained review")
			}
		} else {
			next, cmd = review.Update(key("enter"))
			if cmd == nil {
				t.Fatal("confirm did not apply")
			}
			running := next.(model)
			if !strings.Contains(running.View().Content, "Review update plan") || !strings.Contains(running.View().Content, "Applying update plan") {
				t.Fatal("applying the plan discarded its review workspace")
			}
			result := cmd()
			// The operation hub may deliver progress first for a real service; this
			// fixture returns directly without emitting progress.
			if message, ok := result.(operationMsg); !ok || message.err != nil || !service.applied {
				t.Fatalf("apply=%+v applied=%v", result, service.applied)
			}
			next, _ = running.Update(result)
			if next.(model).updatePlan != nil {
				t.Fatal("completed plan remains actionable")
			}
		}
	}
}

func TestUpdatePlanReviewGeometryAcrossResize(t *testing.T) {
	long := strings.Repeat("界-long-package-", 15)
	plan := app.UpdatePlan{Applications: []app.UpdatePlanEntry{{ID: "fixture", Name: long, Status: "update", Current: app.UpdatePackage{Version: long, Channel: "stable", Fingerprint: long}, Target: app.UpdatePackage{Version: long, Channel: "stable", Fingerprint: long}, Rollback: app.UpdatePackage{Version: long, Channel: "stable", Fingerprint: long}, PackageChanged: true}}}
	m := model{ctx: context.Background(), screen: screenDetails, returnTo: screenUpdates, updatePlan: &plan, dataLoaded: true, theme: newTheme(false)}
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 18}, {30, 12}, {100, 30}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		for _, line := range m.reviewLines() {
			if ansi.StringWidth(line) > max(1, viewWidth(m.width)-2) {
				t.Fatalf("review content exceeds viewport: %q", line)
			}
		}
		if !strings.Contains(strings.Join(m.reviewLines(), ""), long) {
			t.Fatal("review discarded long package identity")
		}
		lines := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
		if len(lines) > size[1] {
			t.Fatalf("%dx%d: %d rows", size[0], size[1], len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("%dx%d: overflow %q", size[0], size[1], line)
			}
		}
	}
}
