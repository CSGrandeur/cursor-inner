//go:build !windows

package antigravitylaunch

// EnsureLanguageServerProxy 非 Windows 暂不包装 language_server。
func EnsureLanguageServerProxy(antiExe, proxyURL string) error { return nil }

// RestoreLanguageServer 非 Windows 无包装可还原。
func RestoreLanguageServer(antiExe string) error { return nil }

// LanguageServerProxyReady 非 Windows 无包装，视为已就绪以免无谓重启。
func LanguageServerProxyReady(antiExe, proxyURL string) bool { return true }
