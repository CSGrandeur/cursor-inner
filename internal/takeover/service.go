package takeover

import (
	"encoding/json"
	"errors"
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
	if err := Mark(s.dir); err != nil {
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
