package tunnel

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStateText(t *testing.T) {
	for _, s := range []State{StateOff, StateStarting, StateOn, StateFailed, StateUnsupported} {
		if s.Text().Zh == "" {
			t.Fatalf("状态 %d 缺少文案", s)
		}
	}
}

func TestWriteConfigRejectsBadOptions(t *testing.T) {
	m := NewManager(t.TempDir())
	// 代理关闭 / 无 DNS：WriteConfig 直接报错，不落盘。
	if err := m.WriteConfig(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080}); err == nil {
		t.Fatal("无 DNS 时 WriteConfig 应报错")
	}
	if _, err := os.Stat(m.ConfigPath()); !os.IsNotExist(err) {
		t.Fatal("出错时不应写出配置文件")
	}
}

func TestWriteConfigWrites0600(t *testing.T) {
	m := NewManager(t.TempDir())
	err := m.WriteConfig(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080, ProxyType: "socks", DNSUpstreams: []string{"223.5.5.5"}})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(m.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限应为 0600，实际 %v", fi.Mode().Perm())
	}
}

// Enable 在非 Windows 上必须优雅失败：返回错误、状态置为 Unsupported，且绝不 panic，
// 调用方据此保留普通代理接管。这覆盖了“sing-box 不可用 / 平台不支持”的回退路径。
func TestEnableFallbackNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("本用例覆盖非 Windows 回退")
	}
	m := NewManager(t.TempDir())
	err := m.Enable(Options{ProxyHost: "127.0.0.1", ProxyPort: 1080, ProxyType: "socks", DNSUpstreams: []string{"223.5.5.5"}})
	if err == nil {
		t.Fatal("非 Windows 上 Enable 应返回错误")
	}
	st, txt := m.Status()
	if st != StateUnsupported {
		t.Fatalf("状态应为 Unsupported，实际 %d", st)
	}
	if txt.Zh == "" {
		t.Fatal("应有状态文案")
	}
	// Disable 也不应 panic。
	if err := m.Disable(); err != nil {
		t.Fatalf("Disable 不应报错：%v", err)
	}
}

func TestPaths(t *testing.T) {
	m := NewManager("/tmp/x")
	if filepath.Base(m.ConfigPath()) != "singbox-tun.json" {
		t.Fatal("配置文件名不对")
	}
}
