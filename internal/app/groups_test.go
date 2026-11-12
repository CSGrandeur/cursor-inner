package app

import (
	"testing"

	"cursor-inner/internal/config"
)

func groupApp(t *testing.T) *App {
	t.Helper()
	store, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{store: store}
}

func addTestGroup(t *testing.T, a *App) config.Group {
	t.Helper()
	if err := a.AddGroup(config.Group{
		Name: "一家", Type: "openai-chat", BaseURL: "https://api.example.com/v1", APIKey: "kg", UseProxy: true, Reasoning: true,
	}); err != nil {
		t.Fatal(err)
	}
	groups := a.store.Get().Groups
	if len(groups) != 1 {
		t.Fatalf("groups %+v", groups)
	}
	return groups[0]
}

func TestAddModelInGroupInheritsSharedFields(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	m := a.store.Get().Models[0]
	if m.Type != g.Type || m.BaseURL != g.BaseURL || m.APIKey != g.APIKey || !m.UseProxy || !m.Reasoning {
		t.Fatalf("member did not inherit: %+v", m)
	}
	if len(m.Overrides()) != 0 {
		t.Fatalf("fresh member overrides: %v", m.Overrides())
	}
}

func TestAddModelInGroupWithOverrides(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{
		DisplayName: "乙", Model: "model-b", GroupID: g.ID,
		Reasoning: false, ReasoningSet: true,
		APIKey: "ko", APIKeySet: true,
	}); err != nil {
		t.Fatal(err)
	}
	m := a.store.Get().Models[0]
	if m.Reasoning || m.APIKey != "ko" || m.BaseURL != g.BaseURL {
		t.Fatalf("%+v", m)
	}
	ov := m.Overrides()
	if len(ov) != 2 {
		t.Fatalf("overrides %v", ov)
	}
}

