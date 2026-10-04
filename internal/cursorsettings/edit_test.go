package cursorsettings

import (
	"encoding/json"
	"testing"
)

func TestApplyKeepsOtherKeysAndClearRemovesOnlyManaged(t *testing.T) {
	in := []byte("{\n  \"editor.fontSize\": 15,\n  // comment\n  \"http.noProxy\": \"example.com\"\n}\n")
	out, err := Apply(in, "http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err, string(out))
	}
	if doc["editor.fontSize"].(float64) != 15 {
		t.Fatal(doc["editor.fontSize"])
	}
	if doc["http.proxy"] != "http://127.0.0.1:9" || doc["http.proxySupport"] != "override" {
		t.Fatal(doc["http.proxy"], doc["http.proxySupport"])
	}
	if _, ok := doc["http.noProxy"]; ok {
		t.Fatal("noProxy remains")
	}
	if doc["cursor.general.disableHttp2"] != true {
		t.Fatal(doc["cursor.general.disableHttp2"])
	}
	cleared, err := Clear(out)
	if err != nil {
		t.Fatal(err)
	}
	doc = map[string]any{}
	if err := json.Unmarshal(cleared, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["http.proxy"]; ok {
		t.Fatal("proxy remains")
	}
	if doc["editor.fontSize"].(float64) != 15 {
		t.Fatal("font")
	}
}
