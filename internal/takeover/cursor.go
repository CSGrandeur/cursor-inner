package takeover

import (
	"os"
	"path/filepath"

	"cursor-inner/internal/i18n"
)

func settingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", i18n.Wrap("找不到 Cursor 配置目录：", "Cannot find the Cursor config directory: ", err)
	}
	return filepath.Join(base, "Cursor", "User", "settings.json"), nil
}
