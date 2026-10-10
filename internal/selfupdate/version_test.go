package selfupdate

import "testing"

func TestCompare(t *testing.T) {
	order := []string{"v0.9.9", "v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta", "v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0", "v1.0.1", "v1.1.0", "v1.10.0", "v2.0.0"}
	for i := range order {
		for j := range order {
			a, _ := ParseVersion(order[i])
			b, _ := ParseVersion(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s,%s)=%d want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestParseVersion(t *testing.T) {
	for _, bad := range []string{"dev", "", "v1.2", "v1.2.3.4", "v01.2.3", "v1.2.3-", "v1.2.3-a..b", "v1.x.3"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
	v, ok := ParseVersion("1.2.3+build.5")
	if !ok || v.String() != "v1.2.3" {
		t.Fatalf("got %v %v", v, ok)
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, latest string
		pre, want   bool
	}{
		{"v0.3.1", "v0.3.2", false, true},
		{"v0.3.2", "v0.3.2", false, false},
		{"v0.3.3", "v0.3.2", false, false},
		{"dev", "v9.9.9", false, false},
		{"v0.3.1", "v0.4.0-rc.1", false, false},
		{"v0.3.1", "v0.4.0-rc.1", true, true},
		{"v0.4.0-rc.1", "v0.4.0", false, true},
		{"v0.3.1", "garbage", false, false},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.latest, c.pre); got != c.want {
			t.Errorf("Newer(%s,%s,%v)=%v", c.cur, c.latest, c.pre, got)
		}
	}
}
