package provider

import (
	"encoding/json"
	"testing"

	"cursor-inner/internal/config"
)

func officialWriteParams() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"contents":{"description":"The contents to write to the file","type":"string"},"path":{"description":"The absolute path to the file to modify","type":"string"}},"required":["path","contents"]}`)
}

func officialReadParams() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"},"offset":{"type":"integer"},"path":{"description":"The absolute path of the file to read.","type":"string"}},"required":["path"]}`)
}

func mustResponsesTools(t *testing.T, req chatRequest) []map[string]any {
	t.Helper()
	raw, err := responsesBody(config.Model{Type: "openai-responses", Model: "qwen4-max-preview"}, req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	tools, _ := body["tools"].([]any)
	out := make([]map[string]any, 0, len(tools))
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		out = append(out, tool)
	}
	return out
}

func assertFlatTool(t *testing.T, tool map[string]any, name string, required ...string) {
	t.Helper()
	if tool["type"] != "function" || tool["name"] != name || tool["function"] != nil || tool["parameters"] == nil {
		t.Fatalf("tool %s is not official flat schema: %v", name, tool)
	}
	params, _ := tool["parameters"].(map[string]any)
	reqd, _ := params["required"].([]any)
	got := map[string]bool{}
	for _, r := range reqd {
		s, _ := r.(string)
		got[s] = true
	}
	for _, r := range required {
		if !got[r] {
			t.Fatalf("tool %s missing required %s: %v", name, r, tool["parameters"])
		}
	}
	props, _ := params["properties"].(map[string]any)
	for _, r := range required {
		if props[r] == nil {
			t.Fatalf("tool %s missing property %s: %v", name, r, tool["parameters"])
		}
	}
}

func TestProductionResponsesWriteReadOfficialSchema(t *testing.T) {
	tools := mustResponsesTools(t, chatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools: []Tool{
			{Name: "Write", Description: "Writes a file", Parameters: officialWriteParams()},
			{Name: "Read", Description: "Reads a file", Parameters: officialReadParams()},
		},
		MaxTokens: 1000,
	})
	if len(tools) != 2 {
		t.Fatalf("tools=%v", tools)
	}
	assertFlatTool(t, tools[0], "Write", "path", "contents")
	assertFlatTool(t, tools[1], "Read", "path")
}

func TestProbeAndProductionResponsesToolsBothFlat(t *testing.T) {
	prod := mustResponsesTools(t, chatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []Tool{{Name: "Write", Description: "w", Parameters: officialWriteParams()}},
	})
	probe := mustResponsesTools(t, chatRequest{
		Messages:        []Message{{Role: "user", Content: "hi"}},
		Tools:           []Tool{{Name: "Write", Description: "w", Parameters: officialWriteParams()}},
		ToolChoice:      "required",
		ReasoningEffort: "low",
		OmitInclude:     true,
		FlatTools:       true,
	})
	assertFlatTool(t, prod[0], "Write", "path", "contents")
	assertFlatTool(t, probe[0], "Write", "path", "contents")
	pb, _ := json.Marshal(prod[0])
	gb, _ := json.Marshal(probe[0])
	if string(pb) != string(gb) {
		t.Fatalf("probe and production tool objects differ:\nprod=%s\nprobe=%s", pb, gb)
	}
}

func TestResponsesStringArgumentsAreKeptComplete(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"Write","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"path\":\"/data/tmp/t84u.txt\",\"contents\":\"ok\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"path\":\"/data/tmp/t84u.txt\",\"contents\":\"ok\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"Write","arguments":"{\"path\":\"/data/tmp/t84u.txt\",\"contents\":\"ok\"}"}}`,
	)
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Name != "Write" {
		t.Fatalf("calls=%v", msg.ToolCalls)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(msg.ToolCalls[0].Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "/data/tmp/t84u.txt" || args["contents"] != "ok" {
		t.Fatalf("incomplete string arguments: %s", msg.ToolCalls[0].Arguments)
	}
}

func TestResponsesObjectArgumentsAreIgnored(t *testing.T) {
	// 当前解析器把 arguments 解成 string；对象形态会使整段事件 Unmarshal 失败并被忽略。
	// 这是另一项风险，此处只记录现有行为，不在本修复中扩大改动。
	// 真测里千问返回的是 string "{}" 或完整 JSON，不是对象。
	done := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"Write","arguments":{"path":"/p","contents":"ok"}}}`
	var ev struct {
		Item struct {
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(done), &ev); err == nil {
		t.Fatal("expected object arguments to fail string unmarshal")
	}
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"Write"}}`,
		done,
	)
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("object arguments are ignored and empty args are dropped, got %v", msg.ToolCalls)
	}
}
