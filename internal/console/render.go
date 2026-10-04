package console

import (
	"strconv"
	"strings"
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	cream  = "\x1b[38;2;244;241;234m"
	signal = "\x1b[38;2;255;90;43m"
	muted  = "\x1b[38;2;161;157;148m"
	faint  = "\x1b[38;2;104;101;95m"
	good   = "\x1b[38;2;139;199;154m"
	bad    = "\x1b[38;2;255;122;92m"
	under  = "\x1b[4m"
	clear  = "\x1b[2K"
)

type segment struct {
	text  string
	style string
	link  string
}

func renderHeader(url, version string, st Status, cols int) string {
	top := []segment{
		{text: " ▌", style: cream},
		{text: "▐ ", style: signal},
		{text: "cursor-inner", style: bold + cream},
		{text: " " + version, style: faint},
		{text: "   "},
		{text: url, style: under + cream, link: url},
		{text: "   按回车打开配置页", style: faint},
	}
	takeover := segment{text: "未接管", style: muted}
	switch {
	case st.Skipped:
		takeover = segment{text: "已跳过（调试）", style: muted}
	case st.Takeover:
		takeover = segment{text: "● 接管中", style: good}
	}
	proxy := segment{text: "直连", style: muted}
	if st.Proxy != "" {
		proxy = segment{text: st.Proxy, style: cream}
	}
	second := []segment{
		{text: "    接管 ", style: faint}, takeover,
		{text: "    出站 ", style: faint}, proxy,
		{text: "    自定义模型 ", style: faint}, {text: strconv.Itoa(st.Models), style: cream},
	}
	rule := faint + strings.Repeat("─", cols) + reset
	return clear + line(top, cols) + "\r\n" + clear + line(second, cols) + "\r\n" + clear + rule
}

func line(parts []segment, cols int) string {
	var b strings.Builder
	left := cols - 1
	for _, part := range parts {
		if left <= 0 {
			break
		}
		text := clip(part.text, left)
		left -= width(text)
		if part.link != "" {
			b.WriteString("\x1b]8;;" + part.link + "\x1b\\")
		}
		b.WriteString(part.style + text + reset)
		if part.link != "" {
			b.WriteString("\x1b]8;;\x1b\\")
		}
	}
	return b.String()
}

func styleLine(text string) string {
	switch {
	case strings.Contains(text, "失败") || strings.Contains(text, "出错") || strings.Contains(text, "无效"):
		return bad + text + reset
	case strings.Contains(text, "-> 本地") || strings.Contains(text, "由本地模型"):
		return signal + text + reset
	default:
		return cream + text + reset
	}
}

func width(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func clip(s string, max int) string {
	n := 0
	for i, r := range s {
		w := runeWidth(r)
		if n+w > max {
			return s[:i]
		}
		n += w
	}
	return s
}

func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x2e80 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe4f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6:
		return 2
	default:
		return 1
	}
}
