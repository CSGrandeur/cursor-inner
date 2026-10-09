package dialer

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/i18n"

	"golang.org/x/net/proxy"

	"cursor-inner/internal/config"
)

type Func func(ctx context.Context, network, addr string) (net.Conn, error)

func Direct() Func {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext
}

func Normalize(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", i18n.E("代理地址为空", "Proxy address is empty")
	}
	if !strings.Contains(address, "://") {
		host, err := bareHost(address)
		if err != nil {
			return "", err
		}
		if scheme, ok := remembered(host); ok {
			return scheme + "://" + host, nil
		}
		return host, nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", i18n.E("代理地址无法解析", "Proxy address cannot be parsed")
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h", "http", "https":
	default:
		return "", i18n.E("代理只支持 socks5、socks5h、http、https", "Only socks5, socks5h, http and https proxies are supported")
	}
	if u.Scheme == "socks5h" {
		u.Scheme = "socks5"
	}
	return u.String(), nil
}

func EffectiveAddress(p config.Proxy) (string, bool, error) {
	if !p.Enabled || strings.TrimSpace(p.Address) == "" {
		return "", false, nil
	}
	spec, err := Normalize(p.Address)
	if err != nil {
		return "", false, err
	}
	return spec, true, nil
}

func RejectSelf(spec, mitmURL string) error {
	if spec == "" || mitmURL == "" {
		return nil
	}
	proxyURL, err := url.Parse(spec)
	if err != nil {
		return err
	}
	local, err := url.Parse(mitmURL)
	if err != nil {
		return err
	}
	if proxyURL.Port() != "" && proxyURL.Port() == local.Port() && loopback(proxyURL.Hostname()) && loopback(local.Hostname()) {
		return i18n.E("代理地址不能指向本工具的接管端口", "The proxy cannot point at cursor-inner's own takeover port")
	}
	return nil
}

func loopback(host string) bool {
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func FromProxy(p config.Proxy) (Func, error) {
	spec, on, err := EffectiveAddress(p)
	if err != nil {
		return nil, err
	}
	if !on {
		return Direct(), nil
	}
	spec, err = withScheme(spec)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(spec)
	if err != nil {
		return nil, err
	}
	base := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	switch u.Scheme {
	case "http", "https":
		return httpProxy(u, base), nil
	}
	d, err := proxy.FromURL(u, base)
	if err != nil {
		return nil, i18n.Wrap("代理不可用：", "Proxy unavailable: ", err)
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext, nil
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.Dial(network, addr)
	}, nil
}

func bareHost(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return "", i18n.E("代理地址无法解析", "Proxy address cannot be parsed")
	}
	return address, nil
}

// withScheme 给没写协议的地址补上探测结果。写了协议的地址原样返回。
func withScheme(spec string) (string, error) {
	if strings.Contains(spec, "://") {
		return spec, nil
	}
	scheme, err := classify(spec)
	if err != nil {
		return "", err
	}
	return scheme + "://" + spec, nil
}

var schemes sync.Map

func remembered(host string) (string, bool) {
	v, ok := schemes.Load(host)
	if !ok {
		return "", false
	}
	scheme, _ := v.(string)
	return scheme, scheme != ""
}

// classify 连上没写协议的代理，看它是不是 socks5。不是就按 http。
// 只记住连得上的结果。代理没开时不记住，下次再探。
func classify(host string) (string, error) {
	if scheme, ok := remembered(host); ok {
		return scheme, nil
	}
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return "", i18n.Wrap("代理不可用：", "Proxy unavailable: ", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return "", i18n.Wrap("代理不可用：", "Proxy unavailable: ", err)
	}
	buf := make([]byte, 2)
	n, _ := io.ReadFull(conn, buf)
	scheme := "http"
	if n == 2 && buf[0] == 0x05 {
		scheme = "socks5"
	}
	schemes.Store(host, scheme)
	return scheme, nil
}

func httpProxy(u *url.URL, base *net.Dialer) Func {
	if u.Scheme == "https" {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, i18n.E("https 代理还不能用，请改成 http 或 socks5", "An https proxy is not available yet. Use http or socks5.")
		}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" {
			return nil, i18n.E("http 代理只支持 tcp", "The http proxy only supports tcp")
		}
		conn, err := base.DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return nil, err
		}
		if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			conn.Close()
			return nil, err
		}
		var auth string
		if u.User != nil {
			user := u.User.Username()
			pass, _ := u.User.Password()
			auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) + "\r\n"
		}
		if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", addr, addr, auth); err != nil {
			conn.Close()
			return nil, err
		}
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if err != nil {
			conn.Close()
			return nil, i18n.Wrap("代理没有接通：", "The proxy did not connect: ", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			conn.Close()
			return nil, i18n.Ef("代理拒绝了连接：%s", "The proxy refused the connection: %s", resp.Status)
		}
		_ = conn.SetDeadline(time.Time{})
		if reader.Buffered() == 0 {
			return conn, nil
		}
		rest, _ := reader.Peek(reader.Buffered())
		return &prefixConn{Conn: conn, rest: append([]byte(nil), rest...)}, nil
	}
}

type prefixConn struct {
	net.Conn
	rest []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.rest) > 0 {
		n := copy(p, c.rest)
		c.rest = c.rest[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func ForModel(global config.Proxy, useProxy bool) (Func, error) {
	if !useProxy {
		return Direct(), nil
	}
	_, on, err := EffectiveAddress(global)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, i18n.E("这个模型要求走代理，但代理开关关闭或地址为空", "This model is set to use the proxy, but the proxy is off or has no address")
	}
	return FromProxy(global)
}
