package cursorsettings

import (
	"bytes"
	"errors"
	"strings"

	"github.com/tidwall/sjson"
)

var managed = []string{
	"http.proxy",
	"http.proxyKerberosServicePrincipal",
	"http.proxySupport",
	"cursor.general.disableHttp2",
	"http.experimental.systemCertificatesV2",
	"http.noProxy",
}

func Apply(doc []byte, proxyURL string) ([]byte, error) {
	text, err := normalize(doc)
	if err != nil {
		return nil, err
	}
	text, err = sjson.Delete(text, path("http.noProxy"))
	if err != nil {
		return nil, err
	}
	sets := []struct {
		key string
		val any
	}{
		{"http.proxy", proxyURL},
		{"http.proxyKerberosServicePrincipal", proxyURL},
		{"http.proxySupport", "override"},
		{"cursor.general.disableHttp2", true},
		{"http.experimental.systemCertificatesV2", true},
	}
	for _, item := range sets {
		text, err = sjson.Set(text, path(item.key), item.val)
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text), nil
}

func Clear(doc []byte) ([]byte, error) {
	text, err := normalize(doc)
	if err != nil {
		return nil, err
	}
	for _, key := range managed {
		text, err = sjson.Delete(text, path(key))
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text), nil
}

func path(key string) string {
	return strings.ReplaceAll(key, ".", "\\.")
}

func normalize(doc []byte) (string, error) {
	doc = bytes.TrimPrefix(doc, []byte{0xEF, 0xBB, 0xBF})
	text := strings.TrimSpace(string(doc))
	if text == "" {
		return "{}", nil
	}
	if !jsonValid(text) {
		text = stripJSONC(text)
	}
	if !jsonValid(text) {
		return "", errors.New("Cursor settings.json 无法解析")
	}
	return text, nil
}

func jsonValid(s string) bool {
	dec := jsonDecoder(s)
	var v any
	if err := dec.Decode(&v); err != nil {
		return false
	}
	return !dec.More()
}
