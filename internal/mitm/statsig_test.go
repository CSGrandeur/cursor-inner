package mitm

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"cursor-inner/internal/catalog"
	"cursor-inner/internal/config"
)

// 实测（17F 缓存 workbench.experiments.statsigBootstrap，2026-10-21）：hash_used=djb2，
// agent_host_local_loop → "1967931573"，value=true（25% 灰度）。
func TestStatsigDJB2MatchesObservedKey(t *testing.T) {
	if got := statsigGateKey("agent_host_local_loop", "djb2"); got != "1967931573" {
		t.Fatalf("djb2=%s", got)
	}
}

const sampleStatsig = `{"feature_gates":{"1967931573":{"name":"1967931573","rule_id":"r:25.00:3","secondary_exposures":[],"id_type":"userID","value":true},"262893686":{"name":"262893686","rule_id":"x","value":true}},"dynamic_configs":{},"hash_used":"djb2","time":1}`

func gates(t *testing.T, cfg string) map[string]map[string]any {
	var m struct {
		FG map[string]map[string]any `json:"feature_gates"`
	}
	if err := json.Unmarshal([]byte(cfg), &m); err != nil {
		t.Fatal(err)
	}
	return m.FG
}

func bootstrapProto(cfg string) []byte {
	b := protowire.AppendTag(nil, 1, protowire.BytesType)
	b = protowire.AppendString(b, cfg)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	return protowire.AppendVarint(b, 1761000000000)
}

func configOf(t *testing.T, body []byte) (string, uint64) {
	var cfg string
	var at uint64
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		body = body[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(body)
			cfg, body = v, body[m:]
		case num == 2:
			v, m := protowire.ConsumeVarint(body)
			at, body = v, body[m:]
		default:
			t.Fatalf("unexpected field %d", num)
		}
	}
	return cfg, at
}

func TestBootstrapStatsigTurnsOffLocalLoopOnly(t *testing.T) {
	for _, gz := range []bool{false, true} {
		s := New(func() config.Proxy { return config.Proxy{} },
			func() []catalog.Entry { return nil },
			func(string) (config.Model, bool) { return config.Model{}, false }, nil)
		var askedEnc string
		s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			askedEnc = r.Header.Get("Accept-Encoding")
			body := bootstrapProto(sampleStatsig)
			h := http.Header{"Content-Type": {"application/proto"}}
			if gz {
				var b bytes.Buffer
				zw := gzip.NewWriter(&b)
				zw.Write(body)
				zw.Close()
				body = b.Bytes()
				h.Set("Content-Encoding", "gzip")
			}
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(body))}, nil
		})}
		req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+bootstrapStatsigPath, strings.NewReader(""))
		req.Header.Set("Accept-Encoding", "br, gzip")
		rec := httptest.NewRecorder()
		s.route(rec, req)
		if askedEnc != "gzip" {
			t.Fatalf("Accept-Encoding upstream=%q", askedEnc)
		}
		if rec.Header().Get("Content-Encoding") != "" {
			t.Fatal("rewritten body must be sent uncompressed")
		}
		cfg, at := configOf(t, rec.Body.Bytes())
		if at != 1761000000000 {
			t.Fatal("generated_at_ms lost")
		}
		fg := gates(t, cfg)
		if fg["1967931573"]["value"] != false {
			t.Fatalf("gz=%v local loop gate not off: %v", gz, fg["1967931573"])
		}
		if fg["262893686"]["value"] != true || fg["1967931573"]["rule_id"] != "r:25.00:3" {
			t.Fatal("other gates/fields must be untouched")
		}
	}
}

func TestBootstrapStatsigJSONAndHashModes(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"config": sampleStatsig, "generatedAtMs": "1"})
	out, changed, err := rewriteBootstrapBody(body, "application/json")
	if err != nil || len(changed) != 1 {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var m map[string]string
	json.Unmarshal(out, &m)
	if gates(t, m["config"])["1967931573"]["value"] != false || m["generatedAtMs"] != "1" {
		t.Fatal(m)
	}
	for _, mode := range []string{"none", "sha256"} {
		k := statsigGateKey("agent_host_local_loop", mode)
		cfg := `{"hash_used":"` + mode + `","feature_gates":{"` + k + `":{"value":true}}}`
		next, c, err := forceGatesOff(cfg, forcedOffGates)
		if err != nil || len(c) != 1 || gates(t, next)[k]["value"] != false {
			t.Fatalf("%s: %v %v %s", mode, c, err, next)
		}
	}
	// 已是 false / 没有这个门：原样，不重写
	same := `{"hash_used":"djb2","feature_gates":{"1":{"value":true}}}`
	if next, c, _ := forceGatesOff(same, forcedOffGates); next != same || c != nil {
		t.Fatal("must leave unrelated config byte-identical")
	}
}

// (test only) keep-env case: see TestBootstrapStatsigKeepEnvLeavesGate.
func TestBootstrapStatsigKeepEnvLeavesGate(t *testing.T) {
	t.Setenv("CURSOR_INNER_KEEP_LOCAL_LOOP", "1")
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return nil },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/proto"}}, Body: io.NopCloser(bytes.NewReader(bootstrapProto(sampleStatsig)))}, nil
	})}
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+bootstrapStatsigPath, strings.NewReader("")))
	cfg, _ := configOf(t, rec.Body.Bytes())
	if gates(t, cfg)["1967931573"]["value"] != true {
		t.Fatal("keep env must leave the gate as served")
	}
}

// 仅供测试的强制开门：未被灰度选中的账号（下发 false）也会被改成 true。
func TestBootstrapStatsigForceEnvTurnsGateOn(t *testing.T) {
	t.Setenv("CURSOR_INNER_FORCE_LOCAL_LOOP", "1")
	t.Setenv("CURSOR_INNER_KEEP_LOCAL_LOOP", "1") // 强制开门优先于保留
	served := strings.Replace(sampleStatsig, `"id_type":"userID","value":true}`, `"id_type":"userID","value":false}`, 1)
	s := New(func() config.Proxy { return config.Proxy{} },
		func() []catalog.Entry { return nil },
		func(string) (config.Model, bool) { return config.Model{}, false }, nil)
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/proto"}}, Body: io.NopCloser(bytes.NewReader(bootstrapProto(served)))}, nil
	})}
	rec := httptest.NewRecorder()
	s.route(rec, httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+bootstrapStatsigPath, strings.NewReader("")))
	cfg, _ := configOf(t, rec.Body.Bytes())
	fg := gates(t, cfg)
	if fg["1967931573"]["value"] != true {
		t.Fatalf("force env must turn the gate on: %v", fg["1967931573"])
	}
	if fg["262893686"]["value"] != true {
		t.Fatal("other gates untouched")
	}
}
