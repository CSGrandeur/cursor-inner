//go:build darwin

package cursoraccount

import (
	"errors"
	"os/exec"
	"strings"
)

func decryptSafeStorageBlob(_ string, blob []byte) (string, error) {
	if len(blob) < 3 || string(blob[:3]) != "v10" {
		return "", errors.New("unexpected macOS safeStorage header")
	}
	out, err := exec.Command("security", "find-generic-password", "-w", "-s", "Cursor Safe Storage").Output()
	if err != nil {
		return "", err
	}
	return decryptAES128CBC(strings.TrimSpace(string(out)), 1003, blob)
}

// macOS 上保留明文写入，Cursor 启动后会自动接管重加密，避免切号弹窗请求钥匙串权限。
func encodePlatformToken(_, plain string) string { return plain }
