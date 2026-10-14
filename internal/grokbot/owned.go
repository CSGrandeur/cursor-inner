package grokbot

import "strings"

// OwnedBy 报告这些命令行里是否有进程带着 proxyURL 这个代理（即由该 cursor-inner 实例接管）。
// proxyURL 为空（旧版接管标记没记代理）时退回旧判断：带任意本程序代理参数即算。
// 记了代理就只认它，另一个数据目录的实例退出时不会误关别人接管的进程。
func OwnedBy(cmds []string, proxyURL string) bool {
	if proxyURL == "" {
		return ShouldRestart(cmds, "")
	}
	for _, cmd := range cmds {
		if cmd == "" || strings.Contains(cmd, "--type=") {
			continue
		}
		if Routed(cmd, proxyURL) {
			return true
		}
	}
	return false
}
