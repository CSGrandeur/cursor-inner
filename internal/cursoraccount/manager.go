package cursoraccount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"cursor-inner/internal/config"
	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
	"cursor-inner/internal/logx"
)

const liveEmailTTL = 3 * time.Second

// postStopSettle 给退出的 Cursor Electron / SQLite WAL 留出释放文件锁的时间；测试可调小。
var postStopSettle = 250 * time.Millisecond

// Manager 是 Cursor 账号管理的入口。凭据加解密与 state.vscdb 读写在本包内；
// 结束与重启 Cursor 由 app 层注入（严格只杀 Cursor.exe，不带 /T 误杀 wsl.exe）。
type Manager struct {
	store *Store
	proxy func() config.Proxy

	mu       sync.Mutex
	switchMu sync.Mutex
	flow     *loginFlow

	liveMu     sync.Mutex
	liveEmail  string
	liveAt     time.Time
	liveStamp  string
	liveTarget string

	stopIDE     func() error
	startIDE    func() error
	ideRunning  func() bool
	openBrowser func(url string) error
	exePathFn   func() string

	applyCreds    func(Account) error
	readCurrentFn func() (Account, bool, error)
	liveEmailFn   func() string
	fetchProfFn   func(ctx context.Context, client *http.Client, accessToken string, prev *QuotaInfo) profileResult
}

func NewManager(store *Store, proxy func() config.Proxy) *Manager {
	return &Manager{
		store:       store,
		proxy:       proxy,
		fetchProfFn: fetchProfileAndUsage,
	}
}

func (m *Manager) SetHooks(stopIDE, startIDE func() error, ideRunning func() bool, openBrowser func(string) error) {
	m.stopIDE, m.startIDE, m.ideRunning, m.openBrowser = stopIDE, startIDE, ideRunning, openBrowser
}

func (m *Manager) SetExePath(fn func() string) {
	m.exePathFn = fn
}

func (m *Manager) EnableHostDetection() {
	m.readCurrentFn = m.defaultReadCurrent
	m.liveEmailFn = m.defaultLiveEmail
}

func (m *Manager) snapshotExePath() string {
	if m.exePathFn != nil {
		return m.exePathFn()
	}
	return ""
}

func (m *Manager) client() *http.Client {
	p := config.Proxy{}
	if m.proxy != nil {
		p = m.proxy()
	}
	d, err := dialer.Scoped(p)
	if err != nil {
		d = dialer.Direct()
	}
	return httpClient(d)
}

func (m *Manager) getProfile(ctx context.Context, client *http.Client, accessToken string, prev *QuotaInfo) profileResult {
	if m.fetchProfFn == nil {
		return profileResult{Quota: prev}
	}
	return m.fetchProfFn(ctx, client, accessToken, prev)
}

// View 是给配置页的账号摘要（不含任何 token）。
type View struct {
	ID        string        `json:"id"`
	Email     string        `json:"email"`
	Name      string        `json:"name,omitempty"`
	Tier      string        `json:"tier,omitempty"`
	Quotas    []QuotaBucket `json:"quotas,omitempty"`
	Status    string        `json:"status,omitempty"`
	CreatedAt int64         `json:"created_at"`
	LastUsed  int64         `json:"last_used"`
	Current   bool          `json:"current"`
	Expiry    int64         `json:"expiry,omitempty"`
}

func viewOf(a Account, current bool) View {
	v := View{
		ID:        a.ID,
		Email:     a.Email,
		Name:      a.Name,
		Status:    a.Status,
		CreatedAt: a.CreatedAt,
		LastUsed:  a.LastUsed,
		Current:   current,
		Expiry:    a.Token.Expiry,
	}
	if a.Quota != nil {
		v.Tier = a.Quota.Tier
		if len(a.Quota.Buckets) > 0 {
			v.Quotas = append([]QuotaBucket(nil), a.Quota.Buckets...)
		}
	}
	return v
}

