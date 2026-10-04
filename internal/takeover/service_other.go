//go:build !windows

package takeover

import "errors"

func (s *Service) Enable() error {
	return errors.New("接管只在 Windows 上执行")
}

func (s *Service) Stop() error {
	return errors.New("接管只在 Windows 上执行")
}

func (s *Service) ClearStale() bool { return false }

func (s *Service) Restore() error {
	if !Marked(s.dir) {
		return nil
	}
	Unmark(s.dir)
	return nil
}

func Recover(dir string) error {
	if Marked(dir) {
		Unmark(dir)
	}
	return nil
}
