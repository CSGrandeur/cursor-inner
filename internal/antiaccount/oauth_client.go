package antiaccount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"cursor-inner/internal/fsutil"
	"cursor-inner/internal/i18n"
)

const (
	envOAuthClientID   = "CURSOR_INNER_ANTIGRAVITY_CLIENT_ID"
	envOAuthClientCred = "CURSOR_INNER_ANTIGRAVITY_CLIENT_SECRET"
	oauthCacheFile     = "antigravity-oauth-client.json"

	clientIDChunkSize = 1 << 20
	clientIDOverlap   = 4096
	credChunkSize     = 1 << 20
	credOverlap       = 64
)

var (
	reGoogleClientID          = regexp.MustCompile(`[0-9]{10,20}-[a-z0-9]{20,48}\.apps\.googleusercontent\.com`)
	reGoogleClientCredPrimary = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_-]{28}`)
	reGoogleClientCredDelim   = regexp.MustCompile("(?:^|[\x00\"'`\\s])(GOCSPX-[A-Za-z0-9_-]{20,40})(?:$|[\x00\"'`\\s])")
)

type oauthClient struct {
	ID   string
	Cred string
}

type cachedOAuthClient struct {
	ClientID   string `json:"client_id"`
	ClientCred string `json:"client_cred"`
	AsarPath   string `json:"asar_path,omitempty"`
	AsarSize   int64  `json:"asar_size,omitempty"`
	AsarMtime  int64  `json:"asar_mtime,omitempty"`
	LSPath     string `json:"ls_path"`
	LSSize     int64  `json:"ls_size"`
	LSMtime    int64  `json:"ls_mtime"`
}

type resolvedOAuth struct {
	client    oauthClient
	fromCache bool
	fromFixed bool
}

func (m *Manager) resourcesDirs() []string {
	if m.resourcesDirsFn != nil {
		return m.resourcesDirsFn(m.snapshotExePath())
	}
	return nil
}

func (m *Manager) cacheFilePath() string {
	if m.store == nil {
		return ""
	}
	dir := m.store.Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, oauthCacheFile)
}

func fileStatMatches(path string, size, mtime int64) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Size() == size && info.ModTime().UnixNano() == mtime
}

func (m *Manager) loadValidCachedOAuth(needCred bool) *cachedOAuthClient {
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()

	if m.oauthCached == nil {
		p := m.cacheFilePath()
		if p != "" {
			if raw, err := os.ReadFile(p); err == nil {
				var c cachedOAuthClient
				if json.Unmarshal(raw, &c) == nil {
					m.oauthCached = &c
				}
			}
		}
	}
	c := m.oauthCached
	if c == nil || c.ClientID == "" {
		return nil
	}
	if needCred {
		if c.ClientCred == "" || !fileStatMatches(c.LSPath, c.LSSize, c.LSMtime) {
			return nil
		}
		if c.AsarPath != "" && !fileStatMatches(c.AsarPath, c.AsarSize, c.AsarMtime) {
			return nil
		}
		copied := *c
		return &copied
	}
	if c.AsarPath != "" {
		if !fileStatMatches(c.AsarPath, c.AsarSize, c.AsarMtime) {
			return nil
		}
	} else if c.LSPath == "" || !fileStatMatches(c.LSPath, c.LSSize, c.LSMtime) {
		return nil
	}
	copied := *c
	return &copied
}

func (m *Manager) storeCachedOAuth(c *cachedOAuthClient) {
	if c == nil {
		return
	}
	copied := *c
	m.oauthMu.Lock()
	m.oauthCached = &copied
	p := m.cacheFilePath()
	m.oauthMu.Unlock()

	if p == "" {
		return
	}
	raw, err := json.MarshalIndent(copied, "", "  ")
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	if err := fsutil.WriteFile(p, raw); err == nil {
		_ = os.Chmod(p, 0o600)
	}
}

func (m *Manager) invalidateOAuthClientCache() {
	m.oauthMu.Lock()
	m.oauthCached = nil
	p := m.cacheFilePath()
	m.oauthMu.Unlock()
	if p != "" {
		_ = os.Remove(p)
	}
}

