package antiaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cursor-inner/internal/dialer"
	"cursor-inner/internal/i18n"
)

// Google OAuth（个人版桌面客户端参数运行期从本机安装的 Antigravity 提取，不在代码库里硬编码）。
const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleUserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
)

var googleTokenURL = "https://oauth2.googleapis.com/token"

// googleScopes 与反重力 IDE 登录时申请的一致。
var googleScopes = strings.Join([]string{
	"openid",
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}, " ")

// authURL 生成授权链接。redirectURI 是本机回环回调。
func authURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", googleScopes)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("include_granted_scopes", "true")
	q.Set("state", state)
	return googleAuthURL + "?" + q.Encode()
}

// httpClient 按当前代理设置构造 Google API 客户端。Google 域名在官方名单里，
// 仅官方与全局都会经代理；代理关着就直连。
func httpClient(d dialer.Func) *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext:         d,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        4,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 20 * time.Second,
		},
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
}

// googleError 把 Google 的错误响应分类成可行动的双语提示；不携带 token 内容。
func googleError(action string, status int, body []byte) error {
	var e struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &e)
	code := e.Error
	if code == "" {
		code = fmt.Sprintf("HTTP %d", status)
	}
	switch code {
	case "invalid_grant":
		return i18n.E("Google 拒绝了这个凭据（invalid_grant）：授权已过期或被撤销，请重新添加这个账号。", "Google rejected this credential (invalid_grant): the grant expired or was revoked. Add this account again.")
	case "invalid_client", "unauthorized_client":
		return i18n.E("Google 不认这个客户端（"+code+"），请检查网络代理是否改写了请求。", "Google does not accept this client ("+code+"); check whether the network proxy rewrote the request.")
	case "access_denied":
		return i18n.E("授权被拒绝（access_denied）。", "Authorization was denied (access_denied).")
	}
	if status == http.StatusTooManyRequests {
		return i18n.E("Google 限流（429），稍后再试。", "Google is rate limiting (429). Try again later.")
	}
	detail := e.ErrorDescription
	if detail == "" {
		detail = code
	}
	return i18n.Ef("%s失败：%s", "%s failed: %s", action, detail)
}

func postForm(ctx context.Context, client *http.Client, form url.Values) (tokenResponse, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, false, i18n.Wrap("连不上 Google 授权服务器，请检查代理：", "Cannot reach Google's OAuth server; check the proxy: ", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, false, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		clientErr := e.Error == "invalid_client" || e.Error == "unauthorized_client"
		return tokenResponse{}, clientErr, googleError("换取凭据", resp.StatusCode, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, false, i18n.Wrap("Google 返回的凭据读不出来：", "Cannot parse Google's token response: ", err)
	}
	if tr.AccessToken == "" {
		return tokenResponse{}, false, i18n.E("Google 没有返回 access token", "Google did not return an access token")
	}
	return tr, false, nil
}

// exchangeCode 用授权码换 token。
func (m *Manager) exchangeCode(ctx context.Context, client *http.Client, code, redirectURI string) (tokenResponse, error) {
	resolved, err := m.resolveOAuthClient(ctx, client)
	if err != nil {
		return tokenResponse{}, err
	}
	send := func(oc oauthClient) (tokenResponse, bool, error) {
		form := url.Values{}
		form.Set("client_id", oc.ID)
		form.Set("client_secret", oc.Cred)
		form.Set("code", code)
		form.Set("redirect_uri", redirectURI)
		form.Set("grant_type", "authorization_code")
		return postForm(ctx, client, form)
	}
	tr, clientErr, err := send(resolved.client)
	if err != nil && clientErr && resolved.fromCache && !resolved.fromFixed {
		m.invalidateOAuthClientCache()
		if fresh, rerr := m.resolveOAuthClient(ctx, client); rerr == nil {
			tr, _, err = send(fresh.client)
		}
	}
	return tr, err
}

// refreshToken 用 refresh token 换新的 access token。invalid_grant 时短退避后
// 再确认一次（Google 偶发误报），仍失败才判定凭据失效；缓存客户端参数被拒时刷新重探一次。
func (m *Manager) refreshToken(ctx context.Context, client *http.Client, refresh string) (tokenResponse, error) {
	resolved, err := m.resolveOAuthClient(ctx, client)
	if err != nil {
		return tokenResponse{}, err
	}
	send := func(oc oauthClient) (tokenResponse, bool, error) {
		form := url.Values{}
		form.Set("client_id", oc.ID)
		form.Set("client_secret", oc.Cred)
		form.Set("refresh_token", refresh)
		form.Set("grant_type", "refresh_token")
		return postForm(ctx, client, form)
	}
	tr, clientErr, err := send(resolved.client)
	if err != nil && clientErr && resolved.fromCache && !resolved.fromFixed {
		m.invalidateOAuthClientCache()
		if fresh, rerr := m.resolveOAuthClient(ctx, client); rerr == nil {
			resolved = fresh
			tr, _, err = send(resolved.client)
		}
	}
	if err != nil && strings.Contains(err.Error(), "invalid_grant") {
		select {
		case <-ctx.Done():
			return tokenResponse{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		tr, _, err = send(resolved.client)
	}
	return tr, err
}

// userInfo 取账号邮箱与显示名。
func userInfo(ctx context.Context, client *http.Client, accessToken string) (email, name string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", i18n.Wrap("取 Google 账号信息失败，请检查代理：", "Fetching the Google account profile failed; check the proxy: ", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", googleError("取账号信息", resp.StatusCode, body)
	}
	var u struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Email == "" {
		return "", "", i18n.E("Google 账号信息里没有邮箱", "Google's profile response has no email")
	}
	return u.Email, u.Name, nil
}

// ensureFresh 保证 token 可用：临期或已过期就刷新。返回的 token 保留原 refresh
// token（Google 刷新时通常不返回新的）。
func (m *Manager) ensureFresh(ctx context.Context, client *http.Client, t Token, now time.Time) (Token, error) {
	if t.fresh(now) {
		return t, nil
	}
	if t.RefreshToken == "" {
		return Token{}, i18n.E("这个账号没有 refresh token，请重新添加。", "This account has no refresh token. Add it again.")
	}
	tr, err := m.refreshToken(ctx, client, t.RefreshToken)
	if err != nil {
		return Token{}, err
	}
	out := Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: t.RefreshToken,
		IDToken:      t.IDToken,
		Expiry:       now.Unix() + tr.ExpiresIn,
	}
	if tr.RefreshToken != "" {
		out.RefreshToken = tr.RefreshToken
	}
	if tr.IDToken != "" {
		out.IDToken = tr.IDToken
	}
	return out, nil
}
