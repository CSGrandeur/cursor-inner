package takeover

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cursor-inner/internal/i18n"

	"cursor-inner/internal/cursorsettings"
	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/mitm"
	"cursor-inner/internal/procfwd"
)

type Service struct {
	dir  string
	mitm *mitm.Server

	mu          sync.Mutex
	active      bool
	catchDirect bool
	url         string
	ca          string
	detail      i18n.Text
	problem     i18n.Text
}

func New(dir string, proxy *mitm.Server) *Service {
	return &Service{dir: dir, mitm: proxy, ca: "missing"}
}

type Snapshot struct {
	Active  bool
	URL     string
	CA      string
	Detail  i18n.Text
	Problem i18n.Text
	Warning i18n.Text
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Active:  s.active && s.mitm.Running(),
		URL:     s.url,
		CA:      s.ca,
		Detail:  s.detail,
		Problem: s.problem,
		Warning: s.mitm.Warning(),
	}
}

// ProxyOnly 只在数据目录里启动本机代理，不改 Cursor 的设置，也不结束 Cursor。
func (s *Service) ProxyOnly() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cert, err := EnsureCA(s.dir)
	if err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	url, err := s.mitm.Start(cert)
	if err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	s.active = true
	s.url = url
	s.ca = "debug"
	s.problem = i18n.Text{}
	return nil
}

// ProxyOnlyAt 和 ProxyOnly 相同，但在指定地址上监听（调试实例的自更新交接用）。
func (s *Service) ProxyOnlyAt(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cert, err := EnsureCA(s.dir)
	if err != nil {
		return err
	}
	url, err := s.mitm.StartAt(cert, addr)
	if err != nil {
		return err
	}
	s.active, s.url, s.ca, s.problem = true, url, "debug", i18n.Text{}
	return nil
}

func (s *Service) Enable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cert, err := EnsureCA(s.dir)
	if err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	certPath, _ := CertPaths(s.dir)
	s.ca, s.detail = InstallCA(certPath, cert.Leaf)
	url, err := s.mitm.Start(cert)
	if err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	if err := saveOriginal(s.dir); err != nil {
		s.mitm.Stop()
		s.problem = i18n.Of(err)
		return err
	}
	if err := writeSettings(cursorsettings.Apply, url); err != nil {
		s.mitm.Stop()
		s.problem = i18n.Of(err)
		return err
	}
	if err := MarkURL(s.dir, url); err != nil {
		_ = restoreSettings(s.dir)
		s.mitm.Stop()
		s.problem = i18n.Of(err)
		return err
	}
	s.active = true
	s.catchDirect = true
	s.url = url
	s.problem = i18n.Text{}
	s.mitm.SyncDirect()
	if err := TerminateCursor(); err != nil {
		s.problem = i18n.Of(i18n.Wrap("设置已写入，但没能结束 Cursor：", "Settings were written, but Cursor could not be closed: ", err))
	}
	return nil
}

// Adopt 在自更新交接中接回上一个进程的接管：在同一地址上启动本机代理，
// 不改 Cursor 设置、不结束 Cursor（设置里写的就是这个地址）。地址被占用或设置对不上时报错，
// 交给旧进程回滚，绝不留下一个指向空端口的接管。
func (s *Service) Adopt(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !Marked(s.dir) {
		return i18n.E("上一个进程没有处于接管状态", "The previous process was not in takeover")
	}
	cert, err := EnsureCA(s.dir)
	if err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	certPath, _ := CertPaths(s.dir)
	s.ca, s.detail = InstallCA(certPath, cert.Leaf)
	url, err := s.mitm.StartAt(cert, addr)
	if err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	if want := currentSettingsProxy(); want != "" && want != url {
		s.mitm.Stop()
		return i18n.E("Cursor 设置里的代理地址与交接地址不一致："+want, "Cursor's proxy setting does not match the handover address: "+want)
	}
	s.active = true
	s.catchDirect = true
	s.url = url
	s.problem = i18n.Text{}
	return nil
}

func currentSettingsProxy() string {
	path, err := settingsPath()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	proxy, _ := doc["http.proxy"].(string)
	return proxy
}

func (s *Service) SyncDirect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.catchDirect {
		return
	}
	s.mitm.SyncDirect()
}

func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.catchDirect = false
	s.mitm.Stop()
	s.active = false
	s.url = ""
	if err := rollback(s.dir); err != nil {
		s.problem = i18n.Of(err)
		return err
	}
	s.problem = i18n.Text{}
	return nil
}

