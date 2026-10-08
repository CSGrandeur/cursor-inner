package tools

import (
	"encoding/json"
	"strings"
)

// RepairArguments 去掉代码块围栏、尾逗号，并补上未闭合的括号。
// 修完仍不是合法 JSON 时返回原文和 false。
func RepairArguments(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	trimmed = stripFence(trimmed)
	if jsonValid(trimmed) {
		return trimmed, trimmed != raw
	}
	fixed := closeBrackets(dropTrailingCommas(trimmed))
	fixed = dropControls(fixed)
	if jsonValid(fixed) {
		return fixed, true
	}
	fixed = dropTrailingCommas(fixed)
	if jsonValid(fixed) {
		return fixed, true
	}
	return raw, false
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func dropTrailingCommas(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == ',' && nextBracket(s[i+1:]) {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func nextBracket(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\n', '\r', '\t':
			continue
		case '}', ']':
			return true
		default:
			return false
		}
	}
	return false
}

func closeBrackets(s string) string {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
			}
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		s += string(stack[i])
	}
	return s
}

func dropControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func jsonValid(s string) bool {
	return strings.TrimSpace(s) != "" && json.Valid([]byte(s))
}
