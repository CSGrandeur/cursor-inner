package web

import (
	"net"
	"net/http"
	"strings"

	"cursor-inner/internal/i18n"
)

// guard 挡住不是从本机配置页发出的请求。
//
// 配置页只监听 127.0.0.1，但浏览器里任何网页都能往本机端口发请求：
//   - Host 必须是回环地址，挡住 DNS rebinding（攻击者域名解析到 127.0.0.1 后读写接口）；
//   - 改动类请求若带 Origin，必须与 Host 同源；带 Sec-Fetch-Site 时只接受 same-origin / none，
//     挡住跨站表单或 text/plain fetch 这类不触发预检的 CSRF（例如偷偷加模型、退出程序）。
//
// 不带这些头的本机客户端（curl、测试）照常可用。
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !loopbackHost(r.Host) {
			writeErr(w, http.StatusForbidden, i18n.E("只接受本机地址访问", "Only loopback hosts are accepted"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && !strings.EqualFold(origin, "http://"+r.Host) {
				writeErr(w, http.StatusForbidden, i18n.E("拒绝跨站请求", "Cross-site request refused"))
				return
			}
			switch site := r.Header.Get("Sec-Fetch-Site"); site {
			case "", "same-origin", "none":
			default:
				writeErr(w, http.StatusForbidden, i18n.E("拒绝跨站请求", "Cross-site request refused"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