func (s *Service) Restore() error {
	if !Marked(s.dir) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.mitm.Stop()
		s.active = false
		s.url = ""
		return nil
	}
	return s.Stop()
}

func Recover(dir string) error {
	if !Marked(dir) {
		return nil
	}
	return rollback(dir)
}

func rollback(dir string) error {
	// 当前接管不写 hosts；Restore 只清旧版本可能留下的标记。
	hostErr := procfwd.Restore()
	err := restoreSettings(dir)
	killErr := TerminateCursor()
	if err == nil && killErr == nil {
		Unmark(dir)
		return hostErr
	}
	if err == nil {
		return killErr
	}
	return err
}

func (s *Service) ClearStale() bool {
	path, err := settingsPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	proxy, _ := doc["http.proxy"].(string)
	support, _ := doc["http.proxySupport"].(string)
	if support == "override" && (strings.HasPrefix(proxy, "http://127.0.0.1:") || strings.HasPrefix(proxy, "http://localhost:")) {
		_ = s.Stop()
		return true
	}
	return false
}

func backupPath(dir string) string {
	return filepath.Join(dir, "cursor-settings.backup.json")
}

func saveOriginal(dir string) error {
	if _, err := os.Stat(backupPath(dir)); err == nil {
		return nil
	}
	path, err := settingsPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	original, err := cursorsettings.Snapshot(raw)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(backupPath(dir), append(data, '\n'))
}

func restoreSettings(dir string) error {
	var original map[string]json.RawMessage
	if raw, err := os.ReadFile(backupPath(dir)); err == nil {
		if err := json.Unmarshal(raw, &original); err != nil {
			return i18n.Wrap("Cursor 设置备份无法读取：", "Cannot read the Cursor settings backup: ", err)
		}
	}
	err := writeSettings(func(doc []byte, _ string) ([]byte, error) {
		return cursorsettings.Clear(doc, original)
	}, "")
	if err == nil {
		_ = os.Remove(backupPath(dir))
	}
	return err
}

func writeSettings(edit func([]byte, string) ([]byte, error), proxyURL string) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next, err := edit(raw, proxyURL)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, next)
}

// StartKind 区分首次接管和已接管恢复。
type StartKind string

const (
	StartEnabled StartKind = "enabled" // 首次写入设置，可能结束 Cursor
	StartResumed StartKind = "resumed" // 在原地址恢复，不改设置、不结束 Cursor
)

// ClassifyStartup 判断冷启动应恢复已有接管，还是做首次 Enable。
// 已有 takeover.on 时绝不返回 StartEnabled：回退到 Enable 会换新端口并结束 Cursor。
func ClassifyStartup(dir string) (StartKind, string, error) {
	if !Marked(dir) {
		return StartEnabled, "", nil
	}
	addr := resumeAddr(dir)
	if addr == "" {
		return "", "", i18n.E("发现上次的接管标记，但无法确定原来的本机代理地址，已停止自动首次接管，以免结束 Cursor 或换端口", "A previous takeover mark was found, but the original local proxy address could not be determined; automatic first-time takeover was skipped so Cursor is not closed and the port is not changed")
	}
	return StartResumed, addr, nil
}

func resumeAddr(dir string) string {
	if addr := loopbackHost(currentSettingsProxy()); addr != "" {
		if marked := loopbackHost(MarkedProxy(dir)); marked != "" && marked != addr {
			return ""
		}
		return addr
	}
	return loopbackHost(MarkedProxy(dir))
}

func loopbackHost(proxy string) string {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return ""
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" {
		if strings.HasPrefix(proxy, "127.0.0.1:") || strings.HasPrefix(proxy, "localhost:") || strings.HasPrefix(proxy, "[::1]:") {
			return proxy
		}
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "" {
		return ""
	}
	host := u.Hostname()
	if host != "127.0.0.1" && !strings.EqualFold(host, "localhost") && host != "::1" {
		return ""
	}
	return u.Host
}

// ResumeOrEnable 在已接管时于原地址 Adopt（不改 Cursor 设置、不结束 Cursor）；
// 否则走首次 Enable。Adopt 失败不会回退到 Enable。
func (s *Service) ResumeOrEnable() (StartKind, error) {
	kind, addr, err := ClassifyStartup(s.dir)
	if err != nil {
		return "", err
	}
	if kind == StartResumed {
		if err := s.Adopt(addr); err != nil {
			return "", err
		}
		return StartResumed, nil
	}
	if err := s.Enable(); err != nil {
		return "", err
	}
	return StartEnabled, nil
}
