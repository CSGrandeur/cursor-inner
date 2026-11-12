package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor-inner/internal/config"
)

func TestEffortPolicyQwen4OfficialSet(t *testing.T) {
	levels, def := EffortPolicy(config.Model{Model: "qwen4-max-preview"})
	if strings.Join(levels, ",") != "low,medium,xhigh" || def != "xhigh" {
		t.Fatalf("qwen4-max-preview levels=%v def=%s", levels, def)
	}
	levels, def = EffortPolicy(config.Model{Model: "openrouter/qwen4-plus:free"})
	if strings.Join(levels, ",") != "low,medium,xhigh" || def != "xhigh" {
		t.Fatalf("qwen4 family levels=%v def=%s", levels, def)
	}
}

func TestEffortPolicyUnknownKeepsGeneric(t *testing.T) {
	levels, def := EffortPolicy(config.Model{Model: "some-local-7b"})
	if strings.Join(levels, ",") != "low,medium,high,xhigh,max" || def != "high" {
		t.Fatalf("unknown levels=%v def=%s", levels, def)
	}
	levels, def = EffortPolicy(config.Model{Model: "deepseek-v4.1-flash-1017"})
	if strings.Join(levels, ",") != "low,medium,high,xhigh,max" || def != "high" {
		t.Fatalf("deepseek (undocumented set) must stay generic, got %v %s", levels, def)
	}
	levels, def = EffortPolicy(config.Model{Model: "qwen3-max"})
	if strings.Join(levels, ",") != "low,medium,high,xhigh,max" || def != "high" {
		t.Fatalf("qwen3 must not inherit qwen4 set, got %v %s", levels, def)
	}
}

func TestEffortPolicyConfigOverrideAndOldJSON(t *testing.T) {
	raw := []byte(`{"display_name":"M","type":"openai-responses","base_url":"https://example.com/v1","model":"qwen4-max-preview"}`)
	var m config.Model
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.EffortLevels != nil || m.DefaultEffort != "" {
		t.Fatalf("old json must omit new fields: %+v", m)
	}
	levels, def := EffortPolicy(m)
	if strings.Join(levels, ",") != "low,medium,xhigh" || def != "xhigh" {
		t.Fatalf("old json still gets family policy: %v %s", levels, def)
	}
	custom := config.Model{Model: "qwen4-max-preview", EffortLevels: []string{"low", "xhigh"}, DefaultEffort: "low"}
	levels, def = EffortPolicy(custom)
	if strings.Join(levels, ",") != "low,xhigh" || def != "low" {
		t.Fatalf("custom override: %v %s", levels, def)
	}
}

func TestResolveEffortStaleHighMaxForQwen4(t *testing.T) {
	m := config.Model{Model: "qwen4-max-preview"}
	if got := ResolveEffort(m, "low"); got != "low" {
		t.Fatal(got)
	}
	if got := ResolveEffort(m, "medium"); got != "medium" {
		t.Fatal(got)
	}
	if got := ResolveEffort(m, "xhigh"); got != "xhigh" {
		t.Fatal(got)
	}
	if got := ResolveEffort(m, "high"); got != "xhigh" {
		t.Fatalf("stale high must not be sent, want default xhigh, got %q", got)
	}
	if got := ResolveEffort(m, "max"); got != "xhigh" {
		t.Fatalf("stale max must not be sent, want default xhigh, got %q", got)
	}
	if got := ResolveEffort(m, ""); got != "" {
		t.Fatalf("empty selection must stay omitted, got %q", got)
	}
}

func TestResolveEffortGenericAcceptsHigh(t *testing.T) {
	m := config.Model{Model: "other-model"}
	if got := ResolveEffort(m, "high"); got != "high" {
		t.Fatal(got)
	}
	if got := ResolveEffort(m, "max"); got != "max" {
		t.Fatal(got)
	}
}
