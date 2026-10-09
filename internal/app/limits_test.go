package app

import (
	"testing"

	"cursor-inner/internal/config"
)

func TestSetModelLimitsStoresTokenCounts(t *testing.T) {
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := &App{store: store}
	if err := application.AddModel(config.Model{
		DisplayName: "Mine", Type: "openai-chat", BaseURL: "https://example.com/v1", APIKey: "k", Model: "m",
	}); err != nil {
		t.Fatal(err)
	}
	id := store.Get().Models[0].ID
	window, maxOut := 4000, 128
	if err := application.SetModelLimits(id, &window, &maxOut); err != nil {
		t.Fatal(err)
	}
	got := store.Get().Models[0]
	if got.ContextWindow != 4000 || got.MaxOutputTokens != 128 {
		t.Fatalf("%d %d", got.ContextWindow, got.MaxOutputTokens)
	}
	zero := 0
	if err := application.SetModelLimits(id, &zero, nil); err != nil {
		t.Fatal(err)
	}
	if store.Get().Models[0].ContextWindow != 0 || store.Get().Models[0].MaxOutputTokens != 128 {
		t.Fatal("clear context")
	}
	negative := -1
	if err := application.SetModelLimits(id, &negative, nil); err == nil {
		t.Fatal("accepted a negative count")
	}
}

func TestUpdateModelKeepsKeyAndReplacesInPlace(t *testing.T) {
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	application := &App{store: store}
	add := func(name, modelName, key string) string {
		t.Helper()
		if err := application.AddModel(config.Model{
			DisplayName: name, Type: "openai-chat", BaseURL: "https://example.com/v1", APIKey: key, Model: modelName,
		}); err != nil {
			t.Fatal(err)
		}
		models := store.Get().Models
		return models[len(models)-1].ID
	}
	id := add("Mine", "m", "secret-key")
	if err := store.Update(func(f *config.File) error {
		f.Models[0].LastTest = &config.LastTest{OK: true, Output: "1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := application.UpdateModel(id, config.Model{
		DisplayName: "Renamed", Type: "openai-chat", BaseURL: "https://example.com/v1", Model: "m",
		UseProxy: true, ContextWindow: 8000,
	}); err != nil {
		t.Fatal(err)
	}
	got := store.Get().Models
	if len(got) != 1 || got[0].DisplayName != "Renamed" || got[0].APIKey != "secret-key" || got[0].ID == id {
		t.Fatalf("%+v", got)
	}
	if got[0].ContextWindow != 8000 || !got[0].UseProxy || got[0].LastTest == nil || got[0].LastTest.Output != "1" {
		t.Fatalf("fields %+v", got[0])
	}
	other := add("Other", "other", "k2")
	if err := application.UpdateModel(got[0].ID, config.Model{
		DisplayName: "Other", Type: "openai-chat", BaseURL: "https://example.com/v1", APIKey: "k2", Model: "other",
	}); err == nil {
		t.Fatal("accepted a duplicate")
	}
	if err := application.UpdateModel(got[0].ID, config.Model{
		DisplayName: "Renamed", Type: "openai-chat", BaseURL: "https://example.com/v2", Model: "m",
	}); err != nil {
		t.Fatal(err)
	}
	moved := store.Get().Models[0]
	if moved.BaseURL != "https://example.com/v2" || moved.LastTest != nil || moved.APIKey != "secret-key" {
		t.Fatalf("%+v", moved)
	}
	if err := application.UpdateModel(moved.ID, config.Model{
		DisplayName: "Renamed", Type: "anthropic", BaseURL: "https://example.com/v1", Model: "m", FastSupport: true,
	}); err != nil {
		t.Fatal(err)
	}
	if store.Get().Models[0].FastSupport {
		t.Fatal("kept Fast on anthropic")
	}
	if err := application.UpdateModel("missing", config.Model{
		DisplayName: "X", Type: "openai-chat", BaseURL: "https://example.com/v1", APIKey: "k", Model: "m",
	}); err == nil {
		t.Fatal("missing id")
	}
	if store.Get().Models[1].ID != other {
		t.Fatal("second model moved")
	}
}
