package antiaccount

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

// postStopSettle 给已退出主进程的 Electron / WSL 语言服务留出收尾释放文件句柄的时间；测试可调小。
var postStopSettle = 250 * time.Millisecond

// Manager 是反重力账号管理的入口。凭据读写（state.vscdb、系统凭据库、文件与 WSL 回退）
// 都在这里；结束与重启 IDE 由上层注入的 hooks 负责，接管参数不在本包处理。
type Manager struct {
	store *Store
	proxy func() config.Proxy // 每次调用取当前代理设置

	mu       sync.Mutex
	switchMu sync.Mutex
	flow     *loginFlow

	liveMu    sync.Mutex
	liveEmail string
	liveAt    time.Time

	// hooks 由 app 层注入；nil 表示对应能力不可用（如非 Windows 的进程管理）。
	stopIDE         func() error // 结束正在运行的反重力；没在运行返回 nil
	startIDE        func() error // 按当前接管设置冷启动反重力
	ideRunning      func() bool  // 是否有反重力在运行
	openBrowser     func(url string) error
	exePathFn       func() string
	resourcesDirsFn func(hintExe string) []string

	oauthMu             sync.Mutex
	oauthResolveMu      sync.Mutex
	oauthCached         *cachedOAuthClient
	oauthClientOverride *oauthClient

	// 测试替身；nil 时按默认主机路径或默认查询逻辑。
	applyCreds    func(Account) error
	readCurrentFn func() (email, name string, token Token, ok bool, err error)
	liveEmailFn   func() string
	fetchQuotaFn  func(ctx context.Context, client *http.Client, accessToken string, prev *QuotaInfo) *QuotaInfo
}

func NewManager(store *Store, proxy func() config.Proxy) *Manager {
	return &Manager{
		store:        store,
		proxy:        proxy,
		fetchQuotaFn: fetchQuota,
	}
}

// SetHooks 注入进程与浏览器操作。app 层装配时调用一次。
func (m *Manager) SetHooks(stopIDE, startIDE func() error, ideRunning func() bool, openBrowser func(string) error) {
	m.stopIDE, m.startIDE, m.ideRunning, m.openBrowser = stopIDE, startIDE, ideRunning, openBrowser
}

// SetExePath 注入查询运行中 Antigravity 可执行路径的函数（便携版路径探测用）。
func (m *Manager) SetExePath(fn func() string) {
	m.exePathFn = fn
}

// SetResourcesDirs 注入按可执行路径查找 Antigravity resources 目录的函数（用于运行期读取 OAuth 客户端参数）。
func (m *Manager) SetResourcesDirs(fn func(hintExe string) []string) {
	m.resourcesDirsFn = fn
}

// EnableHostDetection 打开实机当前账号探测（state.vscdb / Windows 凭据库 / .gemini 回退）。
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

func (m *Manager) getQuota(ctx context.Context, client *http.Client, accessToken string, prev *QuotaInfo) *QuotaInfo {
	if m.fetchQuotaFn == nil {
		return prev
	}
	return m.fetchQuotaFn(ctx, client, accessToken, prev)
}

// View 是给配置页的账号摘要，不含任何 token。
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
	// Expiry 是 access token 到期时刻（Unix 秒）；0 表示只有 refresh token。
	Expiry int64 `json:"expiry,omitempty"`
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
		if a.Quota.Forbidden && v.Status == "" {
			v.Status = "forbidden"
		}
	}
	return v
}

