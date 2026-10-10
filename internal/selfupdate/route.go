package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// DialFunc 与 dialer.Func 同形，避免本包依赖 config。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Route 是一条出网路径。默认路径用系统网络（含系统环境里的代理变量）；
// 代理路径用配置页里的出站代理。
type Route struct {
	Name   string // "default" 或 "proxy"
	Client *http.Client
}

// DefaultRoute 是不经配置代理的默认路径。
func DefaultRoute() Route {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return Route{Name: "default", Client: &http.Client{Transport: tr}}
}

// ProxyRoute 经配置的出站代理拨号。
func ProxyRoute(dial DialFunc) Route {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = dial
	return Route{Name: "proxy", Client: &http.Client{Transport: tr}}
}

// Fetched 是一次成功的请求结果。
type Fetched struct {
	Body     []byte
	FinalURL string // 跟随重定向后的地址，用来拼同目录的下载地址
	Route    Route
}

// RouteAttempt 记录每条路径的尝试结果，写进日志和界面。
type RouteAttempt struct {
	Route string
	Err   error
}

// FetchFirst 依次在各路径上取 url：默认路径先试，超时或失败再试代理路径。
// 先成功的那条就是之后下载用的路径。都失败时返回每条路径的错误。
func FetchFirst(ctx context.Context, routes []Route, url string, timeout time.Duration, limit int64) (Fetched, []RouteAttempt, error) {
	var attempts []RouteAttempt
	for _, r := range routes {
		body, final, err := fetch(ctx, r, url, timeout, limit)
		attempts = append(attempts, RouteAttempt{Route: r.Name, Err: err})
		if err == nil {
			return Fetched{Body: body, FinalURL: final, Route: r}, attempts, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return Fetched{}, attempts, joinAttempts(attempts)
}

func joinAttempts(attempts []RouteAttempt) error {
	if len(attempts) == 0 {
		return errors.New("没有可用的网络路径")
	}
	var errs []error
	for _, a := range attempts {
		errs = append(errs, fmt.Errorf("%s: %w", a.Route, a.Err))
	}
	return errors.Join(errs...)
}

func fetch(ctx context.Context, r Route, url string, timeout time.Duration, limit int64) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "cursor-inner-updater")
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > limit {
		return nil, "", errors.New("响应过大")
	}
	return body, resp.Request.URL.String(), nil
}

var errNotFound = errors.New("HTTP 404")
