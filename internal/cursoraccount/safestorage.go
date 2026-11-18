package cursoraccount

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Cursor 3.30+ 将 state.vscdb 的 accessToken / refreshToken 用 Electron safeStorage
// 加密存储，格式为 "cursor-safe-storage:v1:" + base64("v10"|"v11" + cipherBlob)；
// 同时源码显式保留对未带该前缀的明文向后兼容。
const safeStoragePrefix = "cursor-safe-storage:v1:"

func decodeStoredToken(userDataDir, stored string) (string, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, safeStoragePrefix) {
		return stored, nil
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, safeStoragePrefix))
	if err != nil {
		return "", err
	}
	return decryptSafeStorageBlob(userDataDir, blob)
}

func encodeStoredToken(userDataDir, plain string) string {
	return encodePlatformToken(userDataDir, plain)
}

// encryptAES256GCM 与 Electron / Chromium Windows os_crypt v10 格式一致：
// "v10" (3B) + random nonce (12B) + AES-256-GCM(ciphertext + 16B tag)。
func encryptAES256GCM(key []byte, plain string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("invalid AES-256 key size %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	out := make([]byte, 0, 3+len(nonce)+len(sealed))
	out = append(out, "v10"...)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return safeStoragePrefix + base64.StdEncoding.EncodeToString(out), nil
}

func decryptAES256GCM(key, blob []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("invalid AES-256 key size %d", len(key))
	}
	if len(blob) < 3 {
		return "", errors.New("safeStorage blob too short")
	}
	ver := string(blob[:3])
	if ver != "v10" && ver != "v11" {
		return "", fmt.Errorf("unexpected safeStorage header %q", ver)
	}
	rest := blob[3:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(rest) <= ns+gcm.Overhead() {
		return "", errors.New("safeStorage ciphertext truncated")
	}
	plain, err := gcm.Open(nil, rest[:ns], rest[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// decryptAES128CBC 用于 macOS / Linux Chromium os_crypt：
// "v10"/"v11" 后接 AES-128-CBC（IV 为 16 个空格 0x20）+ PKCS#7 填充。
func decryptAES128CBC(password string, iter int, blob []byte) (string, error) {
	if len(blob) < 3+aes.BlockSize {
		return "", errors.New("safeStorage blob too short")
	}
	key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), iter, 16)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	ct := blob[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("invalid AES-CBC block length")
	}
	iv := []byte("                ") // 16 * 0x20
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	return unpadPKCS7(plain)
}

func unpadPKCS7(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("empty plaintext")
	}
	pad := int(b[len(b)-1])
	if pad <= 0 || pad > aes.BlockSize || pad > len(b) {
		return "", errors.New("invalid PKCS7 padding")
	}
	for _, v := range b[len(b)-pad:] {
		if int(v) != pad {
			return "", errors.New("invalid PKCS7 padding byte")
		}
	}
	return string(b[:len(b)-pad]), nil
}
