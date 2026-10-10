package selfupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// publicKey 是发版签名公钥（base64 的 ed25519 公钥），构建时用
// -ldflags "-X cursor-inner/internal/selfupdate.publicKey=..." 注入。
// 注入后清单必须带有效签名；为空时只认 HTTPS 下载的清单里的 sha256（界面标注「未签名」）。
var publicKey = ""

// ManifestSchema 是本版本能读的清单格式。
const ManifestSchema = 1

// Manifest 是每个 Release 附带的 manifest.json。
type Manifest struct {
	Schema    int     `json:"schema"`
	Version   string  `json:"version"`
	Published string  `json:"published,omitempty"`
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
	ErrBadSignature = errors.New("清单签名无效")
	ErrNoSignature  = errors.New("清单缺少签名")
	ErrNoAsset      = errors.New("清单里没有本平台的文件")
)

// ParseManifest 校验签名（配置了公钥时）并解析清单。sig 是 manifest.json.sig 的内容（base64）。
func ParseManifest(raw, sig []byte, pub ed25519.PublicKey) (Manifest, bool, error) {
	signed := false
	if len(pub) > 0 {
		if len(strings.TrimSpace(string(sig))) == 0 {
			return Manifest{}, false, ErrNoSignature
		}
		s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
		if err != nil || !ed25519.Verify(pub, raw, s) {
			return Manifest{}, false, ErrBadSignature
		}
		signed = true
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("清单无法解析：%w", err)
	}
	if m.Schema != ManifestSchema {
		return Manifest{}, false, fmt.Errorf("清单格式 %d 不受支持", m.Schema)
	}
	if _, ok := ParseVersion(m.Version); !ok {
		return Manifest{}, false, fmt.Errorf("清单版本号无效：%q", m.Version)
	}
	for _, a := range m.Assets {
		if a.Name == "" || strings.ContainsAny(a.Name, `/\`) || a.Size <= 0 {
			return Manifest{}, false, fmt.Errorf("清单里的文件条目无效：%q", a.Name)
		}
		if b, err := hex.DecodeString(a.SHA256); err != nil || len(b) != 32 {
			return Manifest{}, false, fmt.Errorf("%s 的 sha256 无效", a.Name)
		}
		for _, u := range a.URLs {
			if p, err := url.Parse(u); err != nil || p.Scheme != "https" {
				return Manifest{}, false, fmt.Errorf("%s 的下载地址必须是 https", a.Name)
			}
		}
	}
	return m, signed, nil
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

// EmbeddedKey 返回构建时注入的公钥；没注入返回 nil。
func EmbeddedKey() (ed25519.PublicKey, error) {
	if publicKey == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("内置的更新公钥无效")
	}
	return ed25519.PublicKey(b), nil
}

// assetURLs 给出下载地址：清单里写了就用清单的，否则用清单同目录下的同名文件。
func assetURLs(a Asset, manifestURL string) []string {
	if len(a.URLs) > 0 {
		return a.URLs
	}
	base, err := url.Parse(manifestURL)
	if err != nil {
		return nil
	}
	ref, _ := url.Parse(url.PathEscape(a.Name))
	return []string{base.ResolveReference(ref).String()}
}
