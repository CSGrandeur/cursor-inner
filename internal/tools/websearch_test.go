package tools

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

func TestSearchHitsDropEngineLinks(t *testing.T) {
	if !keepSearchHit("Go", "https://go.dev/dl") {
		t.Fatal("go.dev")
	}
	if keepSearchHit("Google", "https://www.google.com/") || keepSearchHit("", "https://example.com") {
		t.Fatal("kept a navigation hit")
	}
	if got := unwrapSearchURL("https://duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdl"); got != "https://go.dev/dl" {
		t.Fatal(got)
	}
	if got := unwrapSearchURL("https://www.bing.com/ck/a?u=a1aHR0cHM6Ly9nby5kZXYv"); got != "https://go.dev/" {
		t.Fatal(got)
	}
}

func TestSavedSearchPagesYieldRealHits(t *testing.T) {
	bing, err := os.ReadFile("testdata/bing.html")
	if err != nil {
		t.Fatal(err)
	}
	ddg, err := os.ReadFile("testdata/duckduckgo.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, hits := range [][]webHit{parseBing(string(bing)), parseDuckHTML(string(ddg))} {
		if len(hits) < 5 {
			t.Fatalf("got %d hits", len(hits))
		}
		for _, hit := range hits[:5] {
			if hit.title == "" || !strings.HasPrefix(hit.url, "http") || hit.chunk == "" {
				t.Fatalf("%+v", hit)
			}
		}
	}
}