func (m *Manager) invalidateLiveCache() {
	m.liveMu.Lock()
	m.liveEmail = ""
	m.liveAt = time.Time{}
	m.liveMu.Unlock()
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
	m.liveMu.Unlock()

	live := normalizeEmail(m.liveEmailFn())
	m.liveMu.Lock()
	m.liveEmail = live
	m.liveAt = time.Now()
	m.liveMu.Unlock()
	return live
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

// LoginStart 开始一次 Google 授权，返回给浏览器的授权链接。
// 同一时间只有一个流程；再次开始会取消上一个。
func (m *Manager) LoginStart() (string, error) {
	clientID, err := m.resolveClientID()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != nil {
		m.flow.cancel()
		m.flow = nil
	}
	f, err := beginLogin(clientID)
	if err != nil {
		return "", err
	}
	m.flow = f
	if m.openBrowser != nil {
		if err := m.openBrowser(f.authURL); err != nil {
			slog.Warn("打开浏览器失败，授权链接仍可在配置页复制", "error", err.Error(), logx.FileOnly)
		}
	}
	return f.authURL, nil
}

// LoginWait 等当前授权流程完成：换票、取账号信息、保存。没有进行中的流程时报错。
func (m *Manager) LoginWait(ctx context.Context) (View, error) {
	m.mu.Lock()
	f := m.flow
	m.mu.Unlock()
	if f == nil {
		return View{}, i18n.E("没有进行中的授权，请先点「添加账号」。", "No authorization is in progress. Click Add account first.")
	}
	code, err := f.waitCode(ctx)
	if err != nil {
		return View{}, err
	}
	client := m.client()
	tr, err := m.exchangeCode(ctx, client, code, f.redirect)
	if err != nil {
		return View{}, err
	}
	if tr.RefreshToken == "" {
		return View{}, i18n.E("Google 没有返回 refresh token：请先在 Google 账号的第三方授权里撤销 Antigravity，再重新添加。", "Google did not return a refresh token: revoke Antigravity in your Google account's third-party access, then add the account again.")
	}
	email, name, err := userInfo(ctx, client, tr.AccessToken)
	if err != nil {
		return View{}, err
	}
	token := Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
		Expiry:       time.Now().Unix() + tr.ExpiresIn,
	}
	a, err := m.store.upsert(email, name, token)
	if err != nil {
		return View{}, err
	}
	m.mu.Lock()
	if m.flow == f {
		m.flow = nil
	}
	m.mu.Unlock()
	m.invalidateLiveCache()
	a = m.enrichQuota(ctx, client, a)
	slog.Info("反重力账号已添加", "email", a.Email, logx.FileOnly)
	return m.viewForID(a.ID, a), nil
}

func (m *Manager) enrichQuota(ctx context.Context, client *http.Client, a Account) Account {
	if m.fetchQuotaFn == nil {
		return a
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	q := m.fetchQuotaFn(qctx, client, a.Token.AccessToken, a.Quota)
	if q == nil {
		return a
	}
	status := ""
	if q.Forbidden {
		status = "forbidden"
	}
	if err := m.store.saveQuotaAndToken(a.ID, a.Token, q, status); err == nil {
		if fresh, ok := m.store.get(a.ID); ok {
			return fresh
		}
	}
	a.Quota = q
	a.Status = status
	return a
}

// LoginCancel 取消进行中的授权。
func (m *Manager) LoginCancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != nil {
		m.flow.cancel()
		m.flow = nil
	}
}

// Import 用 refresh token 添加账号：先刷新验证，再取邮箱与配额。
func (m *Manager) Import(ctx context.Context, refresh string) (View, error) {
	return m.importOne(ctx, refresh, "")
}

func (m *Manager) importOne(ctx context.Context, refresh, hintName string) (View, error) {
	refresh = strings.TrimSpace(refresh)
	if refresh == "" {
		return View{}, i18n.E("refresh token 不能为空", "The refresh token cannot be empty")
	}
	client := m.client()
	tr, err := m.refreshToken(ctx, client, refresh)
	if err != nil {
		return View{}, err
	}
	email, name, err := userInfo(ctx, client, tr.AccessToken)
	if err != nil {
		return View{}, err
	}
	if name == "" {
		name = strings.TrimSpace(hintName)
	}
	token := Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: refresh,
		IDToken:      tr.IDToken,
		Expiry:       time.Now().Unix() + tr.ExpiresIn,
	}
	a, err := m.store.upsert(email, name, token)
	if err != nil {
		return View{}, err
	}
	m.invalidateLiveCache()
	a = m.enrichQuota(ctx, client, a)
	slog.Info("反重力账号已导入", "email", a.Email, logx.FileOnly)
	return m.viewForID(a.ID, a), nil
}

