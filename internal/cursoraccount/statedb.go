package cursoraccount

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"

	"cursor-inner/internal/i18n"
)

// Cursor 桌面端 state.vscdb（SQLite ItemTable）中与登录态、会员归属相关的主键。
const (
	cursorAuthPrefix       = "cursorAuth/"
	keyAccessToken         = cursorAuthPrefix + "accessToken"
	keyRefreshToken        = cursorAuthPrefix + "refreshToken"
	keyCachedEmail         = cursorAuthPrefix + "cachedEmail"
	keyCachedSignUpType    = cursorAuthPrefix + "cachedSignUpType"
	keyCachedScopedProfile = cursorAuthPrefix + "cachedScopedProfile"
	keyStripeType          = cursorAuthPrefix + "stripeMembershipType"
	keyStripeAuthID        = cursorAuthPrefix + "stripeMembershipAuthId"
	keyStripeStatus        = cursorAuthPrefix + "stripeSubscriptionStatus"
	keyUnifiedV2           = cursorAuthPrefix + "hasInvalidatedMembershipForUnifiedSemanticsV2"
	keyCachedTeam          = cursorAuthPrefix + "cachedTeam"
	keyTeamID              = cursorAuthPrefix + "teamId"
	keyStripeCustomer      = cursorAuthPrefix + "stripeCustomerId"
	keyXAISession          = cursorAuthPrefix + "xaiBrowserSession"
)

// StateDBPath 返回当前平台默认 Cursor state.vscdb 路径。
func StateDBPath() string {
	home, _ := os.UserHomeDir()
	return stateDBPathFor(runtime.GOOS, home, os.Getenv("APPDATA"))
}

func stateDBPathFor(goos, home, appdata string) string {
	switch goos {
	case "windows":
		base := appdata
		if base == "" && home != "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		if base == "" {
			return ""
		}
		return filepath.Join(base, "Cursor", "User", "globalStorage", "state.vscdb")
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")
	}
}

func candidateStateDBPaths(goos, home, appdata, exePath string) []string {
	var raw []string
	if strings.TrimSpace(exePath) != "" {
		raw = append(raw, filepath.Join(filepath.Dir(exePath), "data", "user-data", "User", "globalStorage", "state.vscdb"))
	}
	if p := stateDBPathFor(goos, home, appdata); p != "" {
		raw = append(raw, p)
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func hostCandidateStateDBPaths(exePath string) []string {
	home, _ := os.UserHomeDir()
	return candidateStateDBPaths(runtime.GOOS, home, os.Getenv("APPDATA"), exePath)
}

// LatestActivityNano 返回本机 Cursor 状态库及 WAL 的最新修改时间（Unix 纳秒）。
func LatestActivityNano(exePath string) int64 {
	var best int64
	bump := func(p string) {
		if p == "" {
			return
		}
		if fi, err := os.Stat(p); err == nil {
			if ns := fi.ModTime().UnixNano(); ns > best {
				best = ns
			}
		}
	}
	for _, db := range hostCandidateStateDBPaths(exePath) {
		bump(db)
		bump(db + "-wal")
	}
	return best
}

// userDataDirFromDB 从 .../Cursor/User/globalStorage/state.vscdb 推回含 Local State 的用户目录。
func userDataDirFromDB(dbPath string) string {
	dir := filepath.Dir(filepath.Dir(filepath.Dir(dbPath)))
	if dir == "." || dir == "" {
		return ""
	}
	return dir
}

func openStateDB(path string, queryOnly bool) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec(`PRAGMA busy_timeout=1500`); err != nil {
		_ = d.Close()
		return nil, err
	}
	if queryOnly {
		_, _ = d.Exec(`PRAGMA query_only=ON`)
	}
	var name string
	if err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ItemTable'`).Scan(&name); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("state.vscdb missing ItemTable: %w", err)
	}
	return d, nil
}

