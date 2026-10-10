// Package egress 提供可选的「严格模式」：用 Windows 防火墙禁止 Grok Bot / Cursor 直连官方端点。
// 规则由内嵌的 strict-egress.ps1 以管理员身份创建（需要 UAC），cursor-inner 自己不带管理员权限。
package egress

import (
	_ "embed"
	"fmt"
	"regexp"
)

//go:embed strict-egress.ps1
var Script []byte

// safeProxy 只放行 http://主机:端口 形式的地址，拼进命令行时不会被解释成别的参数。
var safeProxy = regexp.MustCompile(`^http://[A-Za-z0-9.\-]+(:[0-9]+)?$|^http://\[[0-9A-Fa-f:.]+\](:[0-9]+)?$`)

// Arguments 返回 powershell.exe 的参数串。proxyURL 不合规时不带 -Proxy（脚本会从运行中的 Grok 读取）。
func Arguments(path string, enable bool, proxyURL string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty script path")
	}
	action := "-Disable"
	if enable {
		action = "-Enable"
	}
	args := fmt.Sprintf(`-NoProfile -ExecutionPolicy Bypass -NoExit -File "%s" %s`, path, action)
	if enable && safeProxy.MatchString(proxyURL) {
		args += ` -Proxy "` + proxyURL + `"`
	}
	return args, nil
}
