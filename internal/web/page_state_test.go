package web

import (
	"os/exec"
	"strings"
	"testing"
)

// 状态判定的页面逻辑（“还没定论的状态不能显示成错误”）单独抽出来用 node 跑；没有 node 就跳过。
func TestPageStateLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	src := pageSource(t)
	start := strings.Index(src, "// <state-logic>")
	end := strings.Index(src, "// </state-logic>")
	if start < 0 || end < start {
		t.Fatal("state-logic block missing from page.html")
	}
	script := `
const lang = "zh";
function tr(v) { if (v == null) return ""; if (typeof v === "string") return v; return v[lang] || v.zh || ""; }
` + src[start:end] + `
const fails = [];
function eq(name, got, want) { if (JSON.stringify(got) !== JSON.stringify(want)) fails.push(name + ": got " + JSON.stringify(got) + ", want " + JSON.stringify(want)); }
const none = {zh: "", en: ""};
const base = {takeover: true, takeover_active: false, last_error: none, catalog_warning: none, proxy: {enabled: true, effective: true}, tunnel: {status: 0}};
const S = (o) => Object.assign({}, base, o);

// takeStatus：后台给的三态原样用；缺字段时按旧字段推，而且没生效不直接当错误。
eq("pending from server", takeStatus(S({takeover_state: "pending", takeover_reason: "handover"})), {kind: "pending", reason: "handover"});
eq("no state yet", takeStatus(null).kind, "pending");
eq("legacy active", takeStatus(S({takeover_active: true})).kind, "active");
eq("legacy configured but inactive is not an error", takeStatus(S({})).kind, "pending");
eq("legacy error", takeStatus(S({last_error: {zh: "x", en: "x"}})).kind, "error");
eq("legacy off", takeStatus(S({takeover: false})).kind, "off");

// networkLevel：红色只给明确的失败。
const pendingTake = S({takeover_state: "pending", takeover_reason: "starting"});
eq("pending takeover is neutral", networkLevel(pendingTake, "ok"), "pending");
eq("pending takeover with the proxy not yet effective is neutral", networkLevel(S({takeover_state: "pending", proxy: {enabled: true, effective: false}}), "ok"), "pending");
eq("error takeover is red", networkLevel(S({takeover_state: "error", takeover_reason: "not_active"}), "ok"), "bad");
eq("last_error with state error", networkLevel(S({takeover_state: "error", last_error: {zh: "x", en: "x"}}), "ok"), "bad");
eq("active and fine", networkLevel(S({takeover_state: "active", takeover_active: true}), "ok"), "");
eq("settled but the proxy is not in effect", networkLevel(S({takeover_state: "active", proxy: {enabled: true, effective: false}}), "ok"), "bad");
eq("proxy off is not a problem", networkLevel(S({takeover_state: "active", proxy: {enabled: false, effective: false}}), "ok"), "");
eq("catalog warning", networkLevel(S({takeover_state: "active", catalog_warning: {zh: "w", en: "w"}}), "ok"), "bad");
eq("tunnel failed", networkLevel(S({takeover_state: "active", tunnel: {status: TUN_FAILED}}), "ok"), "bad");
eq("tunnel starting is neutral", networkLevel(S({takeover_state: "active", tunnel: {status: TUN_STARTING}}), "ok"), "pending");
eq("tunnel on", networkLevel(S({takeover_state: "active", tunnel: {status: TUN_ON}}), "ok"), "");
eq("takeover off", networkLevel(S({takeover: false, takeover_state: "off"}), "ok"), "");
eq("link retrying is neutral", networkLevel(S({takeover_state: "active"}), "pending"), "pending");
eq("link lost is red", networkLevel(S({takeover_state: "active"}), "lost"), "bad");
eq("no state, link fine", networkLevel(null, "ok"), "");

// 备用模型按名称填写，提交 id；空格名称、旧 id、未知名称、自己和重复都要处理对。
const ms = [{id: "h1", display_name: "Qwen3-VL (vLLM)", model: "qwen3vl"}, {id: "h2", display_name: "", model: "deepseek-chat"}, {id: "h3", display_name: "Kimi K2", model: "kimi"}];
eq("fallback by display name with spaces", resolveFallback("Qwen3-VL (vLLM), deepseek-chat", ms, ""), {ids: ["h1", "h2"], unknown: []});
eq("fallback accepts legacy ids and full-width commas", resolveFallback("h3，kimi k2", ms, ""), {ids: ["h3"], unknown: []});
eq("fallback reports unknown names", resolveFallback("nope, Kimi K2", ms, ""), {ids: ["h3"], unknown: ["nope"]});
eq("fallback skips the model itself", resolveFallback("Kimi K2", ms, "h3"), {ids: [], unknown: []});
eq("fallback shows names", fallbackText(["h2", "h1", "gone"], ms), "deepseek-chat, Qwen3-VL (vLLM), gone");

// 更新说明：Markdown 标题和列表要结构化显示，不能把 ### 原样露给用户。
eq("notes blocks", notesBlocks("### Fixed\n\n- One\n  continued\n- Two \u0060x\u0060\n\nPlain text."), [{kind: "h", text: "Fixed"}, {kind: "li", text: "One continued"}, {kind: "li", text: "Two \u0060x\u0060"}, {kind: "p", text: "Plain text."}]);
eq("notes empty", notesBlocks(""), []);

// proxyStatus
eq("proxy ok", proxyStatus(S({}), {kind: "active"}), "ok");
eq("proxy direct", proxyStatus(S({proxy: {enabled: false}}), {kind: "active"}), "direct");
eq("proxy undetermined", proxyStatus(S({proxy: {enabled: true, effective: false}}), {kind: "pending"}), "pending");
eq("proxy failed", proxyStatus(S({proxy: {enabled: true, effective: false}}), {kind: "active"}), "bad");

// linkStatus / needsFastPoll
eq("link ok", linkStatus(0, 1e9), "ok");
eq("link retrying", linkStatus(1000, 1000 + LINK_GRACE_MS - 1), "pending");
eq("link lost", linkStatus(1000, 1000 + LINK_GRACE_MS), "lost");
eq("poll fast before the first state", needsFastPoll(null, "ok"), true);
eq("poll fast while pending", needsFastPoll(pendingTake, "ok"), true);
eq("poll fast while the link is down", needsFastPoll(S({takeover_state: "active"}), "pending"), true);
eq("poll slow once settled", needsFastPoll(S({takeover_state: "active", takeover_active: true}), "ok"), false);
eq("poll slow on a definite error", needsFastPoll(S({takeover_state: "error"}), "ok"), false);

// updateLevel：更新进行中是中性的，不是警告也不是错误。
eq("update applying", updateLevel({state: "downloading"}), "pending");
eq("update handover", updateLevel({state: "handover"}), "pending");
eq("update done", updateLevel({state: "done"}), "pending");
eq("update available", updateLevel({state: "available"}), "warn");
eq("update checking", updateLevel({state: "checking"}), "");
eq("update up to date", updateLevel({state: "up_to_date"}), "");
eq("update failed after finding a version", updateLevel({state: "failed", latest: "v1", error: "boom"}), "bad");
eq("update check failed with no version is quiet", updateLevel({state: "failed", error: "offline"}), "");
eq("update untrusted manifest", updateLevel({state: "available", latest: "v1", error: "x", error_code: "sig_invalid"}), "bad");
eq("no updater key", updateLevel({state: "idle", error_code: "no_key"}), "warn");
console.log(fails.join("\n"));
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Fatalf("page state logic:\n%s", got)
	}
}

// 页面不再一次性读状态：有轮询；更新标签只有后台明确说没有更新器（404）才隐藏；pending 不用红色。
func TestPagePollsAndNeverPaintsPendingRed(t *testing.T) {
	src := pageSource(t)
	for _, want := range []string{
		"function schedulePoll()", "async function pollState()", "needsFastPoll(",
		`err.status !== 404 && tries < 30`, // 更新器没就绪时重试，不把“更新”标签永久藏掉
		".dot.pending", ".pill.pending", ".tab-dot.pending",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("page.html missing %q", want)
		}
	}
	if strings.Contains(src, "networkProblem(") {
		t.Error("networkProblem was replaced by networkLevel (tri-state)")
	}
	for _, rule := range []string{".dot.pending", ".pill.pending", ".tab-dot.pending"} {
		i := strings.Index(src, rule)
		if i < 0 {
			continue
		}
		decl := src[i : i+strings.Index(src[i:], "}")]
		if strings.Contains(decl, "--bad") || strings.Contains(decl, "--signal") {
			t.Errorf("%s must be neutral, got %q", rule, decl)
		}
	}
}

// 等代理期间，设置页“代理”那行也要说在等代理 / 让用户打开代理软件，不能还写“生效中”。
func TestProxySettingRowShowsWaiting(t *testing.T) {
	src := pageSource(t)
	i := strings.Index(src, `const proxyMeta = document.querySelector("#proxy-meta");`)
	if i < 0 {
		t.Fatal("proxyMeta block missing")
	}
	block := src[i : i+strings.Index(src[i:], "proxyMeta.title")]
	for _, want := range []string{`"waiting"`, `"down"`, "proxyWaitingTip", "proxyDownTip"} {
		if !strings.Contains(block, want) {
			t.Errorf("proxy setting row ignores wait state: missing %q", want)
		}
	}
}