// ImportCurrent 把反重力当前登录的账号收进列表：按状态库 → WSL 回退文件 →
// 系统凭据库 → 本机 ~/.gemini 文件顺序读取，并标为当前账号。不写任何外部凭据。
func (m *Manager) ImportCurrent(ctx context.Context) (View, error) {
	var (
		email string
		name  string
		token Token
		ok    bool
		err   error
	)
	if m.readCurrentFn != nil {
		email, name, token, ok, err = m.readCurrentFn()
	} else {
		email, name, token, ok, err = m.defaultReadCurrent()
	}
	if err != nil {
		slog.Warn("读反重力状态库失败，改试凭据库或回退文件", "error", err.Error(), logx.FileOnly)
	}
	if !ok || token.RefreshToken == "" {
		return View{}, i18n.E("读不到反重力当前登录的账号。先在反重力里登录，或手动粘贴 refresh token。", "Cannot read the account Antigravity is signed in with. Sign in inside Antigravity first, or paste a refresh token.")
	}
	client := m.client()
	token, err = m.ensureFresh(ctx, client, token, time.Now())
	if err != nil {
		return View{}, err
	}
	if email == "" {
		email, name, err = userInfo(ctx, client, token.AccessToken)
		if err != nil {
			return View{}, err
		}
	}
	a, err := m.store.upsert(email, name, token)
	if err != nil {
		return View{}, err
	}
	_ = m.store.setCurrent(a.ID)
	m.invalidateLiveCache()
	a = m.enrichQuota(ctx, client, a)
	slog.Info("已导入反重力当前账号", "email", a.Email, logx.FileOnly)
	v := m.viewForID(a.ID, a)
	v.Current = true
	return v, nil
}

func (m *Manager) defaultReadCurrent() (email, name string, token Token, ok bool, err error) {
	exePath := m.snapshotExePath()
	for _, dbPath := range hostCandidateStateDBPaths(exePath) {
		e, n, t, found, derr := CurrentFromDB(dbPath)
		if derr != nil && err == nil {
			err = derr
		}
		if found && t.RefreshToken != "" {
			return e, n, t, true, err
		}
	}
	if _, wslGemini := activeWSLGeminiDir(); wslGemini != "" {
		if e, t, found := readFallbackFileFromDir(wslGemini); found && t.RefreshToken != "" {
			return e, "", t, true, err
		}
	}
	if raw, rerr := keyringRead(); rerr == nil {
		if kEmail, kToken, kOK := parseKeyringPayload(raw); kOK && kToken.RefreshToken != "" {
			return kEmail, "", kToken, true, err
		}
	}
	if e, t, found := readFallbackFileFromDir(geminiDir()); found && t.RefreshToken != "" {
		return e, "", t, true, err
	}
	return "", "", Token{}, false, err
}

// defaultLiveEmail 以零子进程方式探查 IDE / 凭据文件当前登录邮箱。
func (m *Manager) defaultLiveEmail() string {
	exePath := m.snapshotExePath()
	for _, dbPath := range hostCandidateStateDBPaths(exePath) {
		e, _, t, found, _ := CurrentFromDB(dbPath)
		if found && t.RefreshToken != "" && strings.TrimSpace(e) != "" {
			return e
		}
	}
	if _, wslGemini := activeWSLGeminiDir(); wslGemini != "" {
		if e, t, found := readFallbackFileFromDir(wslGemini); found && t.RefreshToken != "" && strings.TrimSpace(e) != "" {
			return e
		}
	}
	if raw, rerr := keyringReadInProcess(); rerr == nil {
		if e, t, found := parseKeyringPayload(raw); found && t.RefreshToken != "" && strings.TrimSpace(e) != "" {
			return e
		}
	}
	if e, t, found := readFallbackFileFromDir(geminiDir()); found && t.RefreshToken != "" && strings.TrimSpace(e) != "" {
		return e
	}
	return ""
}

// Remove 删除保存的账号。不影响 IDE 当前登录状态。
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

// Refresh 刷新一个账号的 access token、订阅档位和配额并回写。
func (m *Manager) Refresh(ctx context.Context, id string) (View, error) {
	a, ok := m.store.get(id)
	if !ok {
		return View{}, i18n.E("没有这个账号", "No such account")
	}
	client := m.client()
	// 强制刷新：把「现在」拨过临期阈值。
	token, err := m.ensureFresh(ctx, client, a.Token, time.Now().Add(freshSkew*time.Second))
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			_ = m.store.setStatus(id, "invalid_grant")
		}
		return View{}, err
	}
	q := m.getQuota(ctx, client, token.AccessToken, a.Quota)
	status := ""
	if q != nil && q.Forbidden {
		status = "forbidden"
	}
	if err := m.store.saveQuotaAndToken(id, token, q, status); err != nil {
		return View{}, err
	}
	updated, ok := m.store.get(id)
	if !ok {
		a.Token, a.Quota, a.Status = token, q, status
		updated = a
	}
	return m.viewForID(id, updated), nil
}