// resolveClientID 返回个人版 Antigravity Google 登录使用的 client_id，不走网络。
func (m *Manager) resolveClientID() (string, error) {
	if m.oauthClientOverride != nil && strings.TrimSpace(m.oauthClientOverride.ID) != "" {
		return strings.TrimSpace(m.oauthClientOverride.ID), nil
	}
	if v := strings.TrimSpace(os.Getenv(envOAuthClientID)); v != "" {
		return v, nil
	}
	if c := m.loadValidCachedOAuth(false); c != nil {
		return c.ClientID, nil
	}
	for _, resDir := range m.resourcesDirs() {
		if id, _, _, _, ok := extractPersonalClientID(resDir); ok {
			return id, nil
		}
	}
	return "", i18n.E(
		"未检测到本机安装的 Antigravity（反重力）。请先安装反重力客户端后再登录或刷新账号。",
		"Antigravity was not found on this computer. Install Antigravity before signing in or refreshing accounts.",
	)
}

// resolveOAuthClient 解析并匹配 Antigravity 客户端的 OAuth client_id 与对应凭据。
func (m *Manager) resolveOAuthClient(ctx context.Context, client *http.Client) (resolvedOAuth, error) {
	if m.oauthClientOverride != nil {
		return resolvedOAuth{
			client: oauthClient{
				ID:   strings.TrimSpace(m.oauthClientOverride.ID),
				Cred: strings.TrimSpace(m.oauthClientOverride.Cred),
			},
			fromFixed: true,
		}, nil
	}
	envID := strings.TrimSpace(os.Getenv(envOAuthClientID))
	envCred := strings.TrimSpace(os.Getenv(envOAuthClientCred))
	if envID != "" && envCred != "" {
		return resolvedOAuth{
			client:    oauthClient{ID: envID, Cred: envCred},
			fromFixed: true,
		}, nil
	}
	if c := m.loadValidCachedOAuth(true); c != nil && (envID == "" || c.ClientID == envID) {
		return resolvedOAuth{
			client:    oauthClient{ID: c.ClientID, Cred: c.ClientCred},
			fromCache: true,
		}, nil
	}

	m.oauthResolveMu.Lock()
	defer m.oauthResolveMu.Unlock()

	if c := m.loadValidCachedOAuth(true); c != nil && (envID == "" || c.ClientID == envID) {
		return resolvedOAuth{
			client:    oauthClient{ID: c.ClientID, Cred: c.ClientCred},
			fromCache: true,
		}, nil
	}

	dirs := m.resourcesDirs()
	var (
		clientID, asarPath  string
		asarSize, asarMtime int64
		lsPath              string
	)
	for _, resDir := range dirs {
		id := envID
		var ap string
		var as, am int64
		hasID := id != ""
		if !hasID {
			if extracted, ep, es, em, ok := extractPersonalClientID(resDir); ok {
				id, ap, as, am, hasID = extracted, ep, es, em, true
			}
		}
		lp := findLanguageServer(resDir)
		if hasID && lp != "" {
			clientID, asarPath, asarSize, asarMtime, lsPath = id, ap, as, am, lp
			break
		}
		if clientID == "" && hasID {
			clientID, asarPath, asarSize, asarMtime = id, ap, as, am
		}
		if lsPath == "" && lp != "" {
			lsPath = lp
		}
	}

	if clientID == "" {
		return resolvedOAuth{}, i18n.E(
			"未检测到本机安装的 Antigravity（反重力）。请先安装反重力客户端后再登录或刷新账号。",
			"Antigravity was not found on this computer. Install Antigravity before signing in or refreshing accounts.",
		)
	}
	if lsPath == "" {
		return resolvedOAuth{}, i18n.E(
			"在本机 Antigravity 安装目录中找不到 language_server，无法读取 OAuth 客户端参数。",
			"Could not locate language_server in the local Antigravity installation to read OAuth client parameters.",
		)
	}

	creds, err := scanLanguageServerCreds(lsPath)
	if err != nil {
		return resolvedOAuth{}, err
	}
	if len(creds) == 0 {
		return resolvedOAuth{}, i18n.E(
			"在本机 Antigravity 程序文件中未找到 OAuth 客户端参数，请确认已安装官方最新版反重力。",
			"Could not find OAuth client parameters in the local Antigravity installation; make sure the official Antigravity app is installed.",
		)
	}

	matchedCred, err := matchClientCred(ctx, client, clientID, creds)
	if err != nil {
		return resolvedOAuth{}, err
	}

	lsInfo, err := os.Stat(lsPath)
	if err == nil && envID == "" {
		m.storeCachedOAuth(&cachedOAuthClient{
			ClientID:   clientID,
			ClientCred: matchedCred,
			AsarPath:   asarPath,
			AsarSize:   asarSize,
			AsarMtime:  asarMtime,
			LSPath:     lsPath,
			LSSize:     lsInfo.Size(),
			LSMtime:    lsInfo.ModTime().UnixNano(),
		})
	}
	return resolvedOAuth{
		client: oauthClient{ID: clientID, Cred: matchedCred},
	}, nil
}

