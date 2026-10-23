package web

import (
	"regexp"
	"strings"
	"testing"
)

func pageSource(t *testing.T) string {
	t.Helper()
	raw, err := page.ReadFile("page.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 配置页按标签分组：每个设置项必须落在约定的标签里，标签名与 URL hash（托盘直达 #update）一致。
func TestPageTabsGroupSettings(t *testing.T) {
	src := pageSource(t)
	for _, tab := range []string{"models", "network", "update", "general"} {
		if !strings.Contains(src, `id="tab-`+tab+`" data-tab="`+tab+`" aria-controls="pane-`+tab+`"`) {
			t.Fatalf("tab %s missing or not wired to its pane", tab)
		}
	}
	if !strings.Contains(src, `const TABS = ["models", "network", "general", "update"];`) {
		t.Fatal("tab list in script out of sync with the markup")
	}
	if strings.Index(src, `id="tab-update"`) < strings.Index(src, `id="tab-general"`) {
		t.Fatal("update tab must be last")
	}
	if strings.Index(src, `id="proxy-address"`) > strings.Index(src, `id="takeover-cursor"`) {
		t.Fatal("proxy settings must come before the takeover switches")
	}
	codexBtn := src[strings.Index(src, `id="open-codex"`):strings.Index(src, `id="open-grok"`)]
	if !strings.Contains(codexBtn, `class="app-icon"`) {
		t.Fatal("Open Codex needs the app icon")
	}
	panes := regexp.MustCompile(`<div class="pane" role="tabpanel" id="pane-([a-z]+)"`).FindAllStringSubmatchIndex(src, -1)
	if len(panes) != 4 {
		t.Fatalf("want 4 panes, got %d", len(panes))
	}
	paneOf := func(id string) string {
		at := strings.Index(src, `id="`+id+`"`)
		if at < 0 {
			t.Fatalf("control %s missing", id)
		}
		name := ""
		for _, p := range panes {
			if p[0] < at {
				name = src[p[2]:p[3]]
			}
		}
		if end := strings.Index(src, "\n  </section>"); at > end {
			return ""
		}
		return name
	}
	want := map[string][]string{
		"models":  {"model-table", "model-form", "draft-test"},
		"network": {"takeover-cursor", "takeover-grok", "takeover-codex", "tun-on", "strict-on", "proxy-address", "scope"},
		"update":  {"update-row", "update-check", "up-steps", "up-fill"},
		"general": {"image-url", "autostart", "background", "notify"},
	}
	for tab, ids := range want {
		for _, id := range ids {
			if got := paneOf(id); got != tab {
				t.Errorf("%s is in tab %q, want %q", id, got, tab)
			}
		}
	}
	// 状态条在标签外，切到哪个标签都看得见。
	for _, id := range []string{"st-ver", "st-take", "st-proxy-v", "st-update"} {
		if at, first := strings.Index(src, `id="`+id+`"`), panes[0][0]; at < 0 || at > first {
			t.Errorf("status item %s must sit above the tabs", id)
		}
	}
}

// 页面里引用的每个文案键（data-i18n*、t("…")）在中英文里都要有，避免某个语言显示成键名。
func TestPageMessagesCoverBothLanguages(t *testing.T) {
	src := pageSource(t)
	zhAt, enAt, endAt := strings.Index(src, "\n  zh: {"), strings.Index(src, "\n  en: {"), strings.Index(src, "const LANG_KEY")
	if zhAt < 0 || enAt < zhAt || endAt < enAt {
		t.Fatal("MESSAGES block not found")
	}
	keyRe := regexp.MustCompile(`(?:^|[\s,{])([A-Za-z_][A-Za-z0-9_]*): "`)
	keys := func(block string) map[string]bool {
		out := map[string]bool{}
		for _, m := range keyRe.FindAllStringSubmatch(block, -1) {
			out[m[1]] = true
		}
		return out
	}
	zh, en := keys(src[zhAt:enAt]), keys(src[enAt:endAt])
	used := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`data-i18n(?:-title|-aria|-placeholder|-tip)?="([A-Za-z0-9_]+)"`),
		regexp.MustCompile(`\bt\("([A-Za-z0-9_]+)"`),
	} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			used[m[1]] = true
		}
	}
	if len(used) < 50 {
		t.Fatalf("only %d keys found; the scan is broken", len(used))
	}
	for k := range used {
		if !zh[k] {
			t.Errorf("zh missing %q", k)
		}
		if !en[k] {
			t.Errorf("en missing %q", k)
		}
	}
	// 设置项默认只有一行短说明，详细的放在“?”里：短说明不能比详细说明长，也不能太长。
	descRe := regexp.MustCompile(`([a-zA-Z]+)Desc: "([^"]*)"`)
	for _, block := range []string{src[zhAt:enAt], src[enAt:endAt]} {
		for _, m := range descRe.FindAllStringSubmatch(block, -1) {
			if n := len([]rune(m[2])); n > 90 {
				t.Errorf("%sDesc is %d chars; keep it to one short line and move details into %sTip", m[1], n, m[1])
			}
		}
	}
}

// 更新失败的原始错误可能带下载地址（含签名查询串）等开发细节：页面只显示分类后的说明，不拼原始错误。
func TestUpdatePageNeverShowsRawError(t *testing.T) {
	src := pageSource(t)
	if strings.Contains(src, `t("upDetail"`) || strings.Contains(src, `: (error || "")`) {
		t.Fatal("update status must not render the raw error text")
	}
	if !strings.Contains(src, `upE_unknown:`) {
		t.Fatal("unclassified update failures need a what-to-do message")
	}
}
