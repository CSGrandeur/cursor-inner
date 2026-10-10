package web

import "strings"

// Title 是窗口和配置页的标题：程序名加构建时写入的版本号（开发者版带构建后缀）。
func Title(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "cursor-inner"
	}
	return "cursor-inner " + version
}
