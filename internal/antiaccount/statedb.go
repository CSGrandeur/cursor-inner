package antiaccount

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/i18n"
)

// IDE 状态库里的键。oauthToken 是登录凭据；userStatus 是账号身份；
// onboarding 标记跳过首次引导；agentManagerInitState 是旧版残留，带着旧 UserID
// 会让会话历史拉不出来，注入时删掉（值为空串即视为删除——见 injectAccount）。
const (
	keyUnifiedOAuth = "antigravityUnifiedStateSync.oauthToken"
	keyUserStatus   = "antigravityUnifiedStateSync.userStatus"
	keyOnboarding   = "antigravityOnboarding"
	keyLegacyInit   = "jetskiStateSync.agentManagerInitState"
	keyEnterprise   = "antigravityUnifiedStateSync.enterprisePreferences"
)

// StateDBPath 按平台返回反重力 IDE 的 state.vscdb 路径。home 为空时用当前用户的。
// 找不到文件也返回默认路径（还没登录过的机器上库可能尚不存在）。
func StateDBPath() string {
	home, _ := os.UserHomeDir()
	return stateDBPathFor(runtime.GOOS, home, os.Getenv("APPDATA"))
}

func stateDBPathFor(goos, home, appdata string) string {
	return stateDBPathForFolder(goos, home, appdata, "Antigravity")
}

func stateDBPathForFolder(goos, home, appdata, folder string) string {
	switch goos {
	case "windows":
		base := appdata
		if base == "" && home != "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		if base == "" {
			return ""
		}
		return filepath.Join(base, folder, "User", "globalStorage", "state.vscdb")
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", folder, "User", "globalStorage", "state.vscdb")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", folder, "User", "globalStorage", "state.vscdb")
	}
}

func candidateStateDBPaths(goos, home, appdata, exePath string) []string {
	var raw []string
	if strings.TrimSpace(exePath) != "" {
		raw = append(raw, filepath.Join(filepath.Dir(exePath), "data", "user-data", "User", "globalStorage", "state.vscdb"))
	}
	for _, folder := range []string{"Antigravity", "Antigravity IDE"} {
		if p := stateDBPathForFolder(goos, home, appdata, folder); p != "" {
			raw = append(raw, p)
		}
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

// LatestActivityNano 返回本机反重力状态库或凭据文件的最新修改时间（Unix 纳秒）；用于排序冷启动兜底。
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
	for _, dir := range []string{geminiDir()} {
		bump(filepath.Join(dir, "jetski-standalone-oauth-token"))
	}
	if _, wslGem := activeWSLGeminiDir(); wslGem != "" {
		bump(filepath.Join(wslGem, "jetski-standalone-oauth-token"))
	}
	return best
}

// CurrentFromDB 读 IDE 状态库，返回当前登录的账号身份。库不存在（还没装或还没
// 登录过）返回 ok=false 且 err=nil；库在但解不出 token 同样 ok=false。
func CurrentFromDB(path string) (email, name string, token Token, ok bool, err error) {
	db, err := readStateDB(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", Token{}, false, nil
		}
		return "", "", Token{}, false, err
	}
	defer db.Close()
	raw, found := db.Get(keyUnifiedOAuth)
	if !found {
		return "", "", Token{}, false, nil
	}
	topic, derr := base64.StdEncoding.DecodeString(string(raw))
	if derr != nil {
		return "", "", Token{}, false, nil
	}
	for _, entry := range parseTopic(topic) {
		if entry.key != oauthSentinelKey {
			continue
		}
		if t, valid := parseOAuthInfo(entry.payload); valid {
			token = t
			ok = true
		}
	}
	if !ok {
		return "", "", Token{}, false, nil
	}
	if raw, found := db.Get(keyUserStatus); found {
		if topic, derr := base64.StdEncoding.DecodeString(string(raw)); derr == nil {
			for _, entry := range parseTopic(topic) {
				if entry.key == userSentinelKey {
					name, email = parseUserStatus(entry.payload)
				}
			}
		}
	}
	return email, name, token, true, nil
}

// injectAccount 把账号凭据写进 IDE 状态库。调用方必须保证 IDE 已退出。
// 缺键会补上（oauthToken、userStatus、onboarding）；旧版 agentManagerInitState
// 和上一个账号留下的企业项目偏好会删掉，避免会话历史和企业项目串号。
func injectAccount(path string, a Account) error {
	db, err := readStateDB(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return i18n.E("找不到反重力的状态库。先打开一次反重力并登录，再在这里切换账号。", "Antigravity's state database was not found. Open Antigravity and sign in once, then switch accounts here.")
		}
		return err
	}
	raw, _ := db.Get(keyUnifiedOAuth)
	if err := snapshotStateDB(db); err != nil {
		return err
	}
	db, err = readStateDB(path)
	if err != nil {
		return err
	}
	// DecodeString 在非法输入上也会返回已经解开的前缀。忽略错误会把半截字节留在 Topic 里。
	var topic []byte
	if dec, err := base64.StdEncoding.DecodeString(string(raw)); err == nil {
		topic = dec
	}
	name := a.Name
	if name == "" {
		name = a.Email
	}
	oauthValue := []byte(base64.StdEncoding.EncodeToString(topicWithEntry(topic, oauthSentinelKey, oauthInfo(a.Token))))
	statusValue := []byte(base64.StdEncoding.EncodeToString(topicWithEntry(nil, userSentinelKey, userStatusPayload(name, a.Email))))
	tx, err := db.db.Begin()
	if err != nil {
		_ = db.Close()
		return i18n.Wrap("写入 state.vscdb 失败：", "Writing state.vscdb failed: ", err)
	}
	err = writeAccountRows(tx, oauthValue, statusValue)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		return i18n.Wrap("写入 state.vscdb 失败：", "Writing state.vscdb failed: ", err)
	}
	return writeStateDB(path, db)
}

