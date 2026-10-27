package provider

import (
	"testing"
)

func feedAll(t *testing.T, a *accumulator, events ...string) Message {
	t.Helper()
	for _, e := range events {
		if _, err := a.feed([]byte(e)); err != nil {
			t.Fatal(err)
		}
	}
	return a.message()
}

// 事件顺序取自一个 OpenAI 兼容服务商的真实 Responses 流：推理项占 output_index 0，
// 两个函数调用各自带 output_index 和 item_id。以前参数增量一律写进第 0 个槽位，
// added 和 done 又各开一个槽位，两个调用变成四个：一个参数是 {A}{B}，一个参数为空。
func TestResponsesParallelCallsKeepTheirOwnArguments(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_text.delta","output_index":0,"item_id":"rs_1","delta":"read the hosts file"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_a","name":"Read","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"\"path\": \"/etc/hosts\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_1","arguments":"{\"path\": \"/etc/hosts\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_a","name":"Read","arguments":"{\"path\": \"/etc/hosts\"}"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"call_b","name":"Shell","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_2","delta":"{\"command\": \"uname -"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_2","delta":"a\"}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"call_b","name":"Shell","arguments":"{\"command\": \"uname -a\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("calls = %+v", msg.ToolCalls)
	}
	if msg.Reasoning != "read the hosts file" {
		t.Fatalf("reasoning_text was dropped: %q", msg.Reasoning)
	}
	want := [][3]string{{"call_a", "Read", `{"path": "/etc/hosts"}`}, {"call_b", "Shell", `{"command": "uname -a"}`}}
	for i, w := range want {
		c := msg.ToolCalls[i]
		if c.ID != w[0] || c.Name != w[1] || c.Arguments != w[2] {
			t.Fatalf("call %d = %+v, want %v", i, c, w)
		}
	}
}

// 参数增量交错到达时也要各归各位。
func TestResponsesInterleavedDeltas(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"a","name":"Read"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"b","name":"Grep"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_2","delta":"{\"pattern\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"\"/a\"}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_2","delta":"\"x\"}"}`,
	)
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0].Arguments != `{"path":"/a"}` || msg.ToolCalls[1].Arguments != `{"pattern":"x"}` {
		t.Fatalf("calls = %+v", msg.ToolCalls)
	}
}

// 正文已经按增量流出来时，output_item.done 里的整段正文不能再追加一遍（界面上同一段话出现两次）。
// 只发 done、不发增量的服务商仍要拿到正文。
func TestResponsesMessageTextIsNotRepeated(t *testing.T) {
	a := newAccumulator("openai-responses", false)
	var shown string
	for _, e := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"item_id":"m1","delta":"key point "}`,
		`{"type":"response.output_text.delta","output_index":0,"item_id":"m1","delta":"is clear."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"key point is clear."}]}}`,
	} {
		p, err := a.feed([]byte(e))
		if err != nil {
			t.Fatal(err)
		}
		shown += p.text
	}
	if msg := a.message(); msg.Content != "key point is clear." || shown != msg.Content {
		t.Fatalf("content=%q shown=%q", msg.Content, shown)
	}
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","content":[{"type":"output_text","text":"only in done"}]}}`,
	)
	if msg.Content != "only in done" {
		t.Fatalf("content=%q", msg.Content)
	}
}

// 一个调用只能出现一次：以前 added 和 done 各登记一次，同一条命令会执行两遍。
// 没有 output_index 的服务商靠 item id 归位。
func TestResponsesSingleCallIsNotDuplicated(t *testing.T) {
	msg := feedAll(t, newAccumulator("openai-responses", false),
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"a","name":"Write"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":\"/x\"}"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"a","name":"Write","arguments":"{\"path\":\"/x\"}"}}`,
	)
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "a" || msg.ToolCalls[0].Arguments != `{"path":"/x"}` {
		t.Fatalf("calls = %+v", msg.ToolCalls)
	}
}
