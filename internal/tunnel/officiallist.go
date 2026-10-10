// Package tunnel 生成并管理一个最小化的 sing-box TUN：只捕获官方域名（grok/x.ai/Cursor 等），
// 转发到用户已有的 v2rayN 代理端口，其它流量完全不碰。TUN 需要管理员权限，由 cursor-inner 在
// 用户明确确认后经 UAC 启动；任何失败都回退到普通的代理接管，绝不阻断启动或改坏网络。
package tunnel

// OfficialSuffixes 是必须经代理的官方域名后缀，与 scripts/grok-leak-check.ps1、strict-egress.ps1 保持一致。
var OfficialSuffixes = []string{
	"grok.com",
	"x.ai",
	"spacexai.com",
	"cursor.com",
	"cursor.sh",
	"cursorapi.com",
	"cursor-cdn.com",
	"cursorvm.com",
	"anysphere.co",
}
