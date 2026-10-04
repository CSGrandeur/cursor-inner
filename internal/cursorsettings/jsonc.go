package cursorsettings

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

func jsonDecoder(s string) *json.Decoder {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	return dec
}

func stripJSONC(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escape := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if inString {
			b.WriteRune(r)
			if escape {
				escape = false
			} else if r == '\\' {
				escape = true
			} else if r == '"' {
				inString = false
			}
			i += size
			continue
		}
		if r == '"' {
			inString = true
			b.WriteRune(r)
			i += size
			continue
		}
		if r == '/' && i+1 < len(s) && s[i+1] == '/' {
			i += 2
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if r == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}
