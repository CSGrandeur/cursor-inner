package tools

import "github.com/pmezard/go-difflib/difflib"

// charRatio 对固定的 old_string 反复做字符级相似度。difflib 只为第二个序列建索引，
// 把 old 放在第二位、只换第一位，就不必每个窗口都重建索引。
type charRatio struct {
	m     *difflib.SequenceMatcher
	big   bool
	old   string
	ready bool
}

func newCharRatio(old string) *charRatio {
	c := &charRatio{old: old, big: len(old) > 4000}
	if !c.big {
		c.m = difflib.NewMatcher(nil, runeStrings(old))
		c.ready = true
	}
	return c
}

func (c *charRatio) ratio(window string) float64 {
	if window == c.old {
		return 1
	}
	if c.big || len(window) > 4000 {
		return textRatio(c.old, window)
	}
	c.m.SetSeq1(runeStrings(window))
	// RealQuickRatio / QuickRatio 是 Ratio 的上界；绝大多数窗口在这里就被排除。
	if q := c.m.RealQuickRatio(); q < 0.85 {
		return q
	}
	if q := c.m.QuickRatio(); q < 0.85 {
		return q
	}
	return c.m.Ratio()
}

func runeStrings(s string) []string {
	out := make([]string, 0, len(s))
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}