// RefreshAll 并发刷新全部已保存账号的 token 与配额（并发上限 4）。
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

// Switch 把 IDE 切到指定账号：刷新凭据 → 若正在运行则结束并等到进程退出 →
// 写四处凭据（state.vscdb / 凭据库 / 本机与 WSL 回退文件）→ 按当前接管设置重新打开。
func (m *Manager) Switch(ctx context.Context, id string) error {
	m.switchMu.Lock()
	defer m.switchMu.Unlock()

	a, ok := m.store.get(id)
	if !ok {
		return i18n.E("没有这个账号", "No such account")
	}
	token, err := m.ensureFresh(ctx, m.client(), a.Token, time.Now())
	if err != nil {
		if strings.Contains(err.Error(), "invalid_grant") {
			_ = m.store.setStatus(id, "invalid_grant")
		}
		return err
	}
	if err := m.store.saveToken(id, token); err != nil {
		return err
	}
	a.Token = token

	if m.stopIDE == nil {
		return i18n.E("这个平台还不支持切换反重力账号", "Switching Antigravity accounts is not supported on this platform yet")
	}
	exePath := m.snapshotExePath()
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
	slog.Info("反重力账号已切换", "email", a.Email, logx.FileOnly)
	if m.startIDE != nil {
		if err := m.startIDE(); err != nil {
			return i18n.Wrap("凭据已写入，但重新打开反重力失败：", "Credentials were written, but reopening Antigravity failed: ", err)
		}
	}
	return nil
}

func (m *Manager) writeCreds(a Account, exePath string) error {
	if m.applyCreds != nil {
		return m.applyCreds(a)
	}
	return applyCredentials(a, exePath)
}

// waitIDEStopped 等到主进程消失再写状态库。结束调用返回时进程可能还占着文件，
// 这时写入会被锁拒绝，或被退出前的最后一次回写盖掉。
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
	return i18n.E("反重力还没退出，没有写入凭据。请先手动关掉它再切换。", "Antigravity has not exited, so credentials were not written. Close it and switch again.")
}

