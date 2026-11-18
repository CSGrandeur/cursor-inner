//go:build windows

package antiaccount

import (
	"os"
	"path/filepath"
	"strings"
)

func activeWSLGeminiDir() (distro, geminiPath string) {
	cfgDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(cfgDir) == "" {
		cfgDir = os.Getenv("APPDATA")
	}
	if strings.TrimSpace(cfgDir) == "" {
		return "", ""
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, "Antigravity", "wsl_state.json"))
	if err != nil {
		return "", ""
	}
	distro = parseWSLStateDistro(raw)
	if distro == "" {
		return "", ""
	}
	distroRoot := `\\wsl.localhost\` + distro
	home := resolveWSLHomeFromRoot(distroRoot)
	if home == "" {
		return distro, ""
	}
	return distro, filepath.Join(home, ".gemini")
}
