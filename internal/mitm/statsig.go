package mitm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// Cursor 3.26 通过 AnalyticsService/BootstrapStatsig 下发 statsig 初始化结果：
// BootstrapStatsigResponse{1 config string（statsig initialize JSON）, 2 generated_at_ms}。
// config.feature_gates 以 hash_used（实测 djb2）后的门名为键。工作台每个回合用
// checkFeatureGate("agent_host_local_loop") 决定走本地循环（RunInference，cursor-inner 不支持）
// 还是后端循环（RunSSE，已支持），并把收到的 config 原样存进 workbench.experiments.statsigBootstrap，
// 下次启动先用缓存。这里只把列出的门改成 false，其余一字不动；改过的 config 也就是会被缓存的那份。
const bootstrapStatsigPath = "/aiserver.v1.AnalyticsService/BootstrapStatsig"

var forcedOffGates = []string{"agent_host_local_loop"}

func statsigDJB2(s string) string {
	var h int32
	for _, r := range s {
		h = (h << 5) - h + int32(r)
	}
	return strconv.FormatUint(uint64(uint32(h)), 10)
}

func statsigGateKey(name, hashUsed string) string {
	switch strings.ToLower(hashUsed) {
	case "none":
		return name
	case "sha256":
		sum := sha256.Sum256([]byte(name))
		return base64.StdEncoding.EncodeToString(sum[:])
	default: // djb2 是 statsig 客户端 SDK 的默认
		return statsigDJB2(name)
	}
}

// forcedGateValue 是改写后门的取值：默认 false；CURSOR_INNER_FORCE_LOCAL_LOOP=1（仅供测试）时为 true，
// 让未被灰度选中的账号也走本地循环，验证原生 RunInference 回答。
func forcedGateValue() string {
	if os.Getenv("CURSOR_INNER_FORCE_LOCAL_LOOP") == "1" {
		return "true"
	}
	return "false"
}

// forceGatesOff 把 config 里列出的门的 value 改成 forcedGateValue()（默认 false）。返回改动过的门名。
func forceGatesOff(config string, gates []string) (string, []string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(config), &top); err != nil {
		return config, nil, err
	}
	rawGates, ok := top["feature_gates"]
	if !ok {
		return config, nil, nil
	}
	var hashUsed string
	if h, ok := top["hash_used"]; ok {
		_ = json.Unmarshal(h, &hashUsed)
	}
	var fg map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rawGates, &fg); err != nil {
		return config, nil, err
	}
	want := forcedGateValue()
	var changed []string
	for _, name := range gates {
		keys := []string{statsigGateKey(name, hashUsed)}
		if keys[0] != name {
			keys = append(keys, name)
		}
		for _, k := range keys {
			g, ok := fg[k]
			if !ok || string(bytes.TrimSpace(g["value"])) == want {
				continue
			}
			g["value"] = json.RawMessage(want)
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return config, nil, nil
	}
	nb, err := json.Marshal(fg)
	if err != nil {
		return config, nil, err
	}
	top["feature_gates"] = nb
	out, err := json.Marshal(top)
	if err != nil {
		return config, nil, err
	}
	return string(out), changed, nil
}

// rewriteBootstrapBody 改 BootstrapStatsigResponse（proto 或 Connect JSON，未压缩）。
func rewriteBootstrapBody(body []byte, contentType string) ([]byte, []string, error) {
	if strings.Contains(strings.ToLower(contentType), "json") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			return body, nil, err
		}
		var cfg string
		if err := json.Unmarshal(m["config"], &cfg); err != nil {
			return body, nil, err
		}
		next, changed, err := forceGatesOff(cfg, forcedOffGates)
		if err != nil || len(changed) == 0 {
			return body, nil, err
		}
		m["config"], _ = json.Marshal(next)
		out, err := json.Marshal(m)
		return out, changed, err
	}
	var out []byte
	var changed []string
	rest := body
	for len(rest) > 0 {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return body, nil, protowire.ParseError(n)
		}
		m := protowire.ConsumeFieldValue(num, typ, rest[n:])
		if m < 0 {
			return body, nil, protowire.ParseError(m)
		}
		field := rest[:n+m]
		if num == 1 && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(rest[n:])
			next, c, err := forceGatesOff(string(v), forcedOffGates)
			if err != nil {
				return body, nil, err
			}
			if len(c) > 0 {
				changed = append(changed, c...)
				field = protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), next)
			}
		}
		out = append(out, field...)
		rest = rest[n+m:]
	}
	if len(changed) == 0 {
		return body, nil, nil
	}
	return out, changed, nil
}

func (s *Server) bootstrapStatsig(w http.ResponseWriter, r *http.Request) {
	// 只接受 gzip 或不压缩的回包，才改得了。
	r.Header.Set("Accept-Encoding", "gzip")
	reqBody, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	resp, err := s.do(r.Context(), r, reqBody, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	noteUnauthorized(r, resp.StatusCode)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out, enc := raw, resp.Header.Get("Content-Encoding")
	// CURSOR_INNER_KEEP_LOCAL_LOOP=1 仅供测试：保留官方下发的门，让 Cursor 走本地循环，验证原生回答。
	if resp.StatusCode == http.StatusOK && (os.Getenv("CURSOR_INNER_KEEP_LOCAL_LOOP") != "1" || forcedGateValue() == "true") {
		if next, changed, err := rewriteStatsig(raw, enc, resp.Header.Get("Content-Type")); err != nil {
			slog.Warn("statsig 下发读不懂，原样转交", "err", err)
		} else if len(changed) > 0 {
			out, enc = next, ""
			slog.Info("已在 statsig 下发里改写门", "gates", strings.Join(changed, ","), "value", forcedGateValue())
		}
	}
	copyHeaders(w.Header(), resp.Header)
	if enc == "" {
		w.Header().Del("Content-Encoding")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

func rewriteStatsig(raw []byte, enc, contentType string) ([]byte, []string, error) {
	plain := raw
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "", "identity":
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, nil, err
		}
		plain, err = io.ReadAll(zr)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("不支持的回包压缩 %s", enc)
	}
	return rewriteBootstrapBody(plain, contentType)
}
