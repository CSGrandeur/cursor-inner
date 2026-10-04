package cursorsettings

import (
	"bytes"
	"encoding/json"
	"strings"

	"cursor-inner/internal/i18n"

	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
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
	return format(text), nil
}

// Snapshot 记下接管前用户自己设置的受管键原值。上次接管残留的本机代理不算用户设置。
func Snapshot(doc []byte) (map[string]json.RawMessage, error) {
	text, err := normalize(doc)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	leftover := isLoopbackProxy(gjson.Get(text, path("http.proxy")).String())
	for _, key := range managed {
		value := gjson.Get(text, path(key))
		if !value.Exists() {
			continue
		}
		if leftover && (key == "http.proxy" || key == "http.proxySupport" || key == "http.proxyKerberosServicePrincipal") {
			continue
		}
		out[key] = json.RawMessage(value.Raw)
	}
	return out, nil
}

func Clear(doc []byte, original map[string]json.RawMessage) ([]byte, error) {
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
	for _, key := range managed {
		raw, ok := original[key]
		if !ok {
			continue
		}
		text, err = sjson.SetRaw(text, path(key), string(raw))
		if err != nil {
			return nil, err
		}
	}
	return format(text), nil
}

func format(text string) []byte {
	return pretty.PrettyOptions([]byte(text), &pretty.Options{Indent: "    ", SortKeys: false})
}

func isLoopbackProxy(proxy string) bool {
	return strings.HasPrefix(proxy, "http://127.0.0.1:") || strings.HasPrefix(proxy, "http://localhost:")
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
		return "", i18n.E("Cursor settings.json 无法解析", "Cannot parse Cursor's settings.json")
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
