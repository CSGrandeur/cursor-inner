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

// defaultVariant 返回被标成默认的 variant 的 model id 后缀（field 9）。
func defaultVariant(t *testing.T, available []byte) string {
	t.Helper()
	model := protox.Children(available, 2)[0]
	var defaults []string
	for _, item := range protox.Children(model, 30) {
		fields, err := protox.Fields(item)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range fields {
			if field.Num == 4 && field.Wire == 0 && field.Val == 1 {
				defaults = append(defaults, protox.StringField(item, 9))
			}
		}
	}
	if len(defaults) != 1 {
		t.Fatalf("default variants = %v", defaults)
	}
	return defaults[0]
}

func TestConfiguredContextBecomesTheDefault(t *testing.T) {
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "Mine", ContextWindow: 4000}})
	if !bytes.Contains(extra, []byte("context=4k")) {
		t.Fatal("context choices missing")
	}
	if got := defaultVariant(t, extra); !bytes.Contains([]byte(got), []byte("context=4k")) {
		t.Fatalf("default is %s", got)
	}
}

// 模型装不下的档位不该出现在菜单里：选中后会被按模型上限截断，用户只会以为选择器没生效。
func TestContextChoicesStopAtTheModelWindow(t *testing.T) {
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "Mine", ContextWindow: 128000}})
	for _, over := range []string{"context=200k", "context=356k", "context=500k", "context=800k", "context=1m"} {
		if bytes.Contains(extra, []byte(over)) {
			t.Fatalf("%s should not be offered by a 128K model", over)
		}
	}
	if !bytes.Contains(extra, []byte("context=128k")) {
		t.Fatal("missing 128k")
	}
	if got := defaultVariant(t, extra); !bytes.Contains([]byte(got), []byte("context=128k")) {
		t.Fatalf("default is %s", got)
	}
}

// 上限正好是固定档位时，保留全部不超限的档位，并把这一档设为默认。
func TestContextChoicesAtOneMillionKeepTheFixedList(t *testing.T) {
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "Mine", ContextWindow: 1000000}})
	for _, want := range []string{"context=200k", "context=356k", "context=500k", "context=800k", "context=1m"} {
		if !bytes.Contains(extra, []byte(want)) {
			t.Fatalf("missing %s", want)
		}
	}
	if got := defaultVariant(t, extra); !bytes.Contains([]byte(got), []byte("context=1m")) {
		t.Fatalf("default is %s", got)
	}
}

// 上限夹在固定档位之间时，上限自己作为额外档位插到最前并成为默认，更大的档位仍然不出现。
func TestContextChoicesInsertTheWindowBetweenFixedOnes(t *testing.T) {
	extra := Available([]Entry{{ID: "deadbeef", DisplayName: "Mine", ContextWindow: 300000}})
	if bytes.Contains(extra, []byte("context=356k")) || bytes.Contains(extra, []byte("context=800k")) {
		t.Fatal("choices above the window must not be offered")
	}
	if !bytes.Contains(extra, []byte("context=300k")) || !bytes.Contains(extra, []byte("context=200k")) {
		t.Fatal("missing 300k or 200k")
	}
	if got := defaultVariant(t, extra); !bytes.Contains([]byte(got), []byte("context=300k")) {
		t.Fatalf("default is %s", got)
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
