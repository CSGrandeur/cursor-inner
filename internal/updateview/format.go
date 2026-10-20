package updateview

import (
	"fmt"
	"strconv"
	"time"
)

// 下面的排版规则和 page.html 里的 upSize / upSpeed / upEta 一一对应，改一边要同时改另一边
// （format_test.go 里的表格同时是两边的约定）。

const mib = 1 << 20

// Size：不到 1 MB 用整数 KB（至少 1 KB），否则保留一位小数的 MB。
func Size(n int64) string {
	if n >= mib {
		return strconv.FormatFloat(float64(n)/mib, 'f', 1, 64) + " MB"
	}
	if n <= 0 {
		return "0 KB"
	}
	return strconv.FormatInt(max(1, (n+512)/1024), 10) + " KB"
}

// SizePair 是“已下 / 总大小”，单位跟总大小走：“6.2 / 14.9 MB”。
func SizePair(done, total int64) string {
	if total <= 0 {
		return Size(done)
	}
	if total >= mib {
		return strconv.FormatFloat(float64(done)/mib, 'f', 1, 64) + " / " + Size(total)
	}
	return strconv.FormatInt((done+512)/1024, 10) + " / " + Size(total)
}

// Speed：“1.3 MB/s”“64 KB/s”。
func Speed(bps int64) string { return Size(bps) + "/s" }

// ETA：不到 90 秒按秒，否则按分钟向上取整。
func ETA(sec int64) string {
	if sec < 90 {
		return strconv.FormatInt(sec, 10) + " 秒"
	}
	return strconv.FormatInt((sec+59)/60, 10) + " 分钟"
}

// Duration 是已经用掉的时间：“11.7 秒”“2 分 05 秒”。
func Duration(d time.Duration) string {
	if d < time.Minute {
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " 秒"
	}
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d 分 %02d 秒", s/60, s%60)
}

// RouteName 是网络路径的中文名。
func RouteName(r string) string {
	switch r {
	case "default":
		return "默认网络"
	case "proxy":
		return "代理"
	}
	return r
}

// SourceName 是更新源的显示名。
func SourceName(s string) string {
	switch s {
	case "github":
		return "GitHub"
	case "gitee":
		return "Gitee"
	}
	return s
}
