//go:build windows

package cursoraccount

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func localStateAESKey(userDataDir string) ([]byte, error) {
	if strings.TrimSpace(userDataDir) == "" {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(filepath.Join(userDataDir, "Local State"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.OSCrypt.EncryptedKey == "" {
		return nil, errors.New("Local State missing os_crypt.encrypted_key")
	}
	enc, err := base64.StdEncoding.DecodeString(doc.OSCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}
	if len(enc) <= 5 || string(enc[:5]) != "DPAPI" {
		return nil, errors.New("encrypted_key missing DPAPI prefix")
	}
	return dpapiUnprotect(enc[5:])
}

func dpapiUnprotect(cipherBytes []byte) ([]byte, error) {
	if len(cipherBytes) == 0 {
		return nil, errors.New("empty DPAPI blob")
	}
	in := windows.DataBlob{
		Size: uint32(len(cipherBytes)),
		Data: &cipherBytes[0],
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size == 0 || out.Data == nil {
		return nil, errors.New("CryptUnprotectData returned empty data")
	}
	plain := make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, out.Size))
	return plain, nil
}

func decryptSafeStorageBlob(userDataDir string, blob []byte) (string, error) {
	key, err := localStateAESKey(userDataDir)
	if err != nil {
		return "", err
	}
	return decryptAES256GCM(key, blob)
}

func encodePlatformToken(userDataDir, plain string) string {
	key, err := localStateAESKey(userDataDir)
	if err != nil {
		return plain
	}
	enc, err := encryptAES256GCM(key, plain)
	if err != nil {
		return plain
	}
	return enc
}
