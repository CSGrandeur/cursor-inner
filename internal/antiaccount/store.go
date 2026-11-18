package antiaccount

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/i18n"
)

// Store 把账号存在数据目录的 antigravity-accounts.json（与 config.json 同级，含
// refresh token，权限 0600）。CurrentID 是用户最近一次切到的账号，只作展示标记；
// IDE 实际用哪个账号以凭据写入结果为准。
type Store struct {
	mu   sync.Mutex
	path string
	data storeData
}

type storeData struct {
	Accounts  []Account `json:"accounts"`
	CurrentID string    `json:"current_id,omitempty"`
}

func LoadStore(dir string) (*Store, error) {
	path := filepath.Join(dir, "antigravity-accounts.json")
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return s, nil
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, i18n.Wrap("反重力账号文件读不出来：", "Cannot read the Antigravity accounts file: ", err)
	}
	return s, nil
}

func (s *Store) Dir() string {
	if s == nil || s.path == "" {
		return ""
	}
	return filepath.Dir(s.path)
}

func (s *Store) persist() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := fsutil.WriteFile(s.path, raw); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

// List 返回账号快照（含 token，调用方不得把 token 发给页面或日志）。
func (s *Store) List() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Account(nil), s.data.Accounts...)
}

func (s *Store) CurrentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.CurrentID
}

func (s *Store) get(id string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(id)
}

func (s *Store) find(id string) (Account, bool) {
	for _, a := range s.data.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// upsert 按邮箱合并：同一邮箱视为同一账号，保留 ID、Name、CreatedAt，替换 token。
func (s *Store) upsert(email, name string, token Token) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = normalizeEmail(email)
	now := time.Now().Unix()
	for i := range s.data.Accounts {
		if normalizeEmail(s.data.Accounts[i].Email) != email {
			continue
		}
		a := &s.data.Accounts[i]
		a.Token = token
		if name != "" {
			a.Name = name
		}
		a.Status = ""
		a.LastUsed = now
		if err := s.persist(); err != nil {
			return Account{}, err
		}
		return *a, nil
	}
	a := Account{ID: newID(), Email: email, Name: name, Token: token, CreatedAt: now, LastUsed: now}
	s.data.Accounts = append(s.data.Accounts, a)
	if err := s.persist(); err != nil {
		return Account{}, err
	}
	return a, nil
}

// saveToken 只更新一个账号的 token（刷新或切换前续期后回写）。
func (s *Store) saveToken(id string, token Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].ID == id {
			s.data.Accounts[i].Token = token
			return s.persist()
		}
	}
	return i18n.E("没有这个账号", "No such account")
}

// saveQuotaAndToken 更新账号的 token、订阅配额快照与状态并持久化。
func (s *Store) saveQuotaAndToken(id string, token Token, quota *QuotaInfo, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].ID == id {
			if token.AccessToken != "" || token.RefreshToken != "" {
				s.data.Accounts[i].Token = token
			}
			if quota != nil {
				copied := *quota
				copied.Buckets = append([]QuotaBucket(nil), quota.Buckets...)
				s.data.Accounts[i].Quota = &copied
			}
			s.data.Accounts[i].Status = status
			return s.persist()
		}
	}
	return i18n.E("没有这个账号", "No such account")
}

func (s *Store) setStatus(id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].ID == id {
			if s.data.Accounts[i].Status == status {
				return nil
			}
			s.data.Accounts[i].Status = status
			return s.persist()
		}
	}
	return nil
}

func (s *Store) setCurrent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].ID == id {
			s.data.Accounts[i].LastUsed = time.Now().Unix()
			s.data.CurrentID = id
			return s.persist()
		}
	}
	return i18n.E("没有这个账号", "No such account")
}

func (s *Store) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]Account, 0, len(s.data.Accounts))
	found := false
	for _, a := range s.data.Accounts {
		if a.ID == id {
			found = true
			continue
		}
		next = append(next, a)
	}
	if !found {
		return i18n.E("没有这个账号", "No such account")
	}
	s.data.Accounts = next
	if s.data.CurrentID == id {
		s.data.CurrentID = ""
	}
	return s.persist()
}
