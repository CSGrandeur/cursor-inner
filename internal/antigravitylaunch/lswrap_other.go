//go:build !windows

package antigravitylaunch

// RestoreLanguageServer 非 Windows 无需处理。
func RestoreLanguageServer(antiExe string) error { return nil }
