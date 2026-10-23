package agent

import "unicode/utf8"

// headUTF8 返回 s 的前至多 n 个字节，不把多字节字符（中文、emoji）从中间切开。
func headUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailUTF8 返回 s 的后至多 n 个字节，同样不切开多字节字符。
func tailUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
