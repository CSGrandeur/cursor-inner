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
	cleared, err := Clear(out, nil)
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

func TestClearRestoresUserValuesFromSnapshot(t *testing.T) {
	in := []byte(`{
  "editor.fontSize": 15,
  "http.proxy": "http://proxy.corp:8080",
  "http.noProxy": ["corp.local", "10.0.0.0/8"],
  "cursor.general.disableHttp2": false
}`)
	original, err := Snapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(in, "http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := Clear(applied, original)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(cleared, &doc); err != nil {
		t.Fatal(err, string(cleared))
	}
	if doc["http.proxy"] != "http://proxy.corp:8080" || doc["cursor.general.disableHttp2"] != false {
		t.Fatalf("%v", doc)
	}
	if np, _ := doc["http.noProxy"].([]any); len(np) != 2 || np[0] != "corp.local" {
		t.Fatalf("noProxy %v", doc["http.noProxy"])
	}
	for _, key := range []string{"http.proxySupport", "http.proxyKerberosServicePrincipal", "http.experimental.systemCertificatesV2"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("%s should be removed, doc=%v", key, doc)
		}
	}
}

func TestSnapshotIgnoresLeftoverLocalTakeover(t *testing.T) {
	in := []byte(`{"http.proxy":"http://127.0.0.1:61022","http.proxySupport":"override","http.proxyKerberosServicePrincipal":"http://127.0.0.1:61022","http.noProxy":["corp.local"]}`)
	original, err := Snapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"http.proxy", "http.proxySupport", "http.proxyKerberosServicePrincipal"} {
		if _, ok := original[key]; ok {
			t.Fatalf("leftover %s captured as user value", key)
		}
	}
	if string(original["http.noProxy"]) != `["corp.local"]` {
		t.Fatalf("%s", original["http.noProxy"])
	}
}
