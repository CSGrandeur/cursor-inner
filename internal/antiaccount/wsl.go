package antiaccount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func parseWSLStateDistro(raw []byte) string {
	var doc struct {
		Distro string `json:"distro"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	d := strings.TrimSpace(doc.Distro)
	if d == "" || strings.ContainsAny(d, "/\\:\x00") || strings.Contains(d, "..") {
		return ""
	}
	return d
}

// resolveWSLHomeFromRoot 通过纯文件读写定位 WSL 发行版里的用户主目录（不拉起 wsl.exe）。
func resolveWSLHomeFromRoot(distroRoot string) string {
	defaultUser := parseWSLDefaultUser(filepath.Join(distroRoot, "etc", "wsl.conf"))
	if home := matchPasswdHome(distroRoot, filepath.Join(distroRoot, "etc", "passwd"), defaultUser); home != "" {
		return home
	}
	homeBase := filepath.Join(distroRoot, "home")
	entries, err := os.ReadDir(homeBase)
	if err != nil {
		return ""
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cand := filepath.Join(homeBase, e.Name())
		dirs = append(dirs, cand)
		for _, marker := range []string{".gemini", ".antigravity-server"} {
			if info, err := os.Stat(filepath.Join(cand, marker)); err == nil && info.IsDir() {
				return cand
			}
		}
	}
	if len(dirs) == 1 {
		return dirs[0]
	}
	return ""
}

func parseWSLDefaultUser(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if idx := strings.IndexAny(line, "#;"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "user" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "default") {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func matchPasswdHome(distroRoot, passwdPath, defaultUser string) string {
	raw, err := os.ReadFile(passwdPath)
	if err != nil {
		return ""
	}
	fallback := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 6 {
			continue
		}
		user := fields[0]
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		homePosix := fields[5]
		if !strings.HasPrefix(homePosix, "/") || homePosix == "/" || strings.Contains(homePosix, "..") {
			continue
		}
		hostHome := filepath.Join(distroRoot, filepath.FromSlash(strings.TrimPrefix(homePosix, "/")))
		info, err := os.Stat(hostHome)
		if err != nil || !info.IsDir() {
			continue
		}
		if defaultUser != "" && user == defaultUser {
			return hostHome
		}
		if fallback == "" && uid >= 1000 && uid < 65534 {
			fallback = hostHome
		}
	}
	return fallback
}
