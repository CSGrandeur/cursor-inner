package catalog

import (
	"bytes"
	"testing"

	"cursor-inner/internal/protox"
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

func TestReasoningSwitchChangesPicker(t *testing.T) {
	off := Available([]Entry{{ID: "deadbeef", DisplayName: "我的模型"}})
	if bytes.Contains(off, []byte("reasoning=")) {
		t.Fatal("reasoning suffix while off")
	}
	if bytes.Contains(off, []byte("Fast")) {
		t.Fatal("fast while off")
	}
	fast := Available([]Entry{{ID: "deadbeef", DisplayName: "我的模型", Fast: true}})
	if !bytes.Contains(fast, []byte("Fast")) {
		t.Fatal("missing fast")
	}
	on := Available([]Entry{{ID: "deadbeef", DisplayName: "我的模型", Reasoning: true}})
	if !bytes.Contains(on, []byte("reasoning=high")) {
		t.Fatal("missing reasoning suffix")
	}
}

func TestConfiguredContextBecomesTheDefault(t *testing.T) {
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "Mine", ContextWindow: 4000}})
	if !bytes.Contains(extra, []byte("context=4k")) || !bytes.Contains(extra, []byte("context=200k")) {
		t.Fatal("context choices missing")
	}
	model := protox.Children(extra, 2)[0]
	var defaults int
	for _, item := range protox.Children(model, 30) {
		fields, err := protox.Fields(item)
		if err != nil {
			t.Fatal(err)
		}
		marked := false
		for _, field := range fields {
			if field.Num == 4 && field.Wire == 0 && field.Val == 1 {
				marked = true
			}
		}
		if !marked {
			continue
		}
		defaults++
		if !bytes.Contains(item, []byte("context=4k")) {
			t.Fatalf("default is %s", protox.StringField(item, 9))
		}
	}
	if defaults != 1 {
		t.Fatal(defaults)
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
