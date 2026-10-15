// Package scripts 把仓库里的辅助脚本编进程序，单独下载的 exe 旁边没有 scripts 目录也能用。
package scripts

import _ "embed"

// GrokLeakCheck 是 grok-leak-check.ps1 的内容。
//
//go:embed grok-leak-check.ps1
var GrokLeakCheck []byte
