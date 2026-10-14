package config

import "testing"

// 默认只接管官方流量：空或认不出的范围按「仅官方」，只有明确写 global 才全局。
func TestScopeDefaultsToOfficial(t *testing.T) {
	for in, want := range map[string]string{"": ScopeOfficial, "bogus": ScopeOfficial, "official": ScopeOfficial, " Global ": ScopeGlobal} {
		if got := (Proxy{Scope: in}).ScopeName(); got != want {
			t.Errorf("Scope %q => %q, want %q", in, got, want)
		}
	}
}
