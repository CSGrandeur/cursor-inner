//go:build darwin

package antiaccount

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strings"

	"cursor-inner/internal/i18n"
)

// macOS Keychain：服务 gemini、账号 antigravity。go-keyring 写的是
// "go-keyring-base64:" 前缀 + base64(JSON)，读侧按同一约定解。
const (
	credTarget = "gemini"
	credUser   = "antigravity"
)

func keyringWrite(a Account) error {
	value := "go-keyring-base64:" + base64.StdEncoding.EncodeToString(keyringPayload(a))
	// add-generic-password -U 覆盖已有条目；-A 允许本机应用免弹窗读取。
	out, err := exec.Command("security", "add-generic-password", "-U", "-s", credTarget, "-a", credUser, "-w", value, "-A").CombinedOutput()
	if err != nil {
		return i18n.Wrap("写入 macOS 钥匙串失败："+strings.TrimSpace(string(out))+" ", "Writing the macOS Keychain failed: "+strings.TrimSpace(string(out))+" ", err)
	}
	return nil
}

func keyringRead() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", credTarget, "-a", credUser, "-w").Output()
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(out))
	if raw, ok := strings.CutPrefix(value, "go-keyring-base64:"); ok {
		return base64.StdEncoding.DecodeString(raw)
	}
	return []byte(value), nil
}

func keyringReadInProcess() ([]byte, error) {
	return nil, os.ErrNotExist
}