func writeAccountRows(tx *sql.Tx, oauthValue, statusValue []byte) error {
	if err := upsertKey(tx, keyUnifiedOAuth, oauthValue); err != nil {
		return err
	}
	if err := upsertKey(tx, keyUserStatus, statusValue); err != nil {
		return err
	}
	if err := upsertKey(tx, keyOnboarding, []byte("true")); err != nil {
		return err
	}
	if err := deleteKey(tx, keyLegacyInit); err != nil {
		return err
	}
	return deleteKey(tx, keyEnterprise)
}

// geminiDir 是语言服务的文件凭据回退目录。
func geminiDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

// writeFallbackFile 写本机 ~/.gemini/jetski-standalone-oauth-token。
func writeFallbackFile(a Account) error {
	return writeFallbackFileToDir(geminiDir(), a)
}

// writeFallbackFileToDir 把 OAuth 凭据写入指定 .gemini 目录的 jetski-standalone-oauth-token，
// 保留原文件里已有的 writer_id。
func writeFallbackFileToDir(dir string, a Account) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "jetski-standalone-oauth-token")
	writerID := readWriterID(path)
	raw, err := json.MarshalIndent(buildKeyringDoc(a, writerID), "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := fsutil.WriteFile(path, raw); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func readWriterID(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		WriterID string `json:"writer_id"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return strings.TrimSpace(doc.WriterID)
}

type keyringTokenDoc struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

type keyringDoc struct {
	Token        keyringTokenDoc `json:"token"`
	AuthMethod   string          `json:"auth_method"`
	IDTokenEmail string          `json:"id_token_email,omitempty"`
	IDToken      string          `json:"id_token,omitempty"`
	WriterID     string          `json:"writer_id,omitempty"`
}

func buildKeyringDoc(a Account, writerID string) keyringDoc {
	expiry := a.Token.Expiry
	if expiry > 10_000_000_000 { // 毫秒容错
		expiry /= 1000
	}
	return keyringDoc{
		Token: keyringTokenDoc{
			AccessToken:  a.Token.AccessToken,
			TokenType:    "Bearer",
			RefreshToken: a.Token.RefreshToken,
			Expiry:       formatExpiry(expiry),
			ExpiresIn:    3599,
		},
		AuthMethod:   "consumer",
		IDTokenEmail: a.Email,
		IDToken:      a.Token.IDToken,
		WriterID:     writerID,
	}
}

// keyringPayload 是系统凭据库与文件回退共用的 JSON。expiry 用 RFC3339 微秒格式，
// auth_method 固定 consumer（个人 Google 账号）。
func keyringPayload(a Account) []byte {
	raw, _ := json.Marshal(buildKeyringDoc(a, ""))
	return raw
}

// readFallbackFileFromDir 从 .gemini 目录读取 jetski / agy / oauth_creds 凭据。
func readFallbackFileFromDir(dir string) (email string, t Token, ok bool) {
	if strings.TrimSpace(dir) == "" {
		return "", Token{}, false
	}
	for _, p := range []string{
		filepath.Join(dir, "jetski-standalone-oauth-token"),
		filepath.Join(dir, "antigravity-cli", "antigravity-oauth-token"),
	} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if e, tok, valid := parseKeyringPayload(raw); valid && tok.RefreshToken != "" {
			return e, tok, true
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "oauth_creds.json"))
	if err != nil {
		return "", Token{}, false
	}
	var creds struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		IDToken      string  `json:"id_token"`
		ExpiryDate   float64 `json:"expiry_date"`
		Expiry       string  `json:"expiry"`
	}
	if json.Unmarshal(raw, &creds) != nil || creds.RefreshToken == "" {
		return "", Token{}, false
	}
	exp := int64(creds.ExpiryDate)
	if exp > 10_000_000_000 {
		exp /= 1000
	}
	if exp == 0 && creds.Expiry != "" {
		exp = parseExpiry(creds.Expiry)
	}
	if acRaw, err := os.ReadFile(filepath.Join(dir, "google_accounts.json")); err == nil {
		var ga struct {
			Active string `json:"active"`
		}
		if json.Unmarshal(acRaw, &ga) == nil {
			email = strings.TrimSpace(ga.Active)
		}
	}
	return email, Token{
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		IDToken:      creds.IDToken,
		Expiry:       exp,
	}, true
}

func formatExpiry(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02T15:04:05.000000Z")
}

func parseExpiry(s string) int64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// parseKeyringPayload 从凭据库 JSON 里取出 token 与邮箱（导入当前账号用）。
func parseKeyringPayload(raw []byte) (email string, t Token, ok bool) {
	var p struct {
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Expiry       string `json:"expiry"`
		} `json:"token"`
		IDTokenEmail string `json:"id_token_email"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", Token{}, false
	}
	if p.Token.RefreshToken == "" && p.Token.AccessToken == "" {
		return "", Token{}, false
	}
	t = Token{
		AccessToken:  p.Token.AccessToken,
		RefreshToken: p.Token.RefreshToken,
		IDToken:      p.IDToken,
		Expiry:       parseExpiry(p.Token.Expiry),
	}
	return strings.TrimSpace(p.IDTokenEmail), t, true
}