func (m *Manager) invalidateLiveCache() {
	m.liveMu.Lock()
	m.liveEmail = ""
	m.liveAt = time.Time{}
	m.liveStamp = ""
	m.liveTarget = ""
	m.liveMu.Unlock()
}

func fileStamp(path string) string {
	var b strings.Builder
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			b.WriteString(p)
			b.WriteByte(':')
			b.WriteString(fi.ModTime().UTC().Format(time.RFC3339Nano))
			b.WriteByte(':')
			b.WriteString(jsonInt64(fi.Size()))
			b.WriteByte('|')
		}
	}
	return b.String()
}

func jsonInt64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (m *Manager) cachedLiveEmail() string {
	if m.liveEmailFn == nil {
		return ""
	}
	m.liveMu.Lock()
	if !m.liveAt.IsZero() && time.Since(m.liveAt) < liveEmailTTL {
		cached := m.liveEmail
		m.liveMu.Unlock()
		return cached
	}
	target := m.liveTarget
	prevStamp := m.liveStamp
	cached := m.liveEmail
	m.liveMu.Unlock()

	if target != "" && prevStamp != "" && fileStamp(target) == prevStamp {
		m.liveMu.Lock()
		m.liveAt = time.Now()
		m.liveMu.Unlock()
		return cached
	}

	live := normalizeEmail(m.liveEmailFn())
	m.liveMu.Lock()
	m.liveEmail = live
	m.liveAt = time.Now()
	m.liveMu.Unlock()
	return live
}

func (m *Manager) defaultLiveEmail() string {
	exePath := m.snapshotExePath()
	for _, dbPath := range hostCandidateStateDBPaths(exePath) {
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}
		email := LiveEmailFromDB(dbPath)
		m.liveMu.Lock()
		m.liveTarget = dbPath
		m.liveStamp = fileStamp(dbPath)
		m.liveMu.Unlock()
		if email != "" {
			return email
		}
	}
	return ""
}

func (m *Manager) List() []View {
	current := m.store.CurrentID()
	accounts := m.store.List()
	live := m.cachedLiveEmail()
	matchedLive := false
	if live != "" {
		for _, a := range accounts {
			if normalizeEmail(a.Email) == live {
				matchedLive = true
				break
			}
		}
	}
	out := make([]View, 0, len(accounts))
	for _, a := range accounts {
		isCur := a.ID == current
		if matchedLive {
			isCur = normalizeEmail(a.Email) == live
		}
		out = append(out, viewOf(a, isCur))
	}
	return out
}

func (m *Manager) viewForID(id string, fallback Account) View {
	for _, v := range m.List() {
		if v.ID == id {
			return v
		}
	}
	return viewOf(fallback, m.store.CurrentID() == id)
}

func (m *Manager) LoginStart() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != nil {
		m.flow.cancel()
		m.flow = nil
	}
	f, err := beginLogin()
	if err != nil {
		return "", err
	}
	m.flow = f
	if m.openBrowser != nil {
		if err := m.openBrowser(f.authURL); err != nil {
			slog.Warn("打开浏览器失败，Cursor 授权链接仍可在配置页复制", "error", err.Error(), logx.FileOnly)
		}
	}
	return f.authURL, nil
}

