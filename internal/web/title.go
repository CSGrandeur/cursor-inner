package web

import "strings"

// titleMark 插在程序名和版本号之间。正式版为空。
var titleMark string

// SetTitleMark 给窗口和配置页标题加上构建标记。正式版不调用。
func SetTitleMark(mark string) {
	titleMark = strings.TrimSpace(mark)
}

// Title 是窗口和配置页的标题：程序名、可选的构建标记、构建时写入的版本号。
func Title(version string) string {
	version = strings.TrimSpace(version)
	name := "cursor-inner"
	if titleMark != "" {
		name += " " + titleMark
	}
	if version == "" {
		return name
	}
	return name + " " + version
}
