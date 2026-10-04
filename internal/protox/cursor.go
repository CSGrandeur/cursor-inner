package protox

import (
	"encoding/json"
	"strings"
)

func TextDelta(text string) []byte {
	delta := AppendString(nil, 1, text)
	update := AppendBytes(nil, 1, delta)
	msg := AppendBytes(nil, 1, update)
	return Frame(0, msg)
}

func TurnEnded() []byte {
	update := AppendBytes(nil, 14, nil)
	msg := AppendBytes(nil, 1, update)
	return Frame(0, msg)
}

func EndStream() []byte {
	return Frame(0x02, []byte("{}"))
}

func EndError(message string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]string{"code": "unavailable", "message": message},
	})
	return Frame(0x02, payload)
}

func ModelID(client []byte) string {
	run := Child(client, 1)
	if run == nil {
		return ""
	}
	if requested := Child(run, 9); requested != nil {
		if id := StringField(requested, 1); id != "" {
			return CanonicalModelID(id)
		}
	}
	if details := Child(run, 3); details != nil {
		return CanonicalModelID(StringField(details, 1))
	}
	return ""
}

func CanonicalModelID(id string) string {
	if i := strings.IndexByte(id, '['); i > 0 {
		return id[:i]
	}
	return id
}

type Chat struct {
	Role    string
	Content string
}

func ChatMessages(client []byte) []Chat {
	run := Child(client, 1)
	action := Child(run, 2)
	userAction := Child(action, 1)
	var out []Chat
	if history := Child(userAction, 7); history != nil {
		for _, message := range Children(history, 1) {
			if user := Child(message, 1); user != nil {
				for _, content := range Children(user, 1) {
					if text := StringField(Child(content, 1), 1); text != "" {
						out = append(out, Chat{Role: "user", Content: text})
					}
				}
			}
			if assistant := Child(message, 2); assistant != nil {
				for _, content := range Children(assistant, 1) {
					if text := StringField(Child(content, 1), 1); text != "" {
						out = append(out, Chat{Role: "assistant", Content: text})
					}
				}
			}
		}
	}
	if user := Child(userAction, 1); user != nil {
		if text := StringField(user, 1); text != "" {
			if len(out) == 0 || out[len(out)-1].Role != "user" || out[len(out)-1].Content != text {
				out = append(out, Chat{Role: "user", Content: text})
			}
		}
	}
	return out
}

func DecodeBidi(payload []byte) (requestID string, client []byte, err error) {
	raw, _ := Unary(payload)
	hexData := StringField(raw, 1)
	if hexData == "" {
		return "", nil, errNoData
	}
	client, err = decodeHex(hexData)
	if err != nil {
		return "", nil, err
	}
	id := StringField(Child(raw, 2), 1)
	if id == "" {
		return "", nil, errNoID
	}
	return id, client, nil
}

func DecodeRunID(payload []byte) (string, error) {
	raw, _ := Unary(payload)
	id := StringField(raw, 1)
	if id == "" {
		return "", errNoID
	}
	return id, nil
}

var (
	errNoData = errString("BidiAppend 没有 data")
	errNoID   = errString("没有 request_id")
)

type errString string

func (e errString) Error() string { return string(e) }

func decodeHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errString("data 不是偶数位十六进制")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok := fromHex(s[i*2])
		lo, ok2 := fromHex(s[i*2+1])
		if !ok || !ok2 {
			return nil, errString("data 不是十六进制")
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func fromHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func EncodeHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