func (m *Manager) LoginWait(ctx context.Context) (View, error) {
	m.mu.Lock()
	f := m.flow
	m.mu.Unlock()
	if f == nil {
		return View{}, i18n.E("没有进行中的 Cursor 授权，请先点「添加」。", "No Cursor authorization is in progress. Click Add first.")
	}
	client := m.client()
	accTok, refTok, authID, err := f.waitToken(ctx, client)
	if err != nil {
		return View{}, err
	}
	sub, jwtEmail, exp := parseJWTClaims(accTok)
	if authID == "" {
		authID = sub
	}
	prof := m.getProfile(ctx, client, accTok, nil)
	email := firstNonEmpty(prof.Email, jwtEmail)
	if prof.AuthID != "" {
		authID = prof.AuthID
	}
	if email == "" {
		if authID != "" {
			email = authID
		} else {
			return View{}, i18n.E("Cursor 没有返回账号邮箱，请重试。", "Cursor did not return an account email. Please try again.")
		}
	}
	token := Token{AccessToken: accTok, RefreshToken: refTok, Expiry: exp}
	a, err := m.store.upsert(email, prof.Name, authID, prof.SignUpType, token)
	if err != nil {
		return View{}, err
	}
	if prof.Quota != nil {
		_ = m.store.saveMetaQuota(a.ID, email, prof.Name, authID, prof.SignUpType, token, prof.Quota, "")
	}
	m.mu.Lock()
	if m.flow == f {
		m.flow = nil
	}
	m.mu.Unlock()
	m.invalidateLiveCache()
	if fresh, ok := m.store.get(a.ID); ok {
		a = fresh
	}
	slog.Info("Cursor 账号已添加", "email", a.Email, logx.FileOnly)
	return m.viewForID(a.ID, a), nil
}

func (m *Manager) LoginCancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != nil {
		m.flow.cancel()
		m.flow = nil
	}
}

// Import 用粘贴的 Token（JWT / WorkosCursorSessionToken / user_xxx::eyJ...）添加账号。
func (m *Manager) Import(ctx context.Context, rawToken string) (View, error) {
	return m.importOne(ctx, "", rawToken, "", "")
}

func (m *Manager) importOne(ctx context.Context, hintAccess, rawRefresh, hintEmail, hintName string) (View, error) {
	ref := normalizePastedToken(rawRefresh)
	acc := normalizePastedToken(hintAccess)
	if ref == "" {
		ref = acc
	}
	if ref == "" {
		return View{}, i18n.E("Cursor 凭据不能为空", "The Cursor token cannot be empty")
	}

	client := m.client()
	var token Token
	var prof profileResult
	if acc != "" && (Token{AccessToken: acc, Expiry: jwtExp(acc)}).fresh(time.Now()) {
		token = Token{AccessToken: acc, RefreshToken: ref, Expiry: jwtExp(acc)}
		prof = m.getProfile(ctx, client, token.AccessToken, nil)
	}
	if token.AccessToken == "" || prof.AuthFailed {
		refreshed, rerr := refreshToken(ctx, client, ref)
		if rerr == nil {
			token = refreshed
			prof = m.getProfile(ctx, client, token.AccessToken, nil)
		} else if !errors.Is(rerr, errSessionExpired) && hasValidClaimOrJWT(ref) {
			// 浏览器 Cookie WorkosCursorSessionToken 有的只用来鉴权、不走 /oauth/token 换票：
			// 若它能直接调通 Cursor 资料接口，照样收进列表。
			prof = m.getProfile(ctx, client, ref, nil)
			if prof.AuthFailed || (prof.Email == "" && prof.AuthID == "" && prof.Quota == nil) {
				return View{}, rerr
			}
			token = Token{AccessToken: ref, RefreshToken: ref, Expiry: jwtExp(ref)}
		} else {
			return View{}, rerr
		}
	}

	sub, jwtEmail, exp := parseJWTClaims(token.AccessToken)
	if token.Expiry == 0 {
		token.Expiry = exp
	}
	email := firstNonEmpty(prof.Email, jwtEmail, normalizeEmail(hintEmail))
	authID := firstNonEmpty(prof.AuthID, sub)
	name := firstNonEmpty(prof.Name, hintName)
	if email == "" {
		if authID == "" {
			return View{}, i18n.E("读不到这个 Cursor 凭据对应的邮箱，请检查后重试。", "Cannot determine the email for this Cursor credential. Check the token and try again.")
		}
		email = authID
	}
	a, err := m.store.upsert(email, name, authID, prof.SignUpType, token)
	if err != nil {
		return View{}, err
	}
	if prof.Quota != nil {
		_ = m.store.saveMetaQuota(a.ID, email, name, authID, prof.SignUpType, token, prof.Quota, "")
	}
	m.invalidateLiveCache()
	if fresh, ok := m.store.get(a.ID); ok {
		a = fresh
	}
	slog.Info("Cursor 账号已导入", "email", a.Email, logx.FileOnly)
	return m.viewForID(a.ID, a), nil
}