func TestFederatedParsersAndRRF(t *testing.T) {
	bing := `<li class="b_algo"><h2><a href="https://example.com/go">Go notes</a></h2><p>release notes</p></li>`
	ddg := `<div class="result "><a class="result__a" href="https://example.com/go">Go notes</a><a class="result__snippet" href="https://example.com/go">longer release notes</a></div></div>`
	baidu := `<div class="result c-container"><h3><a href="https://example.com/other">Other</a></h3><div class="c-abstract">摘要</div></div></div>`
	hits := rrf([][]webHit{parseBing(bing), parseDuckHTML(ddg), parseBaidu(baidu)})
	if len(hits) < 2 || hits[0].url != "https://example.com/go" || !strings.Contains(hits[0].chunk, "longer") {
		t.Fatalf("%+v", hits)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 特征来自 2026-10-11 的真实请求：百度（直连和走代理都一样）302 到 wappass 验证页，偶尔 503 + Location；
// DuckDuckGo HTML 版回 202 挑战页；搜狗、Startpage、Mojeek 的拦截页按地址和标题识别。
func TestBlockPagesAreRecognized(t *testing.T) {
	const wappass = "https://wappass.baidu.com/static/captcha/tuxing_v2.html?&logid=0&ak=0&backurl=https%3A%2F%2Fwww.baidu.com%2Fs%3Fwd%3DGo"
	blocked := map[string]searchPage{
		"baidu-landing":    {status: 200, final: wappass, body: readFixture(t, "baidu-block.html")},
		"baidu-503":        {status: 503, final: "https://www.baidu.com/s?wd=Go", location: wappass},
		"ddg-anomaly":      {status: 202, final: "https://html.duckduckgo.com/html/?q=Go", body: readFixture(t, "duckduckgo-block.html")},
		"sogou-antispider": {status: 200, final: "https://www.sogou.com/antispider/?m=1&antip=web_hd", body: "<title>搜狗搜索</title>"},
		"startpage":        {status: 200, final: "https://www.startpage.com/sp/captcha-block?bc=NL"},
		"mojeek":           {status: 200, final: "https://www.mojeek.com/search?q=Go", body: "<html><head><title>Captcha</title></head></html>"},
	}
	for name, page := range blocked {
		if searchBlocked(page) == "" {
			t.Errorf("%s not recognized", name)
		}
	}
	if !strings.Contains(searchBlocked(blocked["baidu-landing"]), "security verification") {
		t.Fatal(searchBlocked(blocked["baidu-landing"]))
	}
	normal := []searchPage{
		{status: 200, final: "https://www.bing.com/search?q=Go+1.25", body: readFixture(t, "bing.html")},
		{status: 200, final: "https://html.duckduckgo.com/html/?q=Go", body: readFixture(t, "duckduckgo.html")},
		{status: 200, final: "https://search.brave.com/search?q=captcha+challenge", body: readFixture(t, "brave.html")},
	}
	for _, page := range normal {
		if reason := searchBlocked(page); reason != "" {
			t.Errorf("%s: %s", page.final, reason)
		}
	}
}

func TestSavedBravePageYieldsRealHits(t *testing.T) {
	hits := parseBrave(readFixture(t, "brave.html"))
	if len(hits) < 5 || hits[0].url != "https://go.dev/doc/go1.25" || !strings.Contains(hits[0].title, "Go 1.25 Release Notes") {
		t.Fatalf("%d %+v", len(hits), hits)
	}
	for _, hit := range hits[:5] {
		if hit.title == "" || !strings.HasPrefix(hit.url, "http") || hit.chunk == "" {
			t.Fatalf("%+v", hit)
		}
	}
}

type fakeEngines struct {
	mu    sync.Mutex
	pages map[string]searchPage
	errs  map[string]error
	calls map[string]int
}

func stubSearch(t *testing.T, f *fakeEngines) *time.Time {
	t.Helper()
	oldFetch, oldNow := fetchSearch, searchNow
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	blockedMu.Lock()
	blockedEngine = map[string]engineBlock{}
	blockedMu.Unlock()
	f.calls = map[string]int{}
	fetchSearch = func(_ dialer.Func, raw string) (searchPage, error) {
		u, _ := url.Parse(raw)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls[u.Host]++
		if err := f.errs[u.Host]; err != nil {
			return searchPage{}, err
		}
		page, ok := f.pages[u.Host]
		if !ok {
			page = searchPage{status: 200, body: "<html></html>"}
		}
		if page.final == "" {
			page.final = raw
		}
		return page, nil
	}
	searchNow = func() time.Time { return now }
	t.Cleanup(func() {
		fetchSearch, searchNow = oldFetch, oldNow
		blockedMu.Lock()
		blockedEngine = map[string]engineBlock{}
		blockedMu.Unlock()
	})
	return &now
}

const wappassURL = "https://wappass.baidu.com/static/captcha/tuxing_v2.html?&logid=0"

func TestFederatedSearchSkipsVerificationPages(t *testing.T) {
	f := &fakeEngines{pages: map[string]searchPage{
		"www.bing.com":        {status: 200, body: readFixture(t, "bing.html")},
		"lite.duckduckgo.com": {status: 202, body: readFixture(t, "duckduckgo-block.html")},
		"www.baidu.com":       {status: 200, final: wappassURL, body: readFixture(t, "baidu-block.html")},
		"search.brave.com":    {status: 200, body: readFixture(t, "brave.html")},
	}}
	now := stubSearch(t, f)
	refs, notes, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || len(refs) < 5 {
		t.Fatalf("%d %v", len(refs), err)
	}
	for _, ref := range refs {
		if strings.Contains(ref.GetUrl(), "wappass") || strings.Contains(ref.GetTitle(), "安全验证") || strings.Contains(ref.GetTitle(), "DuckDuckGo") {
			t.Fatalf("verification page leaked into results: %+v", ref)
		}
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "baidu: returned a security verification") || !strings.Contains(joined, "duckduckgo: returned a bot-check") {
		t.Fatalf("notes %q", joined)
	}
	// 冷却期内不再请求被拦的引擎，结果里说明跳过原因。
	_, notes, _ = FederatedSearch(nil, "Go 1.25 release notes")
	if f.calls["www.baidu.com"] != 1 || f.calls["lite.duckduckgo.com"] != 1 || f.calls["www.bing.com"] != 2 {
		t.Fatalf("calls %v", f.calls)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "baidu: skipped, it returned a bot-check page") {
		t.Fatalf("notes %q", notes)
	}
	*now = now.Add(blockCooldown + time.Second)
	FederatedSearch(nil, "Go 1.25 release notes")
	if f.calls["www.baidu.com"] != 2 {
		t.Fatalf("after the cooldown the engine is tried again: %v", f.calls)
	}
}

// bing.html 是 2026-10-11 不带 cookie 的 Bing 对“Go 1.25 release notes”的真实结果：只按“Go”搜，
// 全是首页、教程、Gene Ontology。它不算相关结果，要触发 Brave，并且排在后面或丢掉。
func TestOffTopicResultsTriggerFallbackAndRankLast(t *testing.T) {
	f := &fakeEngines{pages: map[string]searchPage{
		"www.bing.com":        {status: 200, body: readFixture(t, "bing.html")},
		"lite.duckduckgo.com": {status: 202, body: readFixture(t, "duckduckgo-block.html")},
		"www.baidu.com":       {status: 200, final: wappassURL, body: readFixture(t, "baidu-block.html")},
		"search.brave.com":    {status: 200, body: readFixture(t, "brave.html")},
	}}
	stubSearch(t, f)
	refs, notes, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || f.calls["search.brave.com"] != 1 {
		t.Fatalf("%v %v", err, f.calls)
	}
	if refs[0].GetUrl() != "https://go.dev/doc/go1.25" {
		t.Fatalf("top hit %v", refs[0])
	}
	for _, ref := range refs {
		if strings.Contains(ref.GetUrl(), "geneontology") || ref.GetUrl() == "https://golang.google.cn/" {
			t.Fatalf("an off-topic Bing hit survived next to enough relevant ones: %v", ref)
		}
	}
	if !strings.Contains(strings.Join(notes, "\n"), "bing: its results matched few of the query words") {
		t.Fatalf("notes %q", notes)
	}
}

func TestOnTopicPrimaryResultsNeedNoFallback(t *testing.T) {
	f := &fakeEngines{pages: map[string]searchPage{
		"www.bing.com":        {status: 200, body: readFixture(t, "bing.html")},
		"lite.duckduckgo.com": {status: 200, body: readFixture(t, "duckduckgo-lite.html")},
	}}
	stubSearch(t, f)
	refs, _, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || f.calls["search.brave.com"] != 0 {
		t.Fatalf("%v %v", err, f.calls)
	}
	if len(refs) < 6 || refs[0].GetUrl() != "https://go.dev/doc/go1.25" || refs[1].GetUrl() != "https://go.dev/blog/go1.25" {
		t.Fatalf("%v", refs)
	}
}

func TestRateLimitedEngineCoolsDown(t *testing.T) {
	f := &fakeEngines{pages: map[string]searchPage{
		"www.bing.com":     {status: 200, body: readFixture(t, "bing.html")},
		"search.brave.com": {status: 429, body: "<title>Brave Search</title>Too many requests right now. Please wait a moment and try again."},
	}}
	now := stubSearch(t, f)
	_, notes, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), "brave: rate-limited (HTTP 429)") {
		t.Fatalf("%v %q", err, notes)
	}
	_, notes, _ = FederatedSearch(nil, "Go 1.25 release notes")
	if f.calls["search.brave.com"] != 1 || !strings.Contains(strings.Join(notes, "\n"), "brave: skipped, it rate-limited us") {
		t.Fatalf("%v %q", f.calls, notes)
	}
	*now = now.Add(rateLimitCooldown + time.Second)
	FederatedSearch(nil, "Go 1.25 release notes")
	if f.calls["search.brave.com"] != 2 {
		t.Fatalf("%v", f.calls)
	}
}

