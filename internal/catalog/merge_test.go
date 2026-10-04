package catalog

import (
	"cursor-inner/internal/protox"
	"testing"
)

func TestAppendKeepsUpstreamName(t *testing.T) {
	upstream := protox.Frame(0, protox.AppendString(nil, 1, "claude-opus"))
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "我的模型"}})
	merged := protox.MergeFramed(upstream, extra)
	payload, _ := protox.Unary(merged)
	names := protox.Children(payload, 1)
	if len(names) != 2 || string(names[0]) != "claude-opus" || string(names[1]) != "deadbeef" {
		t.Fatalf("%q", names)
	}
	model := protox.Children(payload, 2)
	if len(model) != 1 || protox.StringField(model[0], 17) != "我的模型" {
		t.Fatal("display")
	}
}

func TestAvailableHasPickerVariants(t *testing.T) {
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "我的模型"}})
	models := protox.Children(extra, 2)
	if len(models) != 1 {
		t.Fatal(len(models))
	}
	model := models[0]
	if protox.Child(model, 29) == nil {
		t.Fatal("missing parameter_definitions")
	}
	if protox.Child(model, 30) == nil {
		t.Fatal("missing variants")
	}
	if protox.StringField(model, 24) != "我的模型" {
		t.Fatal("short name")
	}
}

func TestUsableHasLocalCredentials(t *testing.T) {
	extra := Usable([]Entry{{ID: "deadbeef", DisplayName: "我的模型"}})
	models := protox.Children(extra, 1)
	if len(models) != 1 {
		t.Fatal(len(models))
	}
	if protox.Child(models[0], 8) == nil {
		t.Fatal("missing api_key credentials")
	}
}