func jwtExp(tok string) int64 {
	_, _, exp := parseJWTClaims(tok)
	return exp
}

func hasValidClaimOrJWT(tok string) bool {
	if len(strings.Split(tok, ".")) == 3 && strings.HasPrefix(tok, "eyJ") {
		_, _, exp := parseJWTClaims(tok)
		return exp == 0 || exp > time.Now().Unix()+30
	}
	return false
}

// ImportCurrent 读取本机 Cursor 当前登录账号，补全资料与配额后保存并标为当前。
func (m *Manager) ImportCurrent(ctx context.Context) (View, error) {
	var (
		cur Account
		ok  bool
		err error
	)
	if m.readCurrentFn != nil {
		cur, ok, err = m.readCurrentFn()
	} else {
		cur, ok, err = m.defaultReadCurrent()
	}
	if err != nil {
		return View{}, err
	}
	if !ok || (cur.Token.AccessToken == "" && cur.Token.RefreshToken == "") {
		return View{}, i18n.E("读不到 Cursor 当前登录的账号。先在 Cursor 里登录，或手动粘贴凭据。", "Cannot read the account Cursor is signed in with. Sign in inside Cursor first, or paste a token.")
	}
	client := m.client()
	tok, err := ensureFresh(ctx, client, cur.Token, time.Now())
	if err != nil {
		// 若当前 accessToken 尚未过期、只是 /oauth/token 返回非过期异常，仍尝试直接探测资料。
		if !errors.Is(err, errSessionExpired) && cur.Token.AccessToken != "" && jwtExp(cur.Token.AccessToken) > time.Now().Unix()+30 {
			tok = cur.Token
		} else {
			return View{}, err
		}
	}
	prof := m.getProfile(ctx, client, tok.AccessToken, cur.Quota)
	sub, jwtEmail, exp := parseJWTClaims(tok.AccessToken)
	if tok.Expiry == 0 {
		tok.Expiry = exp
	}
	email := firstNonEmpty(cur.Email, prof.Email, jwtEmail)
	if email == "" {
		email = firstNonEmpty(prof.AuthID, cur.AuthID, sub)
	}
	if email == "" {
		return View{}, i18n.E("读不到 Cursor 当前账号的邮箱。", "Cannot determine the email of the current Cursor account.")
	}
	authID := firstNonEmpty(prof.AuthID, cur.AuthID, sub)
	name := firstNonEmpty(prof.Name, cur.Name)
	signUp := firstNonEmpty(prof.SignUpType, cur.SignUpType)

	a, err := m.store.upsert(email, name, authID, signUp, tok)
	if err != nil {
		return View{}, err
	}
	if prof.Quota != nil {
		_ = m.store.saveMetaQuota(a.ID, email, name, authID, signUp, tok, prof.Quota, "")
	}
	_ = m.store.setCurrent(a.ID)
	m.invalidateLiveCache()
	slog.Info("已导入 Cursor 当前账号", "email", a.Email, logx.FileOnly)
	v := m.viewForID(a.ID, a)
	v.Current = true
	return v, nil
}

func (m *Manager) defaultReadCurrent() (Account, bool, error) {
	exePath := m.snapshotExePath()
	var firstErr error
	for _, dbPath := range hostCandidateStateDBPaths(exePath) {
		a, ok, err := CurrentFromState(dbPath)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ok {
			return a, true, nil
		}
	}
	return Account{}, false, firstErr
}

func (m *Manager) Remove(id string) error {
	wasCurrent := m.store.CurrentID() == id
	if err := m.store.remove(id); err != nil {
		return err
	}
	if wasCurrent {
		m.invalidateLiveCache()
	}
	return nil
}

