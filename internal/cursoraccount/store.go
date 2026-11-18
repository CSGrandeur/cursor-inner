package cursoraccount

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

// Store 把 Cursor 账号保存在数据目录的 cursor-accounts.json（权限 0600）。
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
	path := filepath.Join(dir, "cursor-accounts.json")
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
		return nil, i18n.Wrap("Cursor 账号文件读不出来：", "Cannot read the Cursor accounts file: ", err)
	}
	return s, nil
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
	for _, a := range s.data.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

func (s *Store) upsert(email, name, authID, signUpType string, token Token) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email = normalizeEmail(email)
	now := time.Now().Unix()
	for i := range s.data.Accounts {
		a := &s.data.Accounts[i]
		if (email != "" && normalizeEmail(a.Email) != email) && (authID == "" || a.AuthID != authID) {
			continue
		}
		if email != "" {
			a.Email = email
		}
		if name != "" {
			a.Name = name
		}
		if authID != "" {
			a.AuthID = authID
		}
		if signUpType != "" {
			a.SignUpType = signUpType
		}
		a.Token = token
		a.Status = ""
		a.LastUsed = now
		if err := s.persist(); err != nil {
			return Account{}, err
		}
		return *a, nil
	}
	a := Account{
		ID:         newID(),
		Email:      email,
		Name:       name,
		AuthID:     authID,
		SignUpType: signUpType,
		Token:      token,
		CreatedAt:  now,
		LastUsed:   now,
	}
	s.data.Accounts = append(s.data.Accounts, a)
	if err := s.persist(); err != nil {
		return Account{}, err
	}
	return a, nil
}

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

func (s *Store) saveMetaQuota(id, email, name, authID, signUpType string, token Token, quota *QuotaInfo, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Accounts {
		if s.data.Accounts[i].ID != id {
			continue
		}
		a := &s.data.Accounts[i]
		if em := normalizeEmail(email); em != "" {
			a.Email = em
		}
		if name != "" {
			a.Name = name
		}
		if authID != "" {
			a.AuthID = authID
		}
		if signUpType != "" {
			a.SignUpType = signUpType
		}
		if token.AccessToken != "" || token.RefreshToken != "" {
			a.Token = token
		}
		if quota != nil {
			copied := *quota
			copied.Buckets = append([]QuotaBucket(nil), quota.Buckets...)
			a.Quota = &copied
		}
		a.Status = status
		return s.persist()
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
