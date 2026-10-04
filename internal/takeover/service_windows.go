//go:build windows

package takeover

import (
	"errors"
	"os"
	"strings"

	"encoding/json"

	"cursor-inner/internal/cursorsettings"
	"cursor-inner/internal/fsutil"
)

func (s *Service) Enable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cert, err := EnsureCA(s.dir)
	if err != nil {
		s.problem = err.Error()
		return err
	}
	certPath, _ := CertPaths(s.dir)
	s.ca, s.detail = InstallCA(certPath, cert.Leaf)
	url, err := s.mitm.Start(cert)
	if err != nil {
		s.problem = err.Error()
		return err
	}
	if err := writeSettings(cursorsettings.Apply, url); err != nil {
		s.mitm.Stop()
		s.problem = err.Error()
		return err
	}
	if err := Mark(s.dir); err != nil {
		_ = writeSettings(func(doc []byte, _ string) ([]byte, error) {
			return cursorsettings.Clear(doc)
		}, "")
		s.mitm.Stop()
		s.problem = err.Error()
		return err
	}
	s.active = true
	s.url = url
	s.problem = ""
	if err := TerminateCursor(); err != nil {
		s.problem = "设置已写入，但没能结束 Cursor：" + err.Error()
	}
	return nil
}

func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mitm.Stop()
	s.active = false
	s.url = ""
	if err := rollback(s.dir); err != nil {
		s.problem = err.Error()
		return err
	}
	s.problem = ""
	return nil
}

func (s *Service) Restore() error {
	if !Marked(s.dir) {
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
	err := writeSettings(func(doc []byte, _ string) ([]byte, error) {
		return cursorsettings.Clear(doc)
	}, "")
	killErr := TerminateCursor()
	if err == nil && killErr == nil {
		Unmark(dir)
		return nil
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
