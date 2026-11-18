//go:build !windows

package winproc

// ForegroundExe 在非 Windows 平台暂不使用，返回空串。
func ForegroundExe() string { return "" }