func (m *Manager) Refresh(ctx context.Context, id string) (View, error) {
	a, ok := m.store.get(id)
	if !ok {
		return View{}, i18n.E("没有这个账号", "No such account")
	}
	client := m.client()
	// 显式刷新始终请求续期；仅在非会话过期错误且原令牌仍有效时保留原令牌续查配额。
	tok, err := ensureFresh(ctx, client, a.Token, time.Unix(1<<60, 0))
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			_ = m.store.setStatus(id, "expired")
			return View{}, err
		}
		if a.Token.AccessToken != "" && (a.Token.Expiry == 0 || a.Token.Expiry > time.Now().Unix()+60) {
			tok = a.Token
		} else {
			return View{}, err
		}
	}
	prof := m.getProfile(ctx, client, tok.AccessToken, a.Quota)
	if prof.AuthFailed {
		_ = m.store.setStatus(id, "expired")
		return View{}, errExpiredWrap()
	}
	if err := m.store.saveMetaQuota(id, prof.Email, prof.Name, prof.AuthID, prof.SignUpType, tok, prof.Quota, ""); err != nil {
		return View{}, err
	}
	updated, ok := m.store.get(id)
	if !ok {
		return viewOf(a, false), nil
	}
	return m.viewForID(id, updated), nil
}

func (m *Manager) RefreshAll(ctx context.Context) (okCount, failCount int, firstErr error) {
	accounts := m.store.List()
	if len(accounts) == 0 {
		return 0, 0, nil
	}
	const maxWorkers = 4
	sem := make(chan struct{}, maxWorkers)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, a := range accounts {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				failCount++
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			}
			if _, err := m.Refresh(ctx, id); err != nil {
				mu.Lock()
				failCount++
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			okCount++
			mu.Unlock()
		}(a.ID)
	}
	wg.Wait()
	return okCount, failCount, firstErr
}

