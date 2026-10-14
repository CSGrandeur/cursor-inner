package web

import (
	"strings"
	"testing"
)

// 分段开关选中态要醒目：主题强调色实底 + 白字，未选中灰字。
func TestSegSelectedIsProminent(t *testing.T) {
	src := pageSource(t)
	i := strings.Index(src, `.seg button[aria-pressed="true"] {`)
	if i < 0 {
		t.Fatal("selected seg rule missing")
	}
	rule := src[i : i+strings.Index(src[i:], "}")]
	if !strings.Contains(rule, "background: var(--signal)") || !strings.Contains(rule, "color: #fff") {
		t.Fatal("selected seg must be accent fill with white text")
	}
}

// 代理范围缺省按仅官方（与 98df0e7 一致）。
func TestScopeDefaultsToOfficial(t *testing.T) {
	src := pageSource(t)
	if !strings.Contains(src, `data-scope="official" data-i18n="scopeOfficial" aria-pressed="true"`) {
		t.Fatal("official must be pre-highlighted")
	}
	if !strings.Contains(src, `const official = state.proxy.scope !== "global";`) {
		t.Fatal("missing/unknown scope must render as official")
	}
}
