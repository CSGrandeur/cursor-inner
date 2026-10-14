package program

import "strings"

// restoreBalloon 是启动时的提示：上次退出关掉了哪些程序，点气泡打开配置页一键恢复。
func restoreBalloon(zh bool, names []string) balloon {
	var shown []string
	for _, n := range names {
		switch n {
		case "grok":
			shown = append(shown, "Grok Bot")
		case "codex":
			shown = append(shown, "Codex")
		}
	}
	if zh {
		return balloon{Title: "上次退出时关闭了 " + strings.Join(shown, " 和 "), Text: "点这里打开配置页一键恢复，照常走代理。", Page: "restore"}
	}
	return balloon{Title: "Closed on last quit: " + strings.Join(shown, " and "), Text: "Click to open settings and restore them, with the proxy as before.", Page: "restore"}
}
