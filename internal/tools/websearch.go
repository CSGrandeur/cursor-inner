package tools

import (
	"encoding/base64"
	"html"
	"net/url"
	"regexp"
	"strings"

	"cursor-inner/internal/cursorpb"
	"cursor-inner/internal/dialer"
)

type webHit struct {
	title  string
	url    string
	chunk  string
	engine string
	rank   int
}

// FederatedSearch 抓取多个搜索页并按倒数排名合并。
func FederatedSearch(web dialer.Func, term string) ([]*cursorpb.WebSearchReference, error) {
	type src struct {
		name string
		raw  string
		pick func(string) []webHit
	}
	sources := []src{
		{"bing", "https://www.bing.com/search?q=" + url.QueryEscape(term) + "&count=10", parseBing},
		{"duckduckgo", "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(term), parseDuckHTML},
		{"baidu", "https://www.baidu.com/s?wd=" + url.QueryEscape(term), parseBaidu},
	}
	var lists [][]webHit
	var firstErr error
	for _, source := range sources {
		body, err := readHTTP(web, source.raw)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		hits := source.pick(string(body))
		for i := range hits {
			hits[i].engine = source.name
			hits[i].rank = i + 1
		}
		if len(hits) > 0 {
			lists = append(lists, hits)
		}
	}
	merged := rrf(lists)
	if len(merged) == 0 && firstErr != nil {
		return nil, firstErr
	}
	var refs []*cursorpb.WebSearchReference
	for _, hit := range merged {
		if len(refs) >= 8 {
			break
		}
		refs = append(refs, &cursorpb.WebSearchReference{Title: hit.title, Url: hit.url, Chunk: hit.chunk})
	}
	return refs, nil
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
)

func parseBing(page string) []webHit     { return parseBlocks(page, bingItem, bingLink, bingSnip) }
func parseDuckHTML(page string) []webHit { return parseBlocks(page, ddgItem, ddgLink, ddgSnip) }
func parseBaidu(page string) []webHit    { return parseBlocks(page, baiduItem, baiduLink, baiduSnip) }

func parseBlocks(page string, item, link, snip *regexp.Regexp) []webHit {
	var out []webHit
	for _, block := range item.FindAllString(page, 10) {
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
	case "bing.com", "duckduckgo.com", "baidu.com", "google.com":
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
