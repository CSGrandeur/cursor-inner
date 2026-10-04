package dialer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"cursor-inner/internal/config"
	"golang.org/x/net/proxy"
)

type Func func(ctx context.Context, network, addr string) (net.Conn, error)

func Direct() Func {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext
}

func Normalize(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("代理地址为空")
	}
	if !strings.Contains(address, "://") {
		address = "socks5://" + address
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("代理地址无法解析")
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h", "http", "https":
	default:
		return "", errors.New("代理只支持 socks5、socks5h、http、https")
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
		return errors.New("代理地址不能指向本工具的接管端口")
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
	u, err := url.Parse(spec)
	if err != nil {
		return nil, err
	}
	base := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	d, err := proxy.FromURL(u, base)
	if err != nil {
		return nil, fmt.Errorf("代理不可用: %w", err)
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext, nil
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.Dial(network, addr)
	}, nil
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
		return nil, errors.New("这个模型要求走代理，但代理开关关闭或地址为空")
	}
	return FromProxy(global)
}
