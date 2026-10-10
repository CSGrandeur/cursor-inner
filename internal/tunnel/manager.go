package tunnel

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"cursor-inner/internal/i18n"
)

// State 是 TUN 的运行状态。无论处于哪种状态，cursor-inner 的普通代理接管（env、flags、桥）都照常工作。
type State int

const (
	StateOff         State = iota // 未开启
	StateStarting                 // 正在请求管理员 / 启动中
	StateOn                       // 已开启
	StateFailed                   // 启动失败或中途退出，已回退到普通代理接管
	StateUnsupported              // 当前平台不支持（非 Windows）
)

// Text 返回状态的中英文说明，供设置页展示。
func (s State) Text() i18n.Text {
	switch s {
	case StateOn:
		return i18n.T("TUN 已开启：官方域名经代理，无泄漏", "TUN on: official domains go through the proxy")
	case StateStarting:
		return i18n.T("TUN 启动中……", "TUN starting…")
	case StateFailed:
		return i18n.T("TUN 未开启，官方端点可能泄漏；已回退到普通代理接管", "TUN off, official endpoints may leak; fell back to normal proxy takeover")
	case StateUnsupported:
		return i18n.T("当前系统不支持 TUN（仅 Windows）；使用普通代理接管", "TUN unsupported on this OS (Windows only); using normal proxy takeover")
	default:
		return i18n.T("TUN 未开启，官方端点可能泄漏；可选开启防火墙严格模式", "TUN off, official endpoints may leak; optional firewall strict mode available")
	}
}

// Manager 管理 TUN 的配置落盘与启停。它从不改动普通代理接管，启停失败只会把状态标成 Failed。
type Manager struct {
	mu    sync.Mutex
	dir   string
	state State
	last  i18n.Text
}

// NewManager 用数据目录（存放 sing-box 可执行文件、配置、日志、pid）构造 Manager。
func NewManager(dir string) *Manager {
	st := StateOff
	if runtime.GOOS != "windows" {
		st = StateUnsupported
	}
	return &Manager{dir: dir, state: st}
}

// Status 返回当前状态和最近一次的说明文本。
func (m *Manager) Status() (State, i18n.Text) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.last
}

// BinaryPath/ConfigPath/LogPath/pidPath 是数据目录下的固定文件名。
func (m *Manager) BinaryPath() string { return filepath.Join(m.dir, "sing-box"+exeSuffix()) }
func (m *Manager) ConfigPath() string { return filepath.Join(m.dir, "singbox-tun.json") }
func (m *Manager) LogPath() string    { return filepath.Join(m.dir, "singbox-tun.log") }
func (m *Manager) pidPath() string    { return filepath.Join(m.dir, "singbox-tun.pid") }

// WriteConfig 生成配置并以 0600 写入数据目录。LogPath 固定指向数据目录下的日志。
func (m *Manager) WriteConfig(o Options) error {
	o.LogPath = m.LogPath()
	b, err := Config(o)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.ConfigPath(), b, 0o600)
}

func (m *Manager) setState(s State, t i18n.Text) {
	m.mu.Lock()
	m.state, m.last = s, t
	m.mu.Unlock()
}

// Enable 生成配置并（在 Windows 上，经 UAC）启动 TUN。任何失败都返回错误并把状态置为 Failed，
// 调用方据此保留普通代理接管、给出“TUN 未开启，官方端点可能泄漏”的提示，绝不阻断启动。
func (m *Manager) Enable(o Options) error {
	if runtime.GOOS != "windows" {
		m.setState(StateUnsupported, StateUnsupported.Text())
		return i18n.E("当前系统不支持 TUN，仅 Windows 可用", "TUN is only supported on Windows")
	}
	if err := m.WriteConfig(o); err != nil {
		m.setState(StateFailed, i18n.Raw(err.Error()))
		return err
	}
	m.setState(StateStarting, StateStarting.Text())
	if err := startTunnel(m); err != nil {
		m.setState(StateFailed, StateFailed.Text())
		return err
	}
	m.setState(StateOn, StateOn.Text())
	return nil
}

// Disable 停止 TUN（移除路由）。失败也只记录，不影响普通代理接管。
func (m *Manager) Disable() error {
	if runtime.GOOS != "windows" {
		m.setState(StateUnsupported, StateUnsupported.Text())
		return nil
	}
	err := stopTunnel(m)
	m.setState(StateOff, StateOff.Text())
	return err
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
