package cursoraccount

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"cursor-inner/internal/i18n"
)

// loginFlow 是 Cursor PKCE 浏览器登录：生成 verifier / challenge / uuid，
// 打开 cursor.com/loginDeepControl，后台轮询 api2.cursor.sh/auth/poll 等令牌回来。
// 不依赖 cursor:// 协议回调，因此不会打断或激活正在运行的 Cursor。
type loginFlow struct {
	uuid      string
	verifier  string
	challenge string
	authURL   string
	cancelMu  sync.Mutex
	cancelled bool
}

const flowTimeout = 5 * time.Minute

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func beginLogin() (*loginFlow, error) {
	var randBytes [32]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		return nil, err
	}
	uid, err := newUUIDv4()
	if err != nil {
		return nil, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(randBytes[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("uuid", uid)
	q.Set("mode", "login")
	return &loginFlow{
		uuid:      uid,
		verifier:  verifier,
		challenge: challenge,
		authURL:   cursorWebsiteURL + "/loginDeepControl?" + q.Encode(),
	}, nil
}

func (f *loginFlow) cancel() {
	f.cancelMu.Lock()
	f.cancelled = true
	f.cancelMu.Unlock()
}

func (f *loginFlow) isCancelled() bool {
	f.cancelMu.Lock()
	defer f.cancelMu.Unlock()
	return f.cancelled
}

func (f *loginFlow) waitToken(ctx context.Context, client *http.Client) (access, refresh, authID string, err error) {
	deadline := time.Now().Add(flowTimeout)
	pollURL := fmt.Sprintf("%s/auth/poll?uuid=%s&verifier=%s", cursorBackendURL, url.QueryEscape(f.uuid), url.QueryEscape(f.verifier))

	for time.Now().Before(deadline) {
		if f.isCancelled() {
			return "", "", "", i18n.E("授权已取消", "Authorization cancelled")
		}
		acc, ref, aid, ready, perr := f.pollOnce(ctx, client, pollURL)
		if perr != nil {
			return "", "", "", perr
		}
		if ready {
			return acc, ref, aid, nil
		}
		t := time.NewTimer(1500 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return "", "", "", ctx.Err()
		case <-t.C:
		}
	}
	return "", "", "", i18n.E("授权超时，请重新添加 Cursor 账号。", "Authorization timed out. Add the Cursor account again.")
}

func (f *loginFlow) pollOnce(ctx context.Context, client *http.Client, pollURL string) (access, refresh, authID string, ready bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return "", "", "", false, err
	}
	req.Header.Set("User-Agent", "Cursor/3.30")
	resp, err := client.Do(req)
	if err != nil {
		// 短暂网络波动不立刻中止，由外层超时兜底。
		return "", "", "", false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", "", false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", false, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		return "", "", "", false, i18n.E("Cursor 拒绝了本次登录。", "Cursor denied this sign-in attempt.")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", "", false, nil
	}
	var doc struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		AuthID       string `json:"authId"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.AccessToken == "" {
		return "", "", "", false, nil
	}
	ref := doc.RefreshToken
	if ref == "" {
		ref = doc.AccessToken
	}
	return doc.AccessToken, ref, doc.AuthID, true, nil
}