func TestUpdateGroupPropagatesAndKeepsStableIDs(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddModel(config.Model{DisplayName: "乙", Model: "model-b", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	models := a.store.Get().Models
	first := models[0].ID
	if err := a.store.Update(func(f *config.File) error {
		f.Models[1].Fallback = []string{first}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.UpdateGroup(g.ID, config.Group{
		Name: "一家", Type: g.Type, BaseURL: "https://new.example.com/v1", APIKey: "kn", UseProxy: g.UseProxy, Reasoning: g.Reasoning,
	}); err != nil {
		t.Fatal(err)
	}
	got := a.store.Get().Models
	if got[0].BaseURL != "https://new.example.com/v1" || got[0].APIKey != "kn" {
		t.Fatalf("member did not follow: %+v", got[0])
	}
	if got[0].ID != first {
		t.Fatal("grouped member id must stay stable when the group changes endpoint or key")
	}
	if len(got[1].Fallback) != 1 || got[1].Fallback[0] != first {
		t.Fatalf("fallback must keep pointing at the stable id: %v want %s", got[1].Fallback, first)
	}
}

func TestSetGroupProfileSwitchesEndpointKeepsIDs(t *testing.T) {
	a := groupApp(t)
	if err := a.AddGroup(config.Group{
		Name: "一家", Type: "openai-chat", BaseURL: "https://a.example.com/v1", APIKey: "ka",
		Profiles: []config.GroupProfile{
			{ID: "p1", Name: "按量", Type: "openai-chat", BaseURL: "https://a.example.com/v1", APIKey: "ka"},
			{ID: "p2", Name: "套餐", Type: "openai-chat", BaseURL: "https://b.example.com/v1", APIKey: "kb"},
		},
		ActiveProfile: "p1",
	}); err != nil {
		t.Fatal(err)
	}
	g := a.store.Get().Groups[0]
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	id := a.store.Get().Models[0].ID
	if err := a.SetGroupProfile(g.ID, "p2"); err != nil {
		t.Fatal(err)
	}
	cfg := a.store.Get()
	m := cfg.Models[0]
	if m.ID != id || m.BaseURL != "https://b.example.com/v1" || m.APIKey != "kb" {
		t.Fatalf("profile switch must keep id and follow endpoint: %+v", m)
	}
	if cfg.Groups[0].ActiveProfile != "p2" || cfg.Groups[0].APIKey != "kb" {
		t.Fatalf("group mirror wrong: %+v", cfg.Groups[0])
	}
	if err := a.SetGroupProfile(g.ID, "nope"); err == nil {
		t.Fatal("unknown profile must error")
	}
}

func TestUpdateGroupKeepsMemberOverrides(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID, APIKey: "ko", APIKeySet: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.UpdateGroup(g.ID, config.Group{
		Name: g.Name, Type: g.Type, BaseURL: g.BaseURL, APIKey: "kr", UseProxy: g.UseProxy, Reasoning: g.Reasoning,
	}); err != nil {
		t.Fatal(err)
	}
	m := a.store.Get().Models[0]
	if m.APIKey != "ko" {
		t.Fatalf("override must survive group key rotation: %+v", m)
	}
}

func TestDeleteGroupMaterializesMembers(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	before := a.store.Get().Models[0]
	if err := a.DeleteGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	cfg := a.store.Get()
	if len(cfg.Groups) != 0 {
		t.Fatalf("groups %+v", cfg.Groups)
	}
	m := cfg.Models[0]
	if m.GroupID != "" || m.APIKey != before.APIKey || m.BaseURL != before.BaseURL || m.ID != before.ID {
		t.Fatalf("materialized member changed: %+v vs %+v", m, before)
	}
}

func TestSetModelProxyReasoningBecomeOverrides(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	id := a.store.Get().Models[0].ID
	if err := a.SetModelProxy(id, false); err != nil {
		t.Fatal(err)
	}
	if err := a.SetModelReasoning(id, false); err != nil {
		t.Fatal(err)
	}
	m := a.store.Get().Models[0]
	if m.UseProxy || m.Reasoning {
		t.Fatalf("%+v", m)
	}
	ov := m.Overrides()
	if len(ov) != 2 {
		t.Fatalf("overrides %v", ov)
	}
	if err := a.ResetModelOverrides(id, []string{"use_proxy", "reasoning"}); err != nil {
		t.Fatal(err)
	}
	m = a.store.Get().Models[0]
	if !m.UseProxy || !m.Reasoning || len(m.Overrides()) != 0 {
		t.Fatalf("reset did not restore group values: %+v", m)
	}
}

func TestUpdateModelMovesBetweenGroupAndStandalone(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	if err := a.AddModel(config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	id := a.store.Get().Models[0].ID
	// 移出组：带完整字段、group_id 空。
	if err := a.UpdateModel(id, config.Model{
		DisplayName: "甲", Type: "openai-chat", BaseURL: "https://api.example.com/v1", APIKey: "kg", Model: "model-a",
	}); err != nil {
		t.Fatal(err)
	}
	m := a.store.Get().Models[0]
	if m.GroupID != "" || m.APIKey != "kg" {
		t.Fatalf("leave group: %+v", m)
	}
	// 再入组：只带模型名与组 id。
	if err := a.UpdateModel(m.ID, config.Model{DisplayName: "甲", Model: "model-a", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	m = a.store.Get().Models[0]
	if m.GroupID != g.ID || m.APIKey != "kg" || len(m.Overrides()) != 0 {
		t.Fatalf("join group: %+v", m)
	}
}

func TestGroupKeyAndValidation(t *testing.T) {
	a := groupApp(t)
	g := addTestGroup(t, a)
	key, err := a.GroupKey(g.ID)
	if err != nil || key != "kg" {
		t.Fatalf("%q %v", key, err)
	}
	if _, err := a.GroupKey("nope"); err == nil {
		t.Fatal("missing group must error")
	}
	if err := a.AddGroup(config.Group{Name: "坏", Type: "openai-chat", BaseURL: "https://x.example.com", APIKey: ""}); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if err := a.AddGroup(config.Group{Name: "坏", Type: "ftp", BaseURL: "https://x.example.com", APIKey: "k"}); err == nil {
		t.Fatal("bad type must be rejected")
	}
}