func readItemText(db *sql.DB, key string) string {
	var raw []byte
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key=?`, key).Scan(&raw); err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// LiveEmailFromDB 只读正在运行或已退出 Cursor 的 cachedEmail 与 accessToken（避免解密，无子进程）。
func LiveEmailFromDB(dbPath string) string {
	db, err := openStateDB(dbPath, true)
	if err != nil {
		return ""
	}
	defer db.Close()
	if email := normalizeEmail(readItemText(db, keyCachedEmail)); email != "" {
		if readItemText(db, keyRefreshToken) != "" || readItemText(db, keyAccessToken) != "" {
			return email
		}
	}
	if tok := readItemText(db, keyAccessToken); tok != "" && !strings.HasPrefix(tok, safeStoragePrefix) {
		_, email, _ := parseJWTClaims(tok)
		return email
	}
	return ""
}

// CurrentFromState 从 Cursor state.vscdb 读出当前登录凭据、邮箱与显示名（含 safeStorage 解密）。
func CurrentFromState(dbPath string) (a Account, ok bool, err error) {
	db, err := openStateDB(dbPath, true)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Account{}, false, nil
		}
		return Account{}, false, err
	}
	defer db.Close()

	udDir := userDataDirFromDB(dbPath)
	rawAcc := readItemText(db, keyAccessToken)
	rawRef := readItemText(db, keyRefreshToken)
	accTok, errAcc := decodeStoredToken(udDir, rawAcc)
	refTok, errRef := decodeStoredToken(udDir, rawRef)
	if errAcc != nil && errRef != nil {
		return Account{}, false, i18n.Wrap("解不开 Cursor 当前登录凭据：", "Cannot decrypt Cursor's current credentials: ", errAcc)
	}
	if refTok == "" {
		refTok = accTok
	}
	if accTok == "" {
		accTok = refTok
	}
	if refTok == "" && accTok == "" {
		return Account{}, false, nil
	}

	sub, jwtEmail, exp := parseJWTClaims(accTok)
	if sub == "" {
		sub, _, _ = parseJWTClaims(refTok)
	}
	email := normalizeEmail(readItemText(db, keyCachedEmail))
	if email == "" {
		email = jwtEmail
	}
	signUp := readItemText(db, keyCachedSignUpType)
	var displayName string
	if prof := readItemText(db, keyCachedScopedProfile); prof != "" {
		var p struct {
			AuthID      string `json:"authId"`
			DisplayName string `json:"displayName"`
		}
		if json.Unmarshal([]byte(prof), &p) == nil {
			displayName = strings.TrimSpace(p.DisplayName)
			if sub == "" && p.AuthID != "" {
				sub = strings.TrimSpace(p.AuthID)
			}
		}
	}
	var quota *QuotaInfo
	if tierRaw := strings.TrimSpace(readItemText(db, keyStripeType)); tierRaw != "" {
		if t := normalizeStripeTier(tierRaw); t != "" {
			quota = &QuotaInfo{Tier: t}
		}
	}
	return Account{
		Email:      email,
		Name:       displayName,
		AuthID:     sub,
		SignUpType: signUp,
		Token: Token{
			AccessToken:  accTok,
			RefreshToken: refTok,
			Expiry:       exp,
		},
		Quota: quota,
	}, true, nil
}

// injectAccount 在 Cursor 已退出后把目标账号写入 state.vscdb：
// 先收拢 WAL 并复制为 state.vscdb.cursor-inner-bak，再更新 cursorAuth/* 键并清除跨账号残留。
func injectAccount(dbPath string, a Account) error {
	db, err := openStateDB(dbPath, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return i18n.E("找不到 Cursor 的状态库。先打开一次 Cursor 并登录，再在这里切换账号。", "Cursor's state database was not found. Open Cursor and sign in once, then switch accounts here.")
		}
		return i18n.Wrap("读取 Cursor state.vscdb 失败：", "Reading Cursor state.vscdb failed: ", err)
	}
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	_ = db.Close()

	if err := copyFileMode(dbPath, dbPath+".cursor-inner-bak", 0o600); err != nil {
		return i18n.Wrap("备份 Cursor state.vscdb 失败：", "Backing up Cursor state.vscdb failed: ", err)
	}

	db, err = openStateDB(dbPath, false)
	if err != nil {
		return i18n.Wrap("打开 Cursor state.vscdb 失败：", "Opening Cursor state.vscdb failed: ", err)
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return i18n.Wrap("写入 Cursor state.vscdb 失败：", "Writing Cursor state.vscdb failed: ", err)
	}

	udDir := userDataDirFromDB(dbPath)
	accVal := encodeStoredToken(udDir, a.Token.AccessToken)
	refPlain := a.Token.RefreshToken
	if refPlain == "" {
		refPlain = a.Token.AccessToken
	}
	refVal := encodeStoredToken(udDir, refPlain)

	signUp := strings.TrimSpace(a.SignUpType)
	if signUp == "" {
		signUp = "Auth_0"
	}
	sub := strings.TrimSpace(a.AuthID)
	if sub == "" {
		sub, _, _ = parseJWTClaims(a.Token.AccessToken)
	}

	writeErr := func() error {
		if err := upsertText(tx, keyAccessToken, accVal); err != nil {
			return err
		}
		if err := upsertText(tx, keyRefreshToken, refVal); err != nil {
			return err
		}
		if err := upsertText(tx, keyCachedEmail, normalizeEmail(a.Email)); err != nil {
			return err
		}
		if err := upsertText(tx, keyCachedSignUpType, signUp); err != nil {
			return err
		}
		if err := upsertText(tx, keyUnifiedV2, "true"); err != nil {
			return err
		}
		if sub != "" && a.Name != "" {
			prof, _ := json.Marshal(struct {
				AuthID      string `json:"authId"`
				DisplayName string `json:"displayName"`
			}{AuthID: sub, DisplayName: a.Name})
			if err := upsertText(tx, keyCachedScopedProfile, string(prof)); err != nil {
				return err
			}
		} else if err := deleteItemKey(tx, keyCachedScopedProfile); err != nil {
			return err
		}
		if sub != "" {
			if err := upsertText(tx, keyStripeAuthID, sub); err != nil {
				return err
			}
		} else if err := deleteItemKey(tx, keyStripeAuthID); err != nil {
			return err
		}
		if a.Quota != nil && a.Quota.Tier != "" {
			if err := upsertText(tx, keyStripeType, stripeTierLower(a.Quota.Tier)); err != nil {
				return err
			}
			if a.Quota.Tier != "FREE" {
				if err := upsertText(tx, keyStripeStatus, "active"); err != nil {
					return err
				}
			} else if err := deleteItemKey(tx, keyStripeStatus); err != nil {
				return err
			}
		} else {
			if err := deleteItemKey(tx, keyStripeType); err != nil {
				return err
			}
			if err := deleteItemKey(tx, keyStripeStatus); err != nil {
				return err
			}
		}
		for _, stale := range []string{keyCachedTeam, keyTeamID, keyStripeCustomer, keyXAISession} {
			if err := deleteItemKey(tx, stale); err != nil {
				return err
			}
		}
		return nil
	}()
	if writeErr != nil {
		_ = tx.Rollback()
		return writeErr
	}
	if err := tx.Commit(); err != nil {
		return i18n.Wrap("提交 Cursor state.vscdb 失败：", "Committing Cursor state.vscdb failed: ", err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return i18n.Wrap("收拢 Cursor state.vscdb 失败：", "Checkpointing Cursor state.vscdb failed: ", err)
	}
	return nil
}

func upsertText(tx *sql.Tx, key, value string) error {
	if _, err := tx.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		return i18n.Wrap("写入 Cursor state.vscdb 失败：", "Writing Cursor state.vscdb failed: ", err)
	}
	return nil
}

func deleteItemKey(tx *sql.Tx, key string) error {
	if _, err := tx.Exec(`DELETE FROM ItemTable WHERE key=?`, key); err != nil {
		return i18n.Wrap("写入 Cursor state.vscdb 失败：", "Writing Cursor state.vscdb failed: ", err)
	}
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
