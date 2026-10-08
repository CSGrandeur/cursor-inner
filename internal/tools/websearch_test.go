package tools

import (
	"os"
	"strings"
	"testing"
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
