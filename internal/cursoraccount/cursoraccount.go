// Package cursoraccount 管理 Cursor 的多账号登录态与配额查询：
// 浏览器 PKCE 登录、导入/导出、令牌刷新、订阅档位与用量查询，
// 以及在退出 Cursor 后按 state.vscdb（含 cursor-safe-storage:v1: 加密）写入凭据并重启生效。
package cursoraccount

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// Token 保存一个 Cursor 账号的访问与刷新凭据。
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Expiry       int64  `json:"expiry,omitempty"`
}

// QuotaBucket 是单个模型或套餐维度的剩余配额与重置时间。
type QuotaBucket struct {
	Label   string `json:"label"`
	Percent int    `json:"percent"`
	ResetAt int64  `json:"reset_at,omitempty"`
	Window  string `json:"window,omitempty"`
}

// QuotaInfo 记录最近一次查询到的订阅档位与配额摘要。
type QuotaInfo struct {
	Tier      string        `json:"tier,omitempty"` // "ULTRA" | "PRO+" | "PRO" | "TRIAL" | "TEAM" | "FREE"
	Buckets   []QuotaBucket `json:"buckets,omitempty"`
	UpdatedAt int64         `json:"updated_at,omitempty"`
}

// Account 是保存下来的一个 Cursor 账号。Email 是唯一业务键。
type Account struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Name       string     `json:"name,omitempty"`
	AuthID     string     `json:"auth_id,omitempty"`
	SignUpType string     `json:"signup_type,omitempty"`
	Token      Token      `json:"token"`
	Quota      *QuotaInfo `json:"quota,omitempty"`
	Status     string     `json:"status,omitempty"`
	CreatedAt  int64      `json:"created_at"`
	LastUsed   int64      `json:"last_used"`
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// freshSkew：access token 剩余有效期不足这么多秒就先刷新，免得切完号刚启动就过期。
const freshSkew = 900

func (t Token) fresh(now time.Time) bool {
	if t.AccessToken == "" {
		return false
	}
	if t.Expiry > 0 {
		return t.Expiry > now.Unix()+freshSkew
	}
	return true
}

// parseJWTClaims 从 JWT 第二段解出 sub、email 与 exp（不校验签名）。
func parseJWTClaims(tok string) (sub, email string, exp int64) {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) < 2 {
		return "", "", 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", "", 0
		}
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return "", "", 0
	}
	return strings.TrimSpace(claims.Sub), normalizeEmail(claims.Email), claims.Exp
}

// normalizePastedToken 兼容裸 JWT、Cookie 原串（WorkosCursorSessionToken=...）
// 以及 `user_xxx::eyJ...` / `%3A%3A` 分隔形式，取出可用令牌。
func normalizePastedToken(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, "WorkosCursorSessionToken="); idx >= 0 {
		s = s[idx+len("WorkosCursorSessionToken="):]
		if semi := strings.IndexByte(s, ';'); semi >= 0 {
			s = s[:semi]
		}
		s = strings.TrimSpace(s)
	}
	if unescaped, err := url.QueryUnescape(s); err == nil && strings.Contains(unescaped, "::") {
		s = unescaped
	}
	if _, after, ok := strings.Cut(s, "::"); ok {
		s = after
	}
	return strings.TrimSpace(s)
}