// 连不上的引擎（不走代理时的 DuckDuckGo、Brave）歇一会儿，免得每次搜索都等到超时。
func TestUnreachableEngineCoolsDown(t *testing.T) {
	f := &fakeEngines{
		pages: map[string]searchPage{"www.bing.com": {status: 200, body: readFixture(t, "bing.html")}},
		errs: map[string]error{
			"lite.duckduckgo.com": errors.New("context deadline exceeded"),
			"search.brave.com":    errors.New("dial tcp: connection refused"),
		},
	}
	now := stubSearch(t, f)
	FederatedSearch(nil, "Go 1.25 release notes")
	_, notes, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || f.calls["lite.duckduckgo.com"] != 1 || f.calls["search.brave.com"] != 1 {
		t.Fatalf("%v %v", err, f.calls)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "duckduckgo: skipped, it was unreachable") {
		t.Fatalf("%q", notes)
	}
	*now = now.Add(unreachableCooldown + time.Second)
	FederatedSearch(nil, "Go 1.25 release notes")
	if f.calls["lite.duckduckgo.com"] != 2 || f.calls["search.brave.com"] != 2 {
		t.Fatalf("%v", f.calls)
	}
}

// 只剩偏题结果时照样返回，但明说多半偏题，让模型换说法或直接 WebFetch。
func TestOnlyOffTopicResultsAreFlagged(t *testing.T) {
	f := &fakeEngines{pages: map[string]searchPage{"www.bing.com": {status: 200, body: readFixture(t, "bing.html")}}}
	stubSearch(t, f)
	refs, notes, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || len(refs) < 5 || !strings.Contains(strings.Join(notes, "\n"), "probably off-topic") {
		t.Fatalf("%d %v %q", len(refs), err, notes)
	}
	if refs[0].GetUrl() != "https://go.dev/dl/" {
		t.Fatalf("the best-covering hit goes first: %v", refs[0])
	}
}

func TestSavedLitePageYieldsRealHits(t *testing.T) {
	hits := parseDuck(readFixture(t, "duckduckgo-lite.html"))
	if len(hits) != 6 || hits[0].url != "https://go.dev/doc/go1.25" || hits[0].title != "Go 1.25 Release Notes - The Go Programming Language" {
		t.Fatalf("%+v", hits)
	}
	for _, hit := range hits {
		if hit.chunk == "" || strings.Contains(hit.url, "duckduckgo.com") {
			t.Fatalf("%+v", hit)
		}
	}
	if len(parseDuck(readFixture(t, "duckduckgo.html"))) < 5 {
		t.Fatal("the HTML layout still parses")
	}
}

