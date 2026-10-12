package codexbot

import "strings"

// isDesktopShell 判断这是不是 Codex 桌面版的窗口进程。
// 桌面版是 Electron 壳（app\Codex.exe 或 app\ChatGPT.exe，包名 OpenAI.Codex），
// 再拉起 resources\codex.exe app-server。命令行里的 app-server、--type= 和 CLI 子命令不是这个壳，不能按桌面进程关掉。
func isDesktopShell(exe, cmdline string) bool {
	if exe == "" {
		return false
	}
	cmd := strings.ToLower(cmdline)
	if strings.Contains(cmd, "--type=") || strings.Contains(cmd, "app-server") {
		return false
	}
	for _, verb := range []string{" exec", " resume", " login", " mcp", " sandbox"} {
		if strings.Contains(cmd, verb) {
			return false
		}
	}
	base := strings.ToLower(baseName(exe))
	if base != "codex.exe" && base != "chatgpt.exe" {
		return false
	}
	p := strings.ToLower(strings.ReplaceAll(exe, "\\", "/"))
	if strings.Contains(p, "/resources/") || strings.Contains(p, "/openai/codex/bin/") {
		return false
	}
	if strings.Contains(p, "/windowsapps/openai.codex") {
		return strings.Contains(p, "/app/")
	}
	if strings.Contains(p, "/programs/codex/") || strings.Contains(p, "/programs/chatgpt/") {
		return true
	}
	if strings.HasSuffix(p, "/app/codex.exe") || strings.HasSuffix(p, "/app/chatgpt.exe") {
		return true
	}
	if strings.HasSuffix(p, "/codex/codex.exe") || strings.HasSuffix(p, "/chatgpt/chatgpt.exe") {
		return true
	}
	return false
}

func baseName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
