//go:build windows

package antigravitylaunch

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

//go:embed shim/lswrap.exe
var lsWrapShim []byte

const (
	lsName     = "language_server.exe"
	lsRealName = "language_server.exe.cursor-inner-real"
	lsMarkName = "language_server.exe.cursor-inner-wrap"
)

// LanguageServerProxyReady 报告包装是否已按当前 shim 与 proxyURL 就位。
func LanguageServerProxyReady(antiExe, proxyURL string) bool {
	binDir, err := languageServerDir(antiExe)
	if err != nil || strings.TrimSpace(proxyURL) == "" {
		return false
	}
	mark := filepath.Join(binDir, lsMarkName)
	cur, err := os.ReadFile(mark)
	if err != nil {
		return false
	}
	shimSum := sha256.Sum256(lsWrapShim)
	want := hex.EncodeToString(shimSum[:]) + "\n" + proxyURL + "\n"
	if string(cur) != want {
		return false
	}
	return sameFileContent(filepath.Join(binDir, lsName), lsWrapShim)
}

// EnsureLanguageServerProxy 在 Antigravity 安装目录的 resources/bin 下，
// 用本程序自带的包装替换 language_server.exe，启动时补 --proxy_server_url，
// 并再次钉死 HTTPS_PROXY（Electron 的 shellEnvSync 可能冲掉进程环境）。
// proxyURL 为空时还原官方二进制。
func EnsureLanguageServerProxy(antiExe, proxyURL string) error {
	binDir, err := languageServerDir(antiExe)
	if err != nil {
		return err
	}
	if strings.TrimSpace(proxyURL) == "" {
		return restoreLanguageServer(binDir)
	}
	return installLanguageServerWrap(binDir, proxyURL)
}

// RestoreLanguageServer 去掉包装，把官方 language_server.exe 放回去。
func RestoreLanguageServer(antiExe string) error {
	binDir, err := languageServerDir(antiExe)
	if err != nil {
		return nil
	}
	return restoreLanguageServer(binDir)
}

func languageServerDir(antiExe string) (string, error) {
	antiExe = strings.TrimSpace(antiExe)
	if antiExe == "" {
		return "", fmt.Errorf("empty antigravity exe")
	}
	dir := filepath.Join(filepath.Dir(antiExe), "resources", "bin")
	if _, err := os.Stat(filepath.Join(dir, lsName)); err == nil {
		return dir, nil
	}
	if _, err := os.Stat(filepath.Join(dir, lsRealName)); err == nil {
		return dir, nil
	}
	return "", fmt.Errorf("language_server not found under %s", dir)
}

func installLanguageServerWrap(binDir, proxyURL string) error {
	exe := filepath.Join(binDir, lsName)
	real := filepath.Join(binDir, lsRealName)
	mark := filepath.Join(binDir, lsMarkName)
	shimSum := sha256.Sum256(lsWrapShim)
	wantMark := hex.EncodeToString(shimSum[:]) + "\n" + proxyURL + "\n"

	if cur, err := os.ReadFile(mark); err == nil && string(cur) == wantMark {
		if sameFileContent(exe, lsWrapShim) {
			return nil
		}
	}

	if _, err := os.Stat(real); err != nil {
		if err := os.Rename(exe, real); err != nil {
			return err
		}
		slog.Info("已备份 Antigravity language_server，准备注入代理参数", "path", real)
	}

	tmp := exe + ".cursor-inner-new"
	if err := os.WriteFile(tmp, lsWrapShim, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.WriteFile(mark, []byte(wantMark), 0o644); err != nil {
		return err
	}
	slog.Info("已包装 Antigravity language_server，强制 --proxy_server_url", "proxy", proxyURL)
	return nil
}

func restoreLanguageServer(binDir string) error {
	exe := filepath.Join(binDir, lsName)
	real := filepath.Join(binDir, lsRealName)
	mark := filepath.Join(binDir, lsMarkName)
	if _, err := os.Stat(real); err != nil {
		_ = os.Remove(mark)
		return nil
	}
	_ = os.Remove(exe)
	if err := os.Rename(real, exe); err != nil {
		return err
	}
	_ = os.Remove(mark)
	slog.Info("已还原 Antigravity language_server")
	return nil
}

func sameFileContent(path string, want []byte) bool {
	b, err := os.ReadFile(path)
	if err != nil || len(b) != len(want) {
		return false
	}
	for i := range b {
		if b[i] != want[i] {
			return false
		}
	}
	return true
}
