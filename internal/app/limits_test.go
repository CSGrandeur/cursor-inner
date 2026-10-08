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
