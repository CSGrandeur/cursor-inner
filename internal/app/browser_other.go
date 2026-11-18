//go:build !windows && !darwin

package app

import (
	"os/exec"

	"cursor-inner/internal/i18n"
)

func openBrowserURL(url string) error {
	for _, opener := range [][]string{{"xdg-open"}, {"wslview"}, {"rundll32.exe", "url.dll,FileProtocolHandler"}} {
		if path, err := exec.LookPath(opener[0]); err == nil {
			cmd := exec.Command(path, append(opener[1:], url)...)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	return i18n.E("找不到浏览器启动命令，请复制链接手动打开", "No browser launcher found; copy the link and open it manually")
}
