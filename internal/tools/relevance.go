package tools

import (
	"strings"
	"unicode"
)

// 搜索结果相关度：按查询词覆盖率估算。用来发现搜索引擎“偏题”——例如不带 cookie 的 Bing
// 对“Go 1.25 release notes”只按 Go 返回 Google 首页、围棋百科——这时再查别的引擎补上。
//
// 查询词切法：按空白和标点切开；英文词、版本号按词边界整词匹配（"go" 不算命中 "Google"，
// "1.25" 算命中 "1.25.3"）；连续汉字按相邻两字切成二元组，一段里命中一半以上算这一段命中。

var searchStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "on": true, "for": true, "to": true,
	"and": true, "or": true, "is": true, "are": true, "how": true, "what": true, "with": true, "vs": true,
	"的": true, "怎么": true, "如何": true, "什么": true, "和": true, "与": true,
}

type queryTerm struct {
	word    string   // 英文词或版本号，小写
	bigrams []string // 汉字段的二元组
}

func queryTerms(query string) []queryTerm {
	var terms []queryTerm
	seen := map[string]bool{}
	add := func(t queryTerm) {
		key := t.word + "|" + strings.Join(t.bigrams, ",")
		if !seen[key] {
			seen[key] = true
			terms = append(terms, t)
		}
	}
	var ascii, han []rune
	flushASCII := func() {
		w := strings.Trim(strings.ToLower(string(ascii)), ".-_")
		ascii = ascii[:0]
		if w != "" && !searchStopwords[w] && (len(w) > 1 || unicode.IsDigit(rune(w[0]))) {
			add(queryTerm{word: w})
		}
	}
	flushHan := func() {
		run := han
		han = han[:0]
		if len(run) == 0 || searchStopwords[string(run)] {
			return
		}
		if len(run) == 1 {
			add(queryTerm{bigrams: []string{string(run)}})
			return
		}
		var grams []string
		for i := 0; i+1 < len(run); i++ {
			grams = append(grams, string(run[i:i+2]))
		}
		add(queryTerm{bigrams: grams})
	}
	for _, r := range query {
		switch {
		case unicode.Is(unicode.Han, r):
			flushASCII()
			han = append(han, r)
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' || r == '+' || r == '#'):
			flushHan()
			ascii = append(ascii, unicode.ToLower(r))
		default:
			flushASCII()
			flushHan()
		}
	}
	flushASCII()
	flushHan()
	return terms
}

// termCoverage 是一条结果（标题 + 摘要 + 网址）覆盖了多少比例的查询词，0 到 1。
func termCoverage(terms []queryTerm, hit webHit) float64 {
	if len(terms) == 0 {
		return 1
	}
	// 结果页把命中词加粗，去标签后会在“Node</b>.js”处留下空格，先合回去。
	text := strings.ReplaceAll(strings.ToLower(hit.title+" "+hit.chunk+" "+hit.url), " .", ".")
	matched := 0
	for _, t := range terms {
		if t.word != "" {
			if containsWord(text, t.word) {
				matched++
			}
			continue
		}
		n := 0
		for _, g := range t.bigrams {
			if strings.Contains(text, g) {
				n++
			}
		}
		if n*2 >= len(t.bigrams) {
			matched++
		}
	}
	return float64(matched) / float64(len(terms))
}

func containsWord(text, word string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(word)
		if (start == 0 || !wordChar(text[start-1])) && (end == len(text) || !wordChar(text[end])) {
			return true
		}
		from = start + 1
	}
}

func wordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// relevantHits 统计前 n 条里覆盖率达到 relevantCoverage 的条数。
func relevantHits(query string, hits []webHit, n int) int {
	terms := queryTerms(query)
	count := 0
	for i, hit := range hits {
		if i >= n {
			break
		}
		if termCoverage(terms, hit) >= relevantCoverage {
			count++
		}
	}
	return count
}

const relevantCoverage = 0.6
