package protox

import (
	"encoding/json"
)

func EndStream() []byte {
	return Frame(0x02, []byte("{}"))
}

func EndError(message string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]string{"code": "unavailable", "message": message},
	})
	return Frame(0x02, payload)
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