func extractPersonalClientID(resDir string) (id, srcPath string, size, mtime int64, ok bool) {
	asarPath := filepath.Join(resDir, "app.asar")
	if info, err := os.Stat(asarPath); err == nil && !info.IsDir() {
		a, b, c, err := scanClientIDFile(asarPath)
		if err == nil {
			if picked := firstNonEmpty(a, b, c); picked != "" {
				return picked, asarPath, info.Size(), info.ModTime().UnixNano(), true
			}
		}
	}

	var bestB, bestBPath string
	var bestC, bestCPath string
	for _, sub := range []string{filepath.Join(resDir, "app", "out"), filepath.Join(resDir, "app", "extensions")} {
		info, err := os.Stat(sub)
		if err != nil || !info.IsDir() {
			continue
		}
		var foundA, foundAPath string
		_ = filepath.WalkDir(sub, func(path string, d os.DirEntry, werr error) error {
			if werr != nil || foundA != "" {
				return nil
			}
			if d.IsDir() {
				if strings.EqualFold(d.Name(), "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(d.Name())) {
			case ".js", ".mjs", ".cjs":
			default:
				return nil
			}
			a, b, c, serr := scanClientIDFile(path)
			if serr != nil {
				return nil
			}
			if a != "" {
				foundA, foundAPath = a, path
				return filepath.SkipAll
			}
			if bestB == "" && b != "" {
				bestB, bestBPath = b, path
			}
			if bestC == "" && c != "" {
				bestC, bestCPath = c, path
			}
			return nil
		})
		if foundA != "" {
			if fi, err := os.Stat(foundAPath); err == nil {
				return foundA, foundAPath, fi.Size(), fi.ModTime().UnixNano(), true
			}
		}
	}
	if bestB != "" {
		if fi, err := os.Stat(bestBPath); err == nil {
			return bestB, bestBPath, fi.Size(), fi.ModTime().UnixNano(), true
		}
	}
	if bestC != "" {
		if fi, err := os.Stat(bestCPath); err == nil {
			return bestC, bestCPath, fi.Size(), fi.ModTime().UnixNano(), true
		}
	}
	return "", "", 0, 0, false
}

func scanClientIDFile(path string) (bestA, bestB, bestC string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()

	signInMarker := []byte("SIGN_IN_CLIENTS")
	aicodeMarker := []byte("https://www.googleapis.com/auth/aicode")
	buf := make([]byte, clientIDOverlap+clientIDChunkSize)
	carry := 0

	for {
		n, rerr := f.Read(buf[carry : carry+clientIDChunkSize])
		if n > 0 {
			data := buf[:carry+n]
			for off := 0; off < len(data); {
				idx := bytes.Index(data[off:], signInMarker)
				if idx < 0 {
					break
				}
				pos := off + idx
				end := pos + 1200
				if end > len(data) {
					end = len(data)
				}
				if m := reGoogleClientID.Find(data[pos:end]); len(m) > 0 {
					return string(m), "", "", nil
				}
				off = pos + len(signInMarker)
			}
			if bestB == "" {
				if idx := bytes.Index(data, aicodeMarker); idx >= 0 {
					start := idx - 600
					if start < 0 {
						start = 0
					}
					if all := reGoogleClientID.FindAll(data[start:idx], -1); len(all) > 0 {
						bestB = string(all[len(all)-1])
					}
				}
			}
			if bestC == "" {
				if m := reGoogleClientID.Find(data); len(m) > 0 {
					bestC = string(m)
				}
			}
			if len(data) > clientIDOverlap {
				copy(buf[:clientIDOverlap], data[len(data)-clientIDOverlap:])
				carry = clientIDOverlap
			} else {
				carry = len(data)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", "", "", rerr
		}
	}
	return "", bestB, bestC, nil
}

func findLanguageServer(resDir string) string {
	if p := findLanguageServerInDir(filepath.Join(resDir, "bin")); p != "" {
		return p
	}
	extRoot := filepath.Join(resDir, "app", "extensions")
	entries, err := os.ReadDir(extRoot)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, sub := range []string{"bin", "dist"} {
			if p := findLanguageServerInDir(filepath.Join(extRoot, e.Name(), sub)); p != "" {
				return p
			}
		}
	}
	return ""
}

func findLanguageServerInDir(dir string) string {
	preferred := []string{
		"language_server.exe.cursor-inner-real",
		"language_server.cursor-inner-real",
		"language_server.exe",
		"language_server",
	}
	for _, name := range preferred {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.HasPrefix(lower, "language_server") {
			continue
		}
		skip := false
		for _, badSuffix := range []string{".cursor-inner-wrap", ".cursor-inner-old", ".cursor-inner-new", ".cursor-inner-bak", ".sig", ".json", ".txt", ".log", ".tmp"} {
			if strings.HasSuffix(lower, badSuffix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		return filepath.Join(dir, e.Name())
	}
	return ""
}

func scanLanguageServerCreds(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		primary     []string
		delim       []string
		seenPrimary = make(map[string]bool)
		seenDelim   = make(map[string]bool)
	)
	buf := make([]byte, credOverlap+credChunkSize)
	carry := 0

	for {
		n, rerr := f.Read(buf[carry : carry+credChunkSize])
		if n > 0 {
			data := buf[:carry+n]
			for _, m := range reGoogleClientCredPrimary.FindAll(data, -1) {
				s := string(m)
				if !seenPrimary[s] {
					seenPrimary[s] = true
					primary = append(primary, s)
				}
			}
			for _, sub := range reGoogleClientCredDelim.FindAllSubmatch(data, -1) {
				if len(sub) < 2 {
					continue
				}
				s := string(sub[1])
				if !seenDelim[s] {
					seenDelim[s] = true
					delim = append(delim, s)
				}
			}
			if len(data) > credOverlap {
				copy(buf[:credOverlap], data[len(data)-credOverlap:])
				carry = credOverlap
			} else {
				carry = len(data)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if len(primary) > 0 {
		return primary, nil
	}
	return delim, nil
}

func matchClientCred(ctx context.Context, client *http.Client, clientID string, creds []string) (string, error) {
	if len(creds) == 0 {
		return "", errors.New("empty candidate client credentials")
	}
	if len(creds) == 1 {
		return creds[0], nil
	}
	for _, c := range creds {
		form := url.Values{}
		form.Set("client_id", clientID)
		form.Set("client_secret", c)
		form.Set("refresh_token", "check")
		form.Set("grant_type", "refresh_token")

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			return "", i18n.Wrap("连不上 Google 授权服务器，请检查代理：", "Cannot reach Google's OAuth server; check the proxy: ", err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			return "", rerr
		}
		if resp.StatusCode == http.StatusOK {
			return c, nil
		}
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		switch e.Error {
		case "invalid_grant":
			return c, nil
		case "invalid_client", "unauthorized_client":
			continue
		default:
			return "", googleError("校验客户端参数", resp.StatusCode, body)
		}
	}
	return "", i18n.E(
		"本机 Antigravity 的 OAuth 客户端参数未能通过 Google 校验，请检查反重力版本或网络代理。",
		"Local Antigravity OAuth client parameters failed Google validation; check your Antigravity version or proxy.",
	)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
