package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// releaseKeys 是发版清单的可信签名公钥（ed25519 原始 32 字节的 base64），编译进每一个构建，
// 不靠构建参数注入：从源码构建的任何版本都只认这些密钥签的更新，构建环境漏设变量也不会变成「不验签」。
//
// 轮换（新钥 B 替换旧钥 A）：
//  1. 把 B 加到这里，发一版（仍用 A 签，或 A、B 都签：manifest.json.sig 可以带多个签名）；
//  2. 等用户基本都升级到认识 B 的版本，改为只用 B 签；
//  3. 再从这里删掉 A。
//
// 私钥泄漏时：立即发一版只含新钥的版本（用仍可信的钥签），并在发布说明里提示手动升级。
var releaseKeys = []string{
	"R9XM08RIIjQCWbgcZ/iw+gJYPFnnEqyt3Fxf6QNrT7I=", // key id eb68418a53240b13，2026-10-11 生成
}

// extraKeys 是构建参数追加的公钥（逗号分隔，-X cursor-inner/internal/selfupdate.extraKeys=...），
// 只给本机演练和 fork 用；正式发版不设。
var extraKeys = ""

// TrustedKey 是一把可信公钥。ID 是公钥 sha256 的前 8 字节（16 个十六进制字符），写进签名文件，
// 用来在轮换期挑出对应的公钥。
type TrustedKey struct {
	ID  string
	Pub ed25519.PublicKey
}

// KeyID 算出公钥的 key id。
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// TrustedKeys 返回编译进来的可信公钥。格式不对的条目被跳过并在 error 里说明（其余照常可用）。
func TrustedKeys() ([]TrustedKey, error) {
	all := append([]string{}, releaseKeys...)
	for _, k := range strings.Split(extraKeys, ",") {
		if k = strings.TrimSpace(k); k != "" {
			all = append(all, k)
		}
	}
	return parseKeys(all)
}

func parseKeys(list []string) ([]TrustedKey, error) {
	var keys []TrustedKey
	var bad []string
	seen := map[string]bool{}
	for _, k := range list {
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
		if err != nil || len(b) != ed25519.PublicKeySize {
			bad = append(bad, k)
			continue
		}
		pub := ed25519.PublicKey(b)
		id := KeyID(pub)
		if !seen[id] {
			seen[id] = true
			keys = append(keys, TrustedKey{ID: id, Pub: pub})
		}
	}
	if len(bad) > 0 {
		return keys, fmt.Errorf("内置的更新公钥有 %d 条无效，已忽略", len(bad))
	}
	return keys, nil
}

// 签名文件 manifest.json.sig 的格式：
//
//	{"signatures":[{"keyid":"eb68418a53240b13","sig":"<base64 ed25519 签名>"}]}
//
// 签的是 manifest.json 的原始字节。轮换期可以有多条签名，任一条由可信公钥验过即可。
// 也接受只有一段 base64 签名的旧格式（没有 key id，逐把可信公钥尝试）。
type sigFile struct {
	Signatures []sigEntry `json:"signatures"`
}

type sigEntry struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// 验签失败的几类原因。界面按 ErrorCode 给出对应的说明。
var (
	ErrNoSignature  = errors.New("更新清单没有签名")
	ErrBadSignature = errors.New("更新清单签名校验失败，内容可能被篡改")
	ErrUnknownKey   = errors.New("更新清单不是用可信密钥签的")
	ErrNoKey        = errors.New("此构建没有内置更新公钥，不能自动更新；请到 Releases 页面手动下载")
)

// verifySignature 用可信公钥验 raw 的签名，返回验过的 key id。
func verifySignature(raw, sig []byte, keys []TrustedKey) (string, error) {
	text := strings.TrimSpace(string(sig))
	if text == "" {
		return "", ErrNoSignature
	}
	var f sigFile
	if err := json.Unmarshal([]byte(text), &f); err != nil || len(f.Signatures) == 0 {
		f = sigFile{Signatures: []sigEntry{{Sig: text}}}
	}
	var unknown []string
	for _, e := range f.Signatures {
		s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(e.Sig))
		if err != nil || len(s) != ed25519.SignatureSize {
			continue
		}
		matched := false
		for _, k := range keys {
			if e.KeyID != "" && !strings.EqualFold(e.KeyID, k.ID) {
				continue
			}
			matched = true
			if ed25519.Verify(k.Pub, raw, s) {
				return k.ID, nil
			}
		}
		if !matched {
			unknown = append(unknown, e.KeyID)
		}
	}
	if len(unknown) == len(f.Signatures) {
		return "", fmt.Errorf("%w（key id %s）", ErrUnknownKey, strings.Join(unknown, ", "))
	}
	return "", ErrBadSignature
}
