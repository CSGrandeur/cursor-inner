package tunnel

import _ "embed"

// Script 是经 UAC 启动/停止 sing-box TUN 的 PowerShell 脚本，与 scripts/tun-egress.ps1 保持一致
// （有测试校验两者字节相同）。它会在缺少 sing-box 时按固定 URL 下载并校验 zip 哈希后解压。
//
//go:embed tun-egress.ps1
var Script string
