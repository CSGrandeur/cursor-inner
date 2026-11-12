package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func groupStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := Group{ID: "g1", Name: "一家", Type: "openai-chat", BaseURL: "https://api.example.com/v1", APIKey: "kg", UseProxy: true, Reasoning: true}
	if err := s.Update(func(f *File) error {
		f.Groups = append(f.Groups, g)
		f.Models = append(f.Models, Model{ID: "m1", DisplayName: "甲", Model: "model-a", GroupID: "g1"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestGroupValuesResolveOnGet(t *testing.T) {
	s, _ := groupStore(t)
	m := s.Get().Models[0]
	if m.Type != "openai-chat" || m.BaseURL != "https://api.example.com/v1" || m.APIKey != "kg" || !m.UseProxy || !m.Reasoning {
		t.Fatalf("group values not applied: %+v", m)
	}
	if len(m.Overrides()) != 0 {
		t.Fatalf("fresh member should not override anything: %v", m.Overrides())
	}
}

func TestGroupMemberPersistsSparse(t *testing.T) {
	_, dir := groupStore(t)
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	m := onDisk.Models[0]
	for _, key := range []string{"type", "base_url", "api_key", "use_proxy", "reasoning", "type_set", "api_key_set"} {
		if v, ok := m[key]; ok && v != "" && v != false {
			t.Fatalf("shared field %s must stay sparse on disk, got %v", key, v)
		}
	}
	// 重新加载后仍解析出完整配置。
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().Models[0]; got.APIKey != "kg" || got.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("reload lost group values: %+v", got)
	}
}

func TestGroupMemberOverrideRoundTrip(t *testing.T) {
	s, dir := groupStore(t)
	if err := s.Update(func(f *File) error {
		f.Models[0].Reasoning = false
		f.Models[0].ReasoningSet = true
		f.Models[0].APIKey = "ko"
		f.Models[0].APIKeySet = true
		f.Models[0].UseProxy = true // 与组相同且未置位：仍是跟随
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := s.Get().Models[0]
	if m.Reasoning || m.APIKey != "ko" || !m.UseProxy {
		t.Fatalf("%+v", m)
	}
	want := map[string]bool{"api_key": true, "reasoning": true}
	got := map[string]bool{}
	for _, k := range m.Overrides() {
		got[k] = true
	}
	if len(got) != 2 || !got["api_key"] || !got["reasoning"] {
		t.Fatalf("overrides %v, want %v", got, want)
	}
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	m2 := s2.Get().Models[0]
	if m2.Reasoning || m2.APIKey != "ko" {
		t.Fatalf("reload lost overrides: %+v", m2)
	}
	// 改回与组相同的值（应用层同时清覆盖位）即回到跟随。
	if err := s2.Update(func(f *File) error {
		f.Models[0].APIKey = "kg"
		f.Models[0].APIKeySet = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ov := s2.Get().Models[0].Overrides(); len(ov) != 1 || ov[0] != "reasoning" {
		t.Fatalf("want only reasoning override, got %v", ov)
	}
}

func TestGroupEditPropagatesToMembers(t *testing.T) {
	s, _ := groupStore(t)
	if err := s.Update(func(f *File) error {
		f.Groups[0].BaseURL = "https://new.example.com/v1"
		f.Groups[0].APIKey = "kn"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := s.Get().Models[0]
	if m.BaseURL != "https://new.example.com/v1" || m.APIKey != "kn" {
		t.Fatalf("member did not follow group edit: %+v", m)
	}
}

func TestDanglingGroupMaterializesMember(t *testing.T) {
	s, dir := groupStore(t)
	if err := s.Update(func(f *File) error {
		f.Groups = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := s.Get().Models[0]
	if m.GroupID != "" || m.APIKey != "kg" || m.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("member must keep effective values when group is gone: %+v", m)
	}
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().Models[0]; got.APIKey != "kg" || got.Type != "openai-chat" {
		t.Fatalf("reload lost materialized values: %+v", got)
	}
}

func TestStandaloneModelKeepsOwnValues(t *testing.T) {
	s, _ := groupStore(t)
	if err := s.Update(func(f *File) error {
		f.Models = append(f.Models, Model{ID: "m2", DisplayName: "独立", Type: "anthropic", BaseURL: "https://other.example.com", APIKey: "ks", Model: "model-b"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := s.Get().Models[1]
	if m.Type != "anthropic" || m.APIKey != "ks" || m.GroupID != "" || len(m.Overrides()) != 0 {
		t.Fatalf("%+v", m)
	}
}

func TestPeekResolvesGroups(t *testing.T) {
	_, dir := groupStore(t)
	f, ok := Peek(dir)
	if !ok {
		t.Fatal("peek failed")
	}
	if f.Models[0].APIKey != "kg" {
		t.Fatalf("peek must see resolved values: %+v", f.Models[0])
	}
}
