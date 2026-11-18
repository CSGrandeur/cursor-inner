package antiaccount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"cursor-inner/internal/i18n"
)

// loginFlow 是一次进行中的 Google 授权：本机回环回调 + 授权链接。
// 浏览器完成授权后 Google 重定向回 http://localhost:<port>/callback，
// 这里校验 state、换票、取账号信息并保存。同一时间只保留一个流程，
// 再次开始会顶掉上一个。
type loginFlow struct {
	state    string
	authURL  string
	redirect string
	codes    chan string
	fail     chan error
	listener []net.Listener
	server   []*http.Server
	once     sync.Once
}

// flowTimeout：授权链接的有效期。过期后回调端口关闭，浏览器会提示无法连接。
const flowTimeout = 5 * time.Minute

// beginLogin 在回环端口上开始一次授权。localhost 可能被浏览器解析成 ::1，
// 所以先占一个随机端口，再尽量把两个协议栈都监听上；只有一个可用时用显式 IP。
func beginLogin(clientID string) (*loginFlow, error) {
	var listeners []net.Listener
	var port int
	host := "localhost"
	if l6, err := net.Listen("tcp", "[::1]:0"); err == nil {
		port = l6.Addr().(*net.TCPAddr).Port
		listeners = append(listeners, l6)
		if l4, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			listeners = append(listeners, l4)
		} else {
			host = "[::1]"
		}
	} else if l4, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		port = l4.Addr().(*net.TCPAddr).Port
		listeners = append(listeners, l4)
		host = "127.0.0.1"
	} else {
		return nil, i18n.Wrap("本机回调端口监听失败：", "Cannot listen on a loopback callback port: ", err)
	}
	var stateBytes [16]byte
	_, _ = rand.Read(stateBytes[:])
	f := &loginFlow{
		state:    hex.EncodeToString(stateBytes[:]),
		redirect: fmt.Sprintf("http://%s:%d/callback", host, port),
		codes:    make(chan string, 1),
		fail:     make(chan error, 1),
		listener: listeners,
	}
	f.authURL = authURL(clientID, f.redirect, f.state)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", f.handleCallback)
	for _, ln := range listeners {
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		f.server = append(f.server, srv)
		go func(s *http.Server, l net.Listener) { _ = s.Serve(l) }(srv, ln)
	}
	return f, nil
}

// shutdown 关掉回调监听。授权码或错误已经送出后调用，浏览器页面已渲染完。
func (f *loginFlow) shutdown() {
	f.once.Do(func() {
		for _, s := range f.server {
			_ = s.Close()
		}
	})
}

func (f *loginFlow) cancel() {
	select {
	case f.fail <- i18n.E("授权已取消", "Authorization cancelled"):
	default:
	}
	f.shutdown()
}

// waitCode 等浏览器送回授权码，超时或 ctx 结束时关掉回调端口。
func (f *loginFlow) waitCode(ctx context.Context) (string, error) {
	timer := time.NewTimer(flowTimeout)
	defer timer.Stop()
	defer f.shutdown()
	select {
	case code := <-f.codes:
		return code, nil
	case err := <-f.fail:
		return "", err
	case <-timer.C:
		return "", i18n.E("授权超时，请重新添加账号。", "Authorization timed out. Add the account again.")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *loginFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("state") != f.state {
		writeCallbackPage(w, http.StatusBadRequest, false)
		f.failOnce(i18n.E("回调的 state 对不上，已拒绝。", "The callback state did not match; refused."))
		return
	}
	if e := q.Get("error"); e != "" {
		writeCallbackPage(w, http.StatusBadRequest, false)
		f.failOnce(i18n.Ef("Google 返回授权错误：%s", "Google returned an authorization error: %s", e))
		return
	}
	code := q.Get("code")
	if code == "" {
		writeCallbackPage(w, http.StatusBadRequest, false)
		f.failOnce(i18n.E("回调里没有授权码", "The callback has no authorization code"))
		return
	}
	// 先回成功页：浏览器不用等换票的网络往返。换票结果由配置页展示。
	writeCallbackPage(w, http.StatusOK, true)
	select {
	case f.codes <- code:
	default:
	}
}

func (f *loginFlow) failOnce(err error) {
	select {
	case f.fail <- err:
	default:
	}
}

// callbackPage 是浏览器里显示的授权结果页，中英并列，不依赖配置页语言。
func writeCallbackPage(w http.ResponseWriter, status int, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	title, body := "✅ 授权成功 · Authorization successful", "可以关闭此页面，回到 cursor-inner。You can close this page and return to cursor-inner."
	if !ok {
		title, body = "❌ 授权失败 · Authorization failed", "请回到 cursor-inner 重试。Please return to cursor-inner and try again."
	}
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>cursor-inner</title></head>
<body style="font-family:system-ui,sans-serif;text-align:center;padding:60px;background:#151514;color:#f4f1ea">
<h2>%s</h2><p>%s</p></body></html>`, title, body)
}
