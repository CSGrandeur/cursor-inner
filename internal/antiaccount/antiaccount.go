// Package antiaccount 管理反重力（Google Antigravity）的账号：Google OAuth 登录或
// 导入 refresh token，把凭据写进 IDE 的 state.vscdb、系统凭据库和 ~/.gemini 文件回退，
// 供上层在切换账号后重启 IDE 生效。
//
// 凭据的三处存放与二进制格式对齐反重力自身的读写路径（机制参考 refs/antigravity-manager，
// 代码独立实现）：
//   - state.vscdb（SQLite ItemTable）：键 antigravityUnifiedStateSync.oauthToken /
//     userStatus，值是 base64(protobuf Topic)，Topic 里每个条目是 {key, row{base64(payload)}}；
//   - 系统凭据库：目标 gemini:antigravity，原始 UTF-8 JSON（Windows CredWriteW，
//     macOS security，Linux secret-tool）；
//   - ~/.gemini/jetski-standalone-oauth-token：语言服务在凭据库不可用时的文件回退。
//
// 本包不动进程：结束和重启 IDE 由 antigravitylaunch / app 层负责（接管参数在那边）。
package antiaccount

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Token 是一个账号的 Google OAuth 凭据。Expiry 是 access token 的到期时刻（Unix 秒）。
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	Expiry       int64  `json:"expiry"`
}

// QuotaBucket 是某个模型家族的剩余配额与重置时刻。
type QuotaBucket struct {
	Label   string `json:"label"`
	Percent int    `json:"percent"`
	ResetAt int64  `json:"reset_at,omitempty"`
	Window  string `json:"window,omitempty"`
}

// QuotaInfo 记录最近一次查到的账号订阅档位与配额摘要。
type QuotaInfo struct {
	Tier      string        `json:"tier,omitempty"` // "ULTRA" | "PRO" | "FREE"
	Buckets   []QuotaBucket `json:"buckets,omitempty"`
	Forbidden bool          `json:"forbidden,omitempty"`
	UpdatedAt int64         `json:"updated_at,omitempty"`
}

// Account 是保存下来的一个反重力账号。ID 是随机十六进制，Email 是唯一业务键。
type Account struct {
	ID        string     `json:"id"`
	Email     string     `json:"email"`
	Name      string     `json:"name,omitempty"`
	Token     Token      `json:"token"`
	Quota     *QuotaInfo `json:"quota,omitempty"`
	Status    string     `json:"status,omitempty"`
	CreatedAt int64      `json:"created_at"`
	LastUsed  int64      `json:"last_used"`
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// freshSkew：access token 剩余有效期不足这么多秒就先刷新再写入，
// 免得切完号 IDE 一启动就要自己刷新（网络差时表现为登录失败）。
const freshSkew = 900

func (t Token) fresh(now time.Time) bool {
	return t.AccessToken != "" && t.Expiry > now.Unix()+freshSkew
}
