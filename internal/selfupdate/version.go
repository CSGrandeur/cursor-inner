// Package selfupdate 检查、下载、校验并切换到新版本。
//
// 它只管 cursor-inner 自己的二进制：不碰 Grok Bot、Cursor、TUN 进程。
// 进程间交接（handover.go）让新进程接过同一组本机端口，Grok 与 Cursor 不需要重启。
package selfupdate

import (
	"strconv"
	"strings"
)

// Version 是语义化版本号 vMAJOR.MINOR.PATCH[-PRE]。构建元数据（+xxx）被忽略。
type Version struct {
	Major, Minor, Patch int
	Pre                 []string
}

// ParseVersion 解析 v1.2.3、1.2.3、v1.2.3-rc.1。dev 这类非版本号返回 ok=false。
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v Version
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		pre := s[i+1:]
		if pre == "" {
			return Version{}, false
		}
		v.Pre = strings.Split(pre, ".")
		for _, p := range v.Pre {
			if p == "" {
				return Version{}, false
			}
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, false
		}
		*nums[i] = n
	}
	return v, true
}

func (v Version) String() string {
	s := "v" + strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Compare 按 semver 2.0 优先级：-1 a<b，0 相等，1 a>b。预发布版本低于同号正式版。
func Compare(a, b Version) int {
	for _, d := range [3][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, xe := strconv.Atoi(x)
		yn, ye := strconv.Atoi(y)
		switch {
		case xe == nil && ye == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xe == nil:
			return -1
		case ye == nil:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Newer 判断 latest 是否比 current 新。current 不是版本号（dev 构建）时不提示更新；
// 正式版不会被推到预发布版本，除非 allowPre。
func Newer(current, latest string, allowPre bool) bool {
	c, ok := ParseVersion(current)
	if !ok {
		return false
	}
	l, ok := ParseVersion(latest)
	if !ok {
		return false
	}
	if len(l.Pre) > 0 && len(c.Pre) == 0 && !allowPre {
		return false
	}
	return Compare(l, c) > 0
}
