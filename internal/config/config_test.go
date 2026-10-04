package config

import "testing"

func TestLoadWritesDefaultAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if !got.Takeover || !got.Proxy.Enabled || got.Autostart || got.Proxy.Address != "" {
		t.Fatalf("%+v", got)
	}
	if err := s.Update(func(f *File) error {
		f.Autostart = true
		f.Proxy.Address = "127.0.0.1:1080"
		f.Models = []Model{{ID: "abc", DisplayName: "本地", Type: "openai-chat", Model: "qwen"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got = s2.Get()
	if !got.Autostart || got.Proxy.Address != "127.0.0.1:1080" || len(got.Models) != 1 || got.Models[0].ID != "abc" {
		t.Fatalf("%+v", got)
	}
}

func TestLastTestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := int64(80)
	if err := s.Update(func(f *File) error {
		f.Models = []Model{{
			ID: "abc", DisplayName: "本地", Type: "openai-chat", Model: "qwen",
			LastTest: &LastTest{
				OK: true, At: "2026-10-04T11:00:00Z", DurationMS: 1284,
				FirstValidResponseMS: &first, OutputTokens: 42,
				TokensPerSecond: 38.6, Output: "1 2 3",
			},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get().Models[0].LastTest
	if got == nil || !got.OK || got.OutputTokens != 42 || got.TokensPerSecond != 38.6 || got.FirstValidResponseMS == nil || *got.FirstValidResponseMS != 80 {
		t.Fatalf("%+v", got)
	}
}

func TestKeyHint(t *testing.T) {
	if (Model{APIKey: "sk-abcdef"}).KeyHint() != "····cdef" {
		t.Fatal((Model{APIKey: "sk-abcdef"}).KeyHint())
	}
}
