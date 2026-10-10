package provider

import "testing"

func TestRescueXMLToolCall(t *testing.T) {
	in := `I'll read it.
<tool_call>
<tool_name>Read</tool_name>
<arguments>{"path":"/w/a.go"}</arguments>
</tool_call>`
	clean, calls := RescueToolCalls(in, []Tool{{Name: "Read"}})
	if len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("%+v", calls)
	}
	if stringsContains(clean, "<tool_call>") {
		t.Fatal(clean)
	}
}

func TestRescueFuzzyToolName(t *testing.T) {
	in := "```json\n{\"name\":\"str_replace\",\"arguments\":{\"path\":\"/w/a.go\",\"old_string\":\"a\",\"new_string\":\"b\"}}\n```"
	_, calls := RescueToolCalls(in, []Tool{{Name: "StrReplace"}, {Name: "Read"}})
	if len(calls) != 1 || calls[0].Name != "StrReplace" {
		t.Fatalf("%+v", calls)
	}
}

func TestRescueDSML(t *testing.T) {
	in := `<|tool_calls_section_begin|>
<|tool_call_begin|>
Read
<|tool_call_argument_begin|>
{"path":"/w/a.go"}
<|tool_call_end|>`
	_, calls := RescueToolCalls(in, []Tool{{Name: "Read"}})
	if len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("%+v", calls)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexString(s, sub) >= 0)
}

func indexString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