func TestQueryTermCoverage(t *testing.T) {
	cover := func(q, title string) float64 { return termCoverage(queryTerms(q), webHit{title: title}) }
	cases := []struct {
		q, title string
		want     float64
	}{
		{"Go 1.25 release notes", "Google", 0},
		{"Go 1.25 release notes", "Go 1.25.3 Release Notes", 1},
		{"Go 1.25 release notes", "Go (game) - Wikipedia", 0.25},
		{"Node.js 22 require esm", "Node .js 22 can require ESM", 1},
		{"Linux 查看端口占用", "linux如何查看端口被谁占用（lsof -i与netstat命令）", 1},
		{"Linux 查看端口占用", "Download Linux | Linux.org", 0.5},
		{"Kubernetes 1.31 边车容器", "Banden kopen bij Tirendo.nl", 0},
		{"C++ 20 协程", "C++20 协程入门", 2.0 / 3},
		{"how to the of", "anything", 1},
	}
	for _, c := range cases {
		if got := cover(c.q, c.title); got < c.want-0.01 || got > c.want+0.01 {
			t.Errorf("%q vs %q: %.2f, want %.2f", c.q, c.title, got, c.want)
		}
	}
}

func TestFederatedSearchFallsBackToBrave(t *testing.T) {
	f := &fakeEngines{
		pages: map[string]searchPage{
			"lite.duckduckgo.com": {status: 202, body: readFixture(t, "duckduckgo-block.html")},
			"www.baidu.com":       {status: 503, location: wappassURL},
			"search.brave.com":    {status: 200, body: readFixture(t, "brave.html")},
		},
		errs: map[string]error{"www.bing.com": errors.New("dial tcp: i/o timeout")},
	}
	stubSearch(t, f)
	refs, notes, err := FederatedSearch(nil, "Go 1.25 release notes")
	if err != nil || len(refs) < 5 || refs[0].GetUrl() != "https://go.dev/doc/go1.25" {
		t.Fatalf("%v %v", refs, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "bing: dial tcp: i/o timeout") {
		t.Fatalf("notes %q", notes)
	}
}

func TestEveryEngineBlockedIsAClearError(t *testing.T) {
	f := &fakeEngines{
		pages: map[string]searchPage{
			"lite.duckduckgo.com": {status: 202, body: readFixture(t, "duckduckgo-block.html")},
			"www.baidu.com":       {status: 200, final: wappassURL, body: readFixture(t, "baidu-block.html")},
			"search.brave.com":    {status: 429},
		},
	}
	stubSearch(t, f)
	ui := &cursorpb.ToolCall{Tool: &cursorpb.ToolCall_WebSearchToolCall{WebSearchToolCall: &cursorpb.WebSearchToolCall{Args: &cursorpb.WebSearchArgs{SearchTerm: "Go 1.25"}}}}
	approved := &cursorpb.WebSearchRequestResponse{Result: &cursorpb.WebSearchRequestResponse_Approved_{Approved: &cursorpb.WebSearchRequestResponse_Approved{}}}
	text, isErr, _ := finishSearch(ui, approved, nil)
	if !isErr || !strings.Contains(text, "baidu: returned a security verification") || !strings.Contains(text, "brave: rate-limited (HTTP 429)") || !strings.Contains(text, "WebFetch") {
		t.Fatalf("%v %q", isErr, text)
	}
	if ui.GetWebSearchToolCall().GetResult().GetError() == nil {
		t.Fatal("the card shows the error")
	}
}

// 查询词本身含“安全验证”“captcha”时，有结果的页面不能被当成验证页。
func TestQueryWordsDoNotLookLikeAVerificationPage(t *testing.T) {
	f := &fakeEngines{pages: map[string]searchPage{
		"www.bing.com": {status: 200, body: "<title>百度安全验证 captcha - 搜索</title>" + readFixture(t, "bing.html")},
	}}
	stubSearch(t, f)
	ui := &cursorpb.ToolCall{Tool: &cursorpb.ToolCall_WebSearchToolCall{WebSearchToolCall: &cursorpb.WebSearchToolCall{Args: &cursorpb.WebSearchArgs{SearchTerm: "百度安全验证 captcha"}}}}
	approved := &cursorpb.WebSearchRequestResponse{Result: &cursorpb.WebSearchRequestResponse_Approved_{Approved: &cursorpb.WebSearchRequestResponse_Approved{}}}
	text, isErr, _ := finishSearch(ui, approved, nil)
	if isErr || strings.Contains(text, "bot-check") || strings.Contains(text, "captcha page") || strings.Contains(text, "verification") || len(ui.GetWebSearchToolCall().GetResult().GetSuccess().GetReferences()) < 5 {
		t.Fatalf("%v %q", isErr, text)
	}
}
