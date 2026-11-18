package cursoraccount

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
)

// Cursor 官网与后端公开客户端配置，与桌面版 workbench 一致；测试可覆盖前两个地址。
var (
	cursorWebsiteURL = "https://cursor.com"
	cursorBackendURL = "https://api2.cursor.sh"
	cursorClientID   = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
)

// errSessionExpired 标识服务器明确要求退出 / 拒绝续期的会话，据此把账号标为 expired。
var errSessionExpired = errors.New("cursor session expired")

func httpClient(d dialer.Func) *http.Client {
	return &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{
			DialContext:         d,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        4,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 20 * time.Second,
		},
	}
}

// refreshToken 通过 /oauth/token 换取新的 access token（若返回 shouldLogout 则标记已过期）。
func refreshToken(ctx context.Context, client *http.Client, refreshTok string) (Token, error) {
	payload, _ := json.Marshal(struct {
		GrantType    string `json:"grant_type"`
		ClientID     string `json:"client_id"`
		RefreshToken string `json:"refresh_token"`
	}{
		GrantType:    "refresh_token",
		ClientID:     cursorClientID,
		RefreshToken: refreshTok,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorBackendURL+"/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cursor/3.30")
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, i18n.Wrap("连不上 Cursor 认证服务器，请检查代理：", "Cannot reach Cursor's auth server; check the proxy: ", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	var doc struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ShouldLogout bool   `json:"shouldLogout"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		if resp.StatusCode == http.StatusUnauthorized {
			return Token{}, errExpiredWrap()
		}
		return Token{}, i18n.Ef("Cursor 刷新凭据失败（HTTP %d）", "Cursor token refresh failed (HTTP %d)", resp.StatusCode)
	}
	if doc.ShouldLogout || resp.StatusCode == http.StatusUnauthorized || doc.Error == "invalid_grant" {
		return Token{}, errExpiredWrap()
	}
	if doc.AccessToken == "" {
		return Token{}, i18n.Ef("Cursor 没有返回新的访问凭据（HTTP %d）", "Cursor did not return a new access token (HTTP %d)", resp.StatusCode)
	}
	nextRef := doc.RefreshToken
	if nextRef == "" {
		nextRef = doc.AccessToken
	}
	_, _, exp := parseJWTClaims(doc.AccessToken)
	return Token{
		AccessToken:  doc.AccessToken,
		RefreshToken: nextRef,
		Expiry:       exp,
	}, nil
}

func errExpiredWrap() error {
	return &expiredError{err: i18n.E("Cursor 登录凭据已过期或被撤销，请重新添加账号。", "The Cursor session expired or was revoked. Add this account again.")}
}

type expiredError struct{ err error }

func (e *expiredError) Error() string { return e.err.Error() }
func (e *expiredError) Unwrap() error { return e.err }
func (e *expiredError) Is(target error) bool {
	return target == errSessionExpired
}

func ensureFresh(ctx context.Context, client *http.Client, t Token, now time.Time) (Token, error) {
	if t.fresh(now) {
		return t, nil
	}
	ref := t.RefreshToken
	if ref == "" {
		ref = t.AccessToken
	}
	if ref == "" {
		return Token{}, i18n.E("这个账号没有可用凭据，请重新添加。", "This account has no usable credential. Add it again.")
	}
	return refreshToken(ctx, client, ref)
}
