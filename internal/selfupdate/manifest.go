package selfupdate

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ManifestSchema 是本版本能读的清单格式。新增字段（product、expires）都是可选的、向后兼容，
// 旧版本读新清单会忽略它们，所以格式号不变。
const ManifestSchema = 1

// Product 写进每份签名清单，防止同一把钥签的别的东西被当成 cursor-inner 的清单。
const Product = "cursor-inner"

// Manifest 是每个 Release 附带的 manifest.json。
type Manifest struct {
	Schema    int     `json:"schema"`
	Product   string  `json:"product,omitempty"`
	Version   string  `json:"version"`
	Published string  `json:"published,omitempty"`
	Expires   string  `json:"expires,omitempty"` // RFC 3339；过了这个时间清单作废（可选，防止旧清单被长期重放）
	Notes     string  `json:"notes"`
	Assets    []Asset `json:"assets"`
}

// Asset 是某个平台的可执行文件。URLs 可为空，此时按清单所在目录拼出下载地址。
type Asset struct {
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	Name   string   `json:"name"`
	Size   int64    `json:"size"`
	SHA256 string   `json:"sha256"`
	URLs   []string `json:"urls,omitempty"`
}

var (
	ErrNoAsset    = errors.New("清单里没有本平台的文件")
	ErrExpired    = errors.New("更新清单已过期")
	ErrNotProduct = errors.New("更新清单不是 cursor-inner 的")
)

// expirySkew 容忍本机时钟偏差。
const expirySkew = 10 * time.Minute

// ParseManifest 验签并解析清单。sig 是 manifest.json.sig 的内容。
// keys 非空时签名必须由其中一把验过（fail closed），返回验过的 key id；keys 为空（没有内置公钥的构建）
// 时不验签，返回空 key id，调用方据此拒绝安装。
func ParseManifest(raw, sig []byte, keys []TrustedKey, now time.Time) (Manifest, string, error) {
	keyID := ""
	if len(keys) > 0 {
		id, err := verifySignature(raw, sig, keys)
		if err != nil {
			return Manifest{}, "", err
		}
		keyID = id
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, "", fmt.Errorf("清单无法解析：%w", err)
	}
	if m.Schema != ManifestSchema {
		return Manifest{}, "", fmt.Errorf("清单格式 %d 不受支持", m.Schema)
	}
	if keyID != "" && m.Product != Product {
		return Manifest{}, "", fmt.Errorf("%w（product=%q）", ErrNotProduct, m.Product)
	}
	if m.Product != "" && m.Product != Product {
		return Manifest{}, "", fmt.Errorf("%w（product=%q）", ErrNotProduct, m.Product)
	}
	if _, ok := ParseVersion(m.Version); !ok {
		return Manifest{}, "", fmt.Errorf("清单版本号无效：%q", m.Version)
	}
	if m.Expires != "" {
		exp, err := time.Parse(time.RFC3339, m.Expires)
		if err != nil {
			return Manifest{}, "", fmt.Errorf("清单过期时间无效：%q", m.Expires)
		}
		if now.After(exp.Add(expirySkew)) {
			return Manifest{}, "", fmt.Errorf("%w（%s 过期）", ErrExpired, exp.Local().Format("2006-01-02 15:04"))
		}
	}
	platforms := map[string]bool{}
	for _, a := range m.Assets {
		if a.Name == "" || strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." || a.Size <= 0 {
			return Manifest{}, "", fmt.Errorf("清单里的文件条目无效：%q", a.Name)
		}
		if b, err := hex.DecodeString(a.SHA256); err != nil || len(b) != 32 {
			return Manifest{}, "", fmt.Errorf("%s 的 sha256 无效", a.Name)
		}
		for _, u := range a.URLs {
			if p, err := url.Parse(u); err != nil || p.Scheme != "https" {
				return Manifest{}, "", fmt.Errorf("%s 的下载地址必须是 https", a.Name)
			}
		}
		key := a.OS + "/" + a.Arch
		if platforms[key] {
			return Manifest{}, "", fmt.Errorf("清单里 %s 有重复的文件条目", key)
		}
		platforms[key] = true
	}
	return m, keyID, nil
}

// Pick 选出本平台的文件。
func (m Manifest) Pick(goos, goarch string) (Asset, error) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch {
			return a, nil
		}
	}
	return Asset{}, ErrNoAsset
}

// assetURLs 给出下载地址：先取清单同目录下的同名文件（各更新源各用自己的），再补上清单里写的地址
// （给只认清单地址的旧版本用），去重。下载内容都要对上签名清单里的 sha256，地址本身不需要可信。
func assetURLs(a Asset, manifestURL string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	add(sibling(manifestURL, a.Name))
	for _, u := range a.URLs {
		add(u)
	}
	return out
}

// 错误分类，给界面挑说明文字用。
const (
	CodeNoSignature  = "sig_missing"
	CodeBadSignature = "sig_invalid"
	CodeUnknownKey   = "key_unknown"
	CodeExpired      = "expired"
	CodeRollback     = "rollback"
	CodeChecksum     = "checksum"
	CodeNoKey        = "no_key"
	CodeBadManifest  = "bad_manifest"
	CodeNetwork      = "network"
)

// ErrorCode 把错误归到上面某一类；无法归类时返回空串。
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoSignature):
		return CodeNoSignature
	case errors.Is(err, ErrBadSignature):
		return CodeBadSignature
	case errors.Is(err, ErrUnknownKey):
		return CodeUnknownKey
	case errors.Is(err, ErrExpired):
		return CodeExpired
	case errors.Is(err, ErrRollback):
		return CodeRollback
	case errors.Is(err, ErrChecksum):
		return CodeChecksum
	case errors.Is(err, ErrNoKey):
		return CodeNoKey
	case errors.Is(err, ErrNotProduct), errors.Is(err, errBadManifest):
		return CodeBadManifest
	}
	return ""
}
