package grokbot

import (
	"net"
	"net/http"
	"strings"
)

// proxyDialTarget 从 HTTP 代理请求里取出要拨号的 host:port 和目标来源。
// CONNECT 用请求 URI 的 authority，其次才是 Host 头；
// 非 CONNECT 绝对形式用 URL.Host，origin-form 用 Host 头。
// 空目标或 HTTP/2 preface 返回 ok=false，绝不把空 host 补成 :80。
func proxyDialTarget(req *http.Request) (target, source string, ok bool) {
	if req == nil {
		return "", "missing", false
	}
	if req.Method == "PRI" || (req.ProtoMajor == 2 && req.Method != http.MethodConnect) {
		return "", "http2-preface", false
	}

	var hostport, src string
	switch {
	case req.Method == http.MethodConnect:
		if req.URL != nil {
			hostport = req.URL.Host
		}
		src = "connect-authority"
		if strings.TrimSpace(hostport) == "" {
			hostport = req.Host
			src = "host-header"
		}
	case req.URL != nil && strings.TrimSpace(req.URL.Host) != "":
		hostport = req.URL.Host
		src = "absolute-uri"
	case strings.TrimSpace(req.Host) != "":
		hostport = req.Host
		src = "host-header"
	default:
		return "", "empty", false
	}

	hostport = strings.TrimSpace(hostport)
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		var addrErr *net.AddrError
		if !errorsIsMissingPort(err, &addrErr) {
			return "", src, false
		}
		host, port = hostport, defaultProxyPort(req)
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", src, false
	}
	return net.JoinHostPort(host, port), src, true
}

func errorsIsMissingPort(err error, addrErr **net.AddrError) bool {
	ae, ok := err.(*net.AddrError)
	if !ok {
		return false
	}
	*addrErr = ae
	return ae.Err == "missing port in address"
}

func defaultProxyPort(req *http.Request) string {
	if req.Method == http.MethodConnect {
		return "443"
	}
	if req.URL != nil && strings.EqualFold(req.URL.Scheme, "https") {
		return "443"
	}
	return "80"
}

func safeHTTPMethod(m string) string {
	switch strings.ToUpper(m) {
	case "CONNECT", "GET", "POST", "HEAD", "PUT", "DELETE", "OPTIONS", "PATCH", "PRI":
		return strings.ToUpper(m)
	case "":
		return "empty"
	default:
		return "other"
	}
}
