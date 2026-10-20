package tools

import (
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

type webHit struct {
	title    string
	url      string
	chunk    string
	engine   string
	rank     int
	coverage float64 // 查询词覆盖率，见 termCoverage
}

type searchEngine struct {
	name     string
	url      func(term string) string
	parse    func(string) []webHit
	fallback bool // 只在主引擎合起来不到 fallbackBelow 条时才查
}

var searchEngines = []searchEngine{
	{name: "bing", url: func(t string) string { return "https://www.bing.com/search?q=" + url.QueryEscape(t) + "&count=10" }, parse: parseBing},
	// DuckDuckGo 用 lite 版：2026-10-11 实测走代理连发 20 次都正常出结果，HTML 版同期时常回人机挑战页。
	{name: "duckduckgo", url: func(t string) string { return "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(t) }, parse: parseDuck},
	{name: "baidu", url: func(t string) string { return "https://www.baidu.com/s?wd=" + url.QueryEscape(t) }, parse: parseBaidu},
	{name: "brave", url: func(t string) string { return "https://search.brave.com/search?q=" + url.QueryEscape(t) }, parse: parseBrave, fallback: true},
}

const (
	// fallbackBelow：主引擎合起来的相关结果（见 relevance.go）不到这么多条时再查备用引擎。
	// 按相关结果而不是总条数算：不带 cookie 的 Bing 几乎总能回 10 条，但多半只按第一个词搜，内容偏题。
	fallbackBelow = 3
	// blockCooldown 内不再请求刚返回过人机验证页的引擎：再请求多半还是验证页，只会拖慢这次搜索。
	blockCooldown = 15 * time.Minute
	// rateLimitCooldown：HTTP 429 限流（Brave 连查几次就会触发）后歇一会儿再查。
	rateLimitCooldown = 5 * time.Minute
	// unreachableCooldown：连不上（超时、拒绝连接）的引擎歇一会儿再试。不走代理时 DuckDuckGo、Brave
	// 常常直接连不上，不歇的话每次搜索都要白等到超时。
	unreachableCooldown = 5 * time.Minute
	// searchTimeout：走代理实测各引擎 0.5–3 秒出页面，8 秒足够，连不上时也不至于拖太久。
	searchTimeout = 8 * time.Second
)

// searchPage 是一次搜索页请求的结果。final 是跟随跳转后的地址，location 是最后一个响应的 Location
// （百度有时直接回 503 并在 Location 里指向验证页，不跳转）。
type searchPage struct {
	status   int
	final    string
	location string
	body     string
}

var fetchSearch = func(dial dialer.Func, rawURL string) (searchPage, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return searchPage{}, err
	}
	req.Header.Set("User-Agent", "cursor-inner")
	resp, err := webClient(dial, searchTimeout).Do(req)
	if err != nil {
		return searchPage{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return searchPage{}, err
	}
	return searchPage{status: resp.StatusCode, final: resp.Request.URL.String(), location: resp.Header.Get("Location"), body: string(body)}, nil
}

var (
	searchNow     = time.Now
	blockedMu     sync.Mutex
	blockedEngine = map[string]engineBlock{}
)

type engineBlock struct {
	until time.Time
	why   string
}

// engineCooling 返回冷却中引擎被跳过的原因；不在冷却期返回空串。
func engineCooling(name string) string {
	blockedMu.Lock()
	defer blockedMu.Unlock()
	if b := blockedEngine[name]; searchNow().Before(b.until) {
		return b.why
	}
	return ""
}

func markBlocked(name, why string, cooldown time.Duration) {
	blockedMu.Lock()
	blockedEngine[name] = engineBlock{until: searchNow().Add(cooldown), why: why}
	blockedMu.Unlock()
}

// searchBlocked 认出人机验证页，返回交给模型的原因；不是验证页时返回空串。
// 特征取自 2026-10-11 的真实请求（testdata/*-block.html）：
//   - 百度：302（偶尔 503）跳到 wappass.baidu.com/static/captcha/…，落地页标题“百度安全验证”；
//   - DuckDuckGo HTML 版：HTTP 202，页面是 anomaly-modal 选鸭子的挑战；
//   - 其余（搜狗 /antispider/、Startpage /sp/captcha-block、Mojeek 标题 Captcha）按地址和标题通用识别。
func searchBlocked(page searchPage) string {
	where := strings.ToLower(hostPath(page.final) + " " + hostPath(page.location))
	body := page.body
	title := ""
	if m := titleTag.FindStringSubmatch(body); m != nil {
		title = strings.ToLower(stripTags(m[1]))
	}
	switch {
	case strings.Contains(where, "wappass.baidu.com") || strings.Contains(title, "百度安全验证"):
		return "returned a security verification (captcha) page instead of results"
	case strings.Contains(body, "anomaly-modal") || strings.Contains(body, "duckduckgo.com/anomaly.js"):
		return "returned a bot-check challenge instead of results"
	case strings.Contains(where, "captcha") || strings.Contains(where, "/antispider") || strings.Contains(where, "/challenge"),
		strings.Contains(title, "captcha") || strings.Contains(title, "安全验证") || strings.Contains(title, "验证码"):
		return "returned a captcha page instead of results"
	}
	return ""
}

// hostPath 只取主机和路径：查询参数里有用户的搜索词，不能拿来判断。
func hostPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	return u.Host + u.Path
}

// FederatedSearch 并行抓取主引擎的搜索页，按倒数排名合并，再按查询词覆盖率把相关结果排到前面；
// 相关结果太少时再查备用引擎。返回的 notes 说明哪些引擎被跳过、哪些结果偏题，交给模型；验证页不会当成结果。
// 一条结果都没有、且有引擎出错或被拦时返回错误，错误里列出每个引擎的原因。
func FederatedSearch(web dialer.Func, term string) ([]*cursorpb.WebSearchReference, []string, error) {
	terms := queryTerms(term)
	var lists [][]webHit
	var notes, failures []string
	run := func(engines []searchEngine) {
		type outcome struct {
			hits []webHit
			note string
			fail string
		}
		results := make([]outcome, len(engines))
		var wg sync.WaitGroup
		for i, engine := range engines {
			if why := engineCooling(engine.name); why != "" {
				results[i].note = engine.name + ": skipped, " + why
				continue
			}
			wg.Add(1)
			go func(i int, engine searchEngine) {
				defer wg.Done()
				page, err := fetchSearch(web, engine.url(term))
				if err != nil {
					markBlocked(engine.name, "it was unreachable from this network recently", unreachableCooldown)
					results[i].fail = fmt.Sprintf("%s: %v", engine.name, err)
					return
				}
				var hits []webHit
				if page.status < 400 {
					hits = engine.parse(page.body)
				}
				// 解析得出结果的页面不会是验证页；只在没有结果时才判断，免得查询词里带“captcha”“安全验证”时误判。
				if len(hits) == 0 {
					if reason := searchBlocked(page); reason != "" {
						markBlocked(engine.name, "it returned a bot-check page recently", blockCooldown)
						results[i].note = engine.name + ": " + reason
						return
					}
				}
				if page.status == http.StatusTooManyRequests {
					markBlocked(engine.name, "it rate-limited us (HTTP 429) recently", rateLimitCooldown)
					results[i].fail = engine.name + ": rate-limited (HTTP 429)"
					return
				}
				if page.status >= 400 {
					results[i].fail = fmt.Sprintf("%s: HTTP %d", engine.name, page.status)
					return
				}
				onTopic := 0
				for j := range hits {
					hits[j].engine = engine.name
					hits[j].rank = j + 1
					hits[j].coverage = termCoverage(terms, hits[j])
					if hits[j].coverage >= relevantCoverage {
						onTopic++
					}
				}
				if len(hits) > 0 && onTopic == 0 {
					results[i].note = engine.name + ": its results matched few of the query words (off-topic), ranked last or dropped"
				}
				results[i].hits = hits
			}(i, engine)
		}
		wg.Wait()
		for _, r := range results {
			if len(r.hits) > 0 {
				lists = append(lists, r.hits)
			}
			if r.note != "" {
				notes = append(notes, r.note)
			}
			if r.fail != "" {
				failures = append(failures, r.fail)
			}
		}
	}
	var primary, backup []searchEngine
	for _, engine := range searchEngines {
		if engine.fallback {
			backup = append(backup, engine)
		} else {
			primary = append(primary, engine)
		}
	}
	run(primary)
	if countRelevant(rrf(lists)) < fallbackBelow && len(backup) > 0 {
		run(backup)
	}
	merged := rankByRelevance(rrf(lists))
	if len(merged) == 0 && (len(notes) > 0 || len(failures) > 0) {
		return nil, notes, fmt.Errorf("web search found nothing usable: %s. Search engines may be blocking automated queries from this network; try again later, rephrase, or use WebFetch on a known documentation URL", strings.Join(append(append([]string{}, notes...), failures...), "; "))
	}
	if len(merged) > 0 && countRelevant(merged) == 0 {
		notes = append(notes, "none of these results contain most of the query words; they are probably off-topic. Rephrase, or use WebFetch on a known documentation URL")
	}
	var refs []*cursorpb.WebSearchReference
	for _, hit := range merged {
		if len(refs) >= 8 {
			break
		}
		refs = append(refs, &cursorpb.WebSearchReference{Title: hit.title, Url: hit.url, Chunk: hit.chunk})
	}
	return refs, append(notes, failures...), nil
}

func countRelevant(hits []webHit) int {
	n := 0
	for _, hit := range hits {
		if hit.coverage >= relevantCoverage {
			n++
		}
	}
	return n
}

// rankByRelevance 把相关结果（覆盖率 ≥ relevantCoverage）按原有的合并顺序排在前面，其余按覆盖率排在后面；
// 相关结果已有 fallbackBelow 条以上时，覆盖率不到一半的直接丢掉——那多半是只按第一个词搜出来的首页、百科。
func rankByRelevance(hits []webHit) []webHit {
	var good, rest []webHit
	for _, hit := range hits {
		if hit.coverage >= relevantCoverage {
			good = append(good, hit)
		} else {
			rest = append(rest, hit)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].coverage > rest[j].coverage })
	if len(good) >= fallbackBelow {
		kept := rest[:0]
		for _, hit := range rest {
			if hit.coverage >= 0.5 {
				kept = append(kept, hit)
			}
		}
		rest = kept
	}
	return append(good, rest...)
}

func rrf(lists [][]webHit) []webHit {
	type acc struct {
		hit   webHit
		score float64
	}
	order := []string{}
	byURL := map[string]*acc{}
	for _, list := range lists {
		for _, hit := range list {
			key := hit.url
			item := byURL[key]
			if item == nil {
				copy := hit
				item = &acc{hit: copy}
				byURL[key] = item
				order = append(order, key)
			}
			item.score += 1 / float64(60+hit.rank)
			if len(hit.chunk) > len(item.hit.chunk) {
				item.hit.chunk = hit.chunk
			}
			if hit.coverage > item.hit.coverage {
				item.hit.coverage = hit.coverage
			}
			if item.hit.title == "" {
				item.hit.title = hit.title
			}
		}
	}
	out := make([]webHit, 0, len(order))
	for _, key := range order {
		out = append(out, byURL[key].hit)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if byURL[out[j].url].score > byURL[out[i].url].score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

var (
	bingItem  = regexp.MustCompile(`(?s)<li class="b_algo".*?</li>`)
	ddgItem   = regexp.MustCompile(`(?s)<div class="result[\s"].*?class="result__snippet".*?</a>`)
	baiduItem = regexp.MustCompile(`(?s)<div class="result c-container.*?</div>\s*</div>`)
	anchor    = regexp.MustCompile(`(?s)<a [^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	bingLink  = regexp.MustCompile(`(?s)<h2[^>]*>\s*<a [^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgLink   = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnip   = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>|<td class="result-snippet">(.*?)</td>`)
	bingSnip  = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)
	baiduLink = regexp.MustCompile(`(?s)<h3[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	baiduSnip = regexp.MustCompile(`(?s)<span class="content-right_[^"]*">(.*?)</span>|<div class="c-abstract">(.*?)</div>`)
	braveItem = regexp.MustCompile(`<div class="snippet[^"]*" data-pos="\d+" data-type="web"`)
	braveLink = regexp.MustCompile(`(?s)<a href="([^"]+)"[^>]*>.*?class="title search-snippet-title[^"]*"[^>]*>(.*?)</div>`)
	braveSnip = regexp.MustCompile(`(?s)<div class="content [^"]*">(.*?)</div>`)
	titleTag  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	liteItem  = regexp.MustCompile(`<a [^>]*class=['"]result-link['"]`)
	liteLink  = regexp.MustCompile(`(?s)<a [^>]*href="([^"]+)"[^>]*class=['"]result-link['"][^>]*>(.*?)</a>`)
	liteSnip  = regexp.MustCompile(`(?s)<td class=['"]result-snippet['"]>(.*?)</td>`)
)

// parseDuck 先按 lite 版解析，解析不出再按 HTML 版（两种页面结构不同）。
func parseDuck(page string) []webHit {
	if hits := parseDuckLite(page); len(hits) > 0 {
		return hits
	}
	return parseDuckHTML(page)
}

// parseDuckLite 解析 lite.duckduckgo.com：每条结果是一个 result-link 链接加后面一行 result-snippet，
// 按链接切块。
func parseDuckLite(page string) []webHit {
	return parseBlockList(splitAt(page, liteItem), liteLink, liteSnip)
}

// splitAt 按每条结果的起始标记切块，用于结果块没有可靠结束标记的页面。
func splitAt(page string, start *regexp.Regexp) []string {
	starts := start.FindAllStringIndex(page, 10)
	var blocks []string
	for i, at := range starts {
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		blocks = append(blocks, page[at[0]:end])
	}
	return blocks
}

// parseBrave 按每条网页结果的起始标记切块（结果块没有可靠的结束标记）。
func parseBrave(page string) []webHit {
	return parseBlockList(splitAt(page, braveItem), braveLink, braveSnip)
}

func parseBing(page string) []webHit     { return parseBlocks(page, bingItem, bingLink, bingSnip) }
func parseDuckHTML(page string) []webHit { return parseBlocks(page, ddgItem, ddgLink, ddgSnip) }
func parseBaidu(page string) []webHit    { return parseBlocks(page, baiduItem, baiduLink, baiduSnip) }

func parseBlocks(page string, item, link, snip *regexp.Regexp) []webHit {
	return parseBlockList(item.FindAllString(page, 10), link, snip)
}

func parseBlockList(blocks []string, link, snip *regexp.Regexp) []webHit {
	var out []webHit
	for _, block := range blocks {
		lm := link.FindStringSubmatch(block)
		if lm == nil {
			continue
		}
		href, title := unwrapSearchURL(html.UnescapeString(lm[1])), stripTags(lm[2])
		if !keepSearchHit(title, href) {
			continue
		}
		chunk := ""
		if sm := snip.FindStringSubmatch(block); sm != nil {
			for _, part := range sm[1:] {
				if strings.TrimSpace(part) != "" {
					chunk = stripTags(part)
					break
				}
			}
		}
		out = append(out, webHit{title: title, url: href, chunk: chunk})
	}
	return out
}

func keepSearchHit(title, href string) bool {
	if title == "" || href == "" || strings.HasPrefix(href, "/") {
		return false
	}
	u, err := url.Parse(href)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch host {
	case "bing.com", "duckduckgo.com", "baidu.com", "google.com", "search.brave.com", "brave.com":
		return false
	}
	return true
}

func unwrapSearchURL(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, "duckduckgo.com") {
		if raw := u.Query().Get("uddg"); strings.HasPrefix(raw, "http") {
			return raw
		}
	}
	if strings.Contains(host, "bing.com") {
		if raw := u.Query().Get("u"); raw != "" {
			if strings.HasPrefix(raw, "http") {
				return raw
			}
			if decoded := decodeBingU(raw); decoded != "" {
				return decoded
			}
		}
	}
	return href
}

func decodeBingU(raw string) string {
	raw = strings.TrimPrefix(raw, "a1")
	if pad := len(raw) % 4; pad != 0 {
		raw += strings.Repeat("=", 4-pad)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(raw)
	}
	if err != nil || !strings.HasPrefix(string(decoded), "http") {
		return ""
	}
	return string(decoded)
}

func stripTags(s string) string {
	s = htmlTag.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}
