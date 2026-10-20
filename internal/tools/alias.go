package tools

import (
	"encoding/json"

	"cursor-inner/internal/provider"
)

// Canonical 把模型按 Cursor 新版工具名发来的调用改成本机实现的等价工具。
// Cursor 新版客户端的提示和官方轮次里出现 GetDynamicTools / CallDynamicTool（MCP 动态工具），
// 自定义模型照着调，本机没有同名实现就报“is not available”，等于丢掉 MCP 能力。
// 它们和 GetMcpTools / CallMcpTool 是同一件事，参数里的 namespace 就是 server。
func Canonical(call provider.ToolCall) provider.ToolCall {
	var target string
	switch normalize(call.Name) {
	case "getdynamictools":
		target = "GetMcpTools"
	case "calldynamictool":
		target = "CallMcpTool"
	default:
		return call
	}
	call.Name = target
	var args map[string]any
	if json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
		return call
	}
	if ns, ok := args["namespace"]; ok {
		if _, has := args["server"]; !has {
			args["server"] = ns
		}
		delete(args, "namespace")
	}
	if raw, err := json.Marshal(args); err == nil {
		call.Arguments = string(raw)
	}
	return call
}