// applyCredentials 按当前版本与模式写入凭据：
// - 存在的 state.vscdb（Antigravity 1.x / Antigravity IDE / 便携版）；
// - 系统凭据库（Antigravity 2.x 首要真理源）；
// - 本机 ~/.gemini/jetski-standalone-oauth-token 与（Windows WSL 模式下）WSL 内 ~/.gemini 回退文件。
func applyCredentials(a Account, exePath string) error {
	var sqliteOK, keyringOK, fileOK, wslOK bool
	for _, dbPath := range hostCandidateStateDBPaths(exePath) {
		if _, err := os.Stat(dbPath); err == nil {
			if err := injectAccount(dbPath, a); err != nil {
				return err
			}
			sqliteOK = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	keyringErr := keyringWrite(a)
	if keyringErr != nil {
		slog.Warn("写系统凭据库失败，尝试继续写文件凭据", "error", keyringErr.Error(), logx.FileOnly)
	} else {
		keyringOK = true
	}

	fileErr := writeFallbackFile(a)
	if fileErr != nil {
		slog.Warn("写本机文件凭据回退失败", "error", fileErr.Error(), logx.FileOnly)
	} else {
		fileOK = true
	}

	if distro, wslGemini := activeWSLGeminiDir(); distro != "" {
		if wslGemini == "" {
			return i18n.Ef("找不到 WSL 发行版 %s 的用户目录，请先确认该发行版已初始化。", "Cannot locate the home directory in WSL distro %s; make sure the distro is initialized.", distro)
		}
		if wslErr := writeFallbackFileToDir(wslGemini, a); wslErr != nil {
			return i18n.Wrap("写入 WSL 反重力凭据失败：", "Writing Antigravity credentials into WSL failed: ", wslErr)
		}
		wslOK = true
	}

	if !sqliteOK && !keyringOK && !fileOK && !wslOK {
		if keyringErr != nil {
			return keyringErr
		}
		if fileErr != nil {
			return i18n.Wrap("写入反重力凭据文件失败：", "Writing Antigravity credential file failed: ", fileErr)
		}
		return i18n.E("找不到可写入的反重力凭据位置。先打开一次反重力，再重试切换。", "No writable Antigravity credential location was found. Open Antigravity once, then switch again.")
	}
	return nil
}

// ExportJSON 导出全部账号的 email + refresh_token（备份用，与常见工具格式一致）。
func (m *Manager) ExportJSON() ([]byte, error) {
	type item struct {
		Email        string `json:"email"`
		RefreshToken string `json:"refresh_token"`
	}
	var items []item
	for _, a := range m.store.List() {
		if a.Token.RefreshToken == "" {
			continue
		}
		items = append(items, item{Email: a.Email, RefreshToken: a.Token.RefreshToken})
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
	RefreshToken string
}

type rawImportItem struct {
	Email        string `json:"email"`
	Name         string `json:"name"`
	CustomLabel  string `json:"custom_label"`
	RefreshToken string `json:"refresh_token"`
	Token        *struct {
		RefreshToken string `json:"refresh_token"`
	} `json:"token"`
}

func extractImportEntry(it rawImportItem) (importEntry, bool) {
	rt := strings.TrimSpace(it.RefreshToken)
	if rt == "" && it.Token != nil {
		rt = strings.TrimSpace(it.Token.RefreshToken)
	}
	if rt == "" {
		return importEntry{}, false
	}
	name := strings.TrimSpace(it.CustomLabel)
	if name == "" {
		name = strings.TrimSpace(it.Name)
	}
	return importEntry{
		Email:        strings.TrimSpace(it.Email),
		Name:         name,
		RefreshToken: rt,
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
			return nil, i18n.E("JSON 读不出来：需要含 refresh_token 的账号数组。", "Cannot parse the JSON: expected an array where each item has a refresh_token.")
		}
		for _, it := range items {
			if e, ok := extractImportEntry(it); ok {
				entries = append(entries, e)
			}
		}
	case '{':
		var wrap struct {
			Accounts     json.RawMessage `json:"accounts"`
			RefreshToken string          `json:"refresh_token"`
			Token        json.RawMessage `json:"token"`
		}
		if err := json.Unmarshal(cleaned, &wrap); err != nil {
			return nil, i18n.E("JSON 读不出来：需要 accounts 字段或含 refresh_token 的对象。", "Cannot parse the JSON: expected an accounts field or an object with refresh_token.")
		}
		if len(bytes.TrimSpace(wrap.Accounts)) > 0 {
			acc := bytes.TrimSpace(wrap.Accounts)
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
		} else if wrap.RefreshToken != "" || len(wrap.Token) > 0 {
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
			if strings.HasPrefix(tok, "1//") {
				entries = append(entries, importEntry{RefreshToken: tok})
			}
		}
		if len(entries) == 0 {
			return nil, i18n.E("JSON 读不出来：需要账号数组或逐条的 refresh token（1// 开头）。", "Cannot parse input: expected JSON accounts or refresh tokens starting with 1//.")
		}
	}

	// 按 refresh_token 去重并保持顺序
	seen := make(map[string]bool, len(entries))
	dedup := make([]importEntry, 0, len(entries))
	for _, e := range entries {
		if seen[e.RefreshToken] {
			continue
		}
		seen[e.RefreshToken] = true
		dedup = append(dedup, e)
	}
	if len(dedup) == 0 {
		return nil, i18n.E("输入里没有有效的账号或 refresh token", "No valid accounts or refresh tokens were found")
	}
	return dedup, nil
}

// ImportJSON 批量导入：兼容数组、{"accounts": [...] / {...}}、完整备份结构及多行 1// refresh token。
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
			// 微抖动打散并发请求，避免同一毫秒齐射 Google OAuth。
			if stagger := time.Duration(idx%maxWorkers) * 40 * time.Millisecond; stagger > 0 {
				t := time.NewTimer(stagger)
				select {
				case <-ctx.Done():
					t.Stop()
					out[idx] = ImportResult{Email: entry.Email, OK: false, Error: i18n.Of(ctx.Err())}
					return
				case <-t.C:
				}
			}
			v, ierr := m.importOne(ctx, entry.RefreshToken, entry.Name)
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
