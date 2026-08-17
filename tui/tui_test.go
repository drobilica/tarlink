package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/drobilica/tarlink/internal/app"
)

type fakeService struct {
	applications []app.Application
	rolledBack   string
}

func (f *fakeService) Install(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{AppID: "blender", Version: "5.2.0"}, nil
}
func (f *fakeService) Update(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{AppID: "blender", Version: "5.2.0"}, nil
}
func (f *fakeService) UpdateAll(context.Context, app.ProgressSink) (app.UpdateAllResult, error) {
	return app.UpdateAllResult{}, errors.New("unused")
}
func (f *fakeService) Remove(context.Context, string, app.ProgressSink) error {
	return errors.New("unused")
}
func (f *fakeService) Rollback(_ context.Context, id string, _ app.ProgressSink) (app.Result, error) {
	f.rolledBack = id
	return app.Result{AppID: id, Version: "5.1.0"}, nil
}
func (f *fakeService) List(context.Context) ([]app.Application, error) {
	return f.applications, nil
}
func (f *fakeService) Info(context.Context, string) (app.Application, error) {
	return app.Application{}, errors.New("unused")
}
func (f *fakeService) Search(context.Context, string) ([]app.Application, error) {
	return f.applications, nil
}
func (f *fakeService) Versions(context.Context, string) ([]app.Version, error) {
	return []app.Version{{Version: "5.2.0", Status: "current"}}, nil
}
func (f *fakeService) SyncRegistry(context.Context, app.ProgressSink) error {
	return errors.New("unused")
}

func TestModelLoadsAndShowsApplications(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", RegistryVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service}
	message := m.Init()()
	updated, _ := m.Update(message)
	view := updated.(model).View()
	if !strings.Contains(view.Content, "Blender") || !strings.Contains(view.Content, "AVAILABLE / SEARCH") {
		t.Fatalf("unexpected view: %q", view.Content)
	}
}

func TestRollbackDelegatesToService(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}
	updated, command := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if command != nil || updated.(model).screen != screenRollback {
		t.Fatal("rollback confirmation did not open")
	}
	updated, command = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || updated.(model).busy == "" {
		t.Fatal("confirmed rollback did not start")
	}
	result := command()
	if result.(operationMsg).err != nil || service.rolledBack != "blender" {
		t.Fatalf("rollback result=%#v id=%q", result, service.rolledBack)
	}
}