// Switch 把 Cursor 切到指定账号：先确认目标凭据有效、关掉正在运行的 Cursor、
// 备份并写入 state.vscdb，再按原路径重新打开。
func (m *Manager) Switch(ctx context.Context, id string) error {
	m.switchMu.Lock()
	defer m.switchMu.Unlock()

	a, ok := m.store.get(id)
	if !ok {
		return i18n.E("没有这个账号", "No such account")
	}
	client := m.client()
	tok, err := ensureFresh(ctx, client, a.Token, time.Now())
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			_ = m.store.setStatus(id, "expired")
			return err
		}
		if a.Token.AccessToken == "" || (a.Token.Expiry > 0 && a.Token.Expiry <= time.Now().Unix()+60) {
			return err
		}
		tok = a.Token
	}
	if err := m.store.saveToken(id, tok); err != nil {
		return err
	}
	a.Token = tok

	if m.stopIDE == nil {
		return i18n.E("这个平台还不支持切换 Cursor 账号", "Switching Cursor accounts is not supported on this platform yet")
	}
	exePath := m.snapshotExePath()
	if m.applyCreds == nil && len(existingStateDBs(exePath)) == 0 {
		return i18n.E("找不到 Cursor 的状态库。先打开一次 Cursor 并登录，再在这里切换账号。", "Cursor's state database was not found. Open Cursor and sign in once, then switch accounts here.")
	}

	wasRunning := m.ideRunning != nil && m.ideRunning()
	if err := m.stopIDE(); err != nil {
		return err
	}
	if wasRunning {
		if err := waitIDEStopped(m.ideRunning); err != nil {
			return err
		}
		if postStopSettle > 0 {
			timer := time.NewTimer(postStopSettle)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err := m.writeCreds(a, exePath); err != nil {
		return err
	}
	if err := m.store.setCurrent(id); err != nil {
		return err
	}
	m.invalidateLiveCache()
	slog.Info("Cursor 账号已切换", "email", a.Email, logx.FileOnly)
	if m.startIDE != nil {
		if err := m.startIDE(); err != nil {
			return i18n.Wrap("凭据已写入，但重新打开 Cursor 失败：", "Credentials were written, but reopening Cursor failed: ", err)
		}
	}
	return nil
}

func existingStateDBs(exePath string) []string {
	var out []string
	for _, p := range hostCandidateStateDBPaths(exePath) {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (m *Manager) writeCreds(a Account, exePath string) error {
	if m.applyCreds != nil {
		return m.applyCreds(a)
	}
	dbs := existingStateDBs(exePath)
	if len(dbs) == 0 {
		return i18n.E("找不到 Cursor 的状态库。先打开一次 Cursor 并登录，再在这里切换账号。", "Cursor's state database was not found. Open Cursor and sign in once, then switch accounts here.")
	}
	for _, dbPath := range dbs {
		if err := injectAccount(dbPath, a); err != nil {
			return err
		}
	}
	return nil
}

func waitIDEStopped(running func() bool) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !running() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !running() {
		return nil
	}
	return i18n.E("Cursor 还没退出，没有写入凭据。请先手动关掉它再切换。", "Cursor has not exited, so credentials were not written. Close it and switch again.")
}

// ExportJSON 导出已存 Cursor 账号备份 JSON（含 token，只留在本机）。
func (m *Manager) ExportJSON() ([]byte, error) {
	type item struct {
		Email        string `json:"email"`
		Name         string `json:"name,omitempty"`
		AccessToken  string `json:"access_token,omitempty"`
		RefreshToken string `json:"refresh_token"`
	}
	var items []item
	for _, a := range m.store.List() {
		ref := a.Token.RefreshToken
		if ref == "" {
			ref = a.Token.AccessToken
		}
		if ref == "" {
			continue
		}
		items = append(items, item{
			Email:        a.Email,
			Name:         a.Name,
			AccessToken:  a.Token.AccessToken,
			RefreshToken: ref,
		})
	}
	if items == nil {
		items = []item{}
	}
	return json.MarshalIndent(map[string]any{"accounts": items}, "", "  ")
}

// ImportResult 是批量导入里一条的结果。
type ImportResult struct {
	Email string    `json:"email,omitempty"`
	OK    bool      `json:"ok"`
	Error i18n.Text `json:"error,omitzero"`
}

type importEntry struct {
	Email        string
	Name         string
	AccessToken  string
	RefreshToken string
}

type rawImportItem struct {
	Email        string `json:"email"`
	Name         string `json:"name"`
	CustomLabel  string `json:"custom_label"`
	AccessToken  string `json:"access_token"`
	AccessCamel  string `json:"accessToken"`
	RefreshToken string `json:"refresh_token"`
	RefreshCamel string `json:"refreshToken"`
	Token        *struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	} `json:"token"`
}

func extractImportEntry(it rawImportItem) (importEntry, bool) {
	ref := normalizePastedToken(firstNonEmpty(it.RefreshToken, it.RefreshCamel))
	acc := normalizePastedToken(firstNonEmpty(it.AccessToken, it.AccessCamel))
	if it.Token != nil {
		if ref == "" {
			ref = normalizePastedToken(it.Token.RefreshToken)
		}
		if acc == "" {
			acc = normalizePastedToken(it.Token.AccessToken)
		}
	}
	if ref == "" {
		ref = acc
	}
	if ref == "" {
		return importEntry{}, false
	}
	return importEntry{
		Email:        strings.TrimSpace(it.Email),
		Name:         firstNonEmpty(it.CustomLabel, it.Name),
		AccessToken:  acc,
		RefreshToken: ref,
	}, true
}

func parseImportEntries(raw []byte) ([]importEntry, error) {
	cleaned := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))
	if len(cleaned) == 0 {
		return nil, i18n.E("输入内容为空", "The input is empty")
	}
	var entries []importEntry
	switch cleaned[0] {
	case '[':
		var items []rawImportItem
		if err := json.Unmarshal(cleaned, &items); err != nil {
			return nil, i18n.E("JSON 读不出来：需要含 token 的账号数组。", "Cannot parse the JSON: expected an array where each item has a token.")
		}
		for _, it := range items {
			if e, ok := extractImportEntry(it); ok {
				entries = append(entries, e)
			}
		}
	case '{':
		var wrap struct {
			Accounts     json.RawMessage `json:"accounts"`
			AccessToken  string          `json:"access_token"`
			RefreshToken string          `json:"refresh_token"`
			Token        json.RawMessage `json:"token"`
		}
		if err := json.Unmarshal(cleaned, &wrap); err != nil {
			return nil, i18n.E("JSON 读不出来：需要 accounts 字段或含 token 的对象。", "Cannot parse the JSON: expected an accounts field or an object with a token.")
		}
		if acc := bytes.TrimSpace(wrap.Accounts); len(acc) > 0 {
			if acc[0] == '[' {
				var items []rawImportItem
				if err := json.Unmarshal(acc, &items); err != nil {
					return nil, i18n.E("JSON 读不出来：accounts 数组格式有误。", "Cannot parse the JSON: invalid accounts array.")
				}
				for _, it := range items {
					if e, ok := extractImportEntry(it); ok {
						entries = append(entries, e)
					}
				}
			} else if acc[0] == '{' {
				var itemMap map[string]rawImportItem
				if err := json.Unmarshal(acc, &itemMap); err != nil {
					return nil, i18n.E("JSON 读不出来：accounts 映射格式有误。", "Cannot parse the JSON: invalid accounts map.")
				}
				for _, it := range itemMap {
					if e, ok := extractImportEntry(it); ok {
						entries = append(entries, e)
					}
				}
			}
		} else if wrap.RefreshToken != "" || wrap.AccessToken != "" || len(wrap.Token) > 0 {
			var single rawImportItem
			if json.Unmarshal(cleaned, &single) == nil {
				if e, ok := extractImportEntry(single); ok {
					entries = append(entries, e)
				}
			}
		} else {
			var itemMap map[string]rawImportItem
			if json.Unmarshal(cleaned, &itemMap) == nil {
				for _, it := range itemMap {
					if e, ok := extractImportEntry(it); ok {
						entries = append(entries, e)
					}
				}
			}
		}
	default:
		fields := strings.FieldsFunc(string(cleaned), func(r rune) bool {
			return r == ',' || r == ';' || r == '"' || r == '\'' || r <= ' '
		})
		for _, tok := range fields {
			norm := normalizePastedToken(tok)
			if norm != "" && strings.Count(norm, ".") >= 2 {
				entries = append(entries, importEntry{RefreshToken: norm})
			}
		}
		if len(entries) == 0 {
			return nil, i18n.E("格式读不出来：需要导出 JSON 或逐条的 Cursor 凭据。", "Cannot parse input: expected JSON accounts or Cursor tokens.")
		}
	}

	seen := make(map[string]bool, len(entries))
	dedup := make([]importEntry, 0, len(entries))
	for _, e := range entries {
		key := e.RefreshToken
		if key == "" {
			key = e.AccessToken
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		dedup = append(dedup, e)
	}
	if len(dedup) == 0 {
		return nil, i18n.E("输入里没有有效的 Cursor 账号或凭据", "No valid Cursor accounts or tokens were found")
	}
	return dedup, nil
}

func (m *Manager) ImportJSON(ctx context.Context, raw []byte) ([]ImportResult, error) {
	entries, err := parseImportEntries(raw)
	if err != nil {
		return nil, err
	}
	out := make([]ImportResult, len(entries))
	const maxWorkers = 4
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(idx int, entry importEntry) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[idx] = ImportResult{Email: entry.Email, OK: false, Error: i18n.Of(ctx.Err())}
				return
			}
			v, ierr := m.importOne(ctx, entry.AccessToken, entry.RefreshToken, entry.Email, entry.Name)
			if ierr != nil {
				out[idx] = ImportResult{Email: entry.Email, OK: false, Error: i18n.Of(ierr)}
				return
			}
			out[idx] = ImportResult{Email: v.Email, OK: true}
		}(i, e)
	}
	wg.Wait()
	return out, nil
}
