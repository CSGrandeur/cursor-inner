//go:build windows

package winproc

import (
	"syscall"
	"unsafe"
)

// 系统凭据库（Credential Manager）的进程内读写。反重力账号切换用它写
// gemini:antigravity 凭据；与列进程一样不起任何子进程（cmdkey 会闪黑框）。

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

type credentialAttributeW struct {
	keyword   *uint16
	flags     uint32
	valueSize uint32
	value     *byte
}

type credentialW struct {
	flags              uint32
	credType           uint32
	targetName         *uint16
	comment            *uint16
	lastWritten        syscall.Filetime
	credentialBlobSize uint32
	credentialBlob     *byte
	persist            uint32
	attributeCount     uint32
	attributes         *credentialAttributeW
	targetAlias        *uint16
	userName           *uint16
}

// CredWrite 写入（覆盖）一条通用凭据。blob 是原始字节，不做任何编码。
func CredWrite(target, user string, blob []byte) error {
	targetW, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	userW, err := syscall.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	cred := credentialW{
		credType:   credTypeGeneric,
		targetName: targetW,
		persist:    credPersistLocalMachine,
		userName:   userW,
	}
	if len(blob) > 0 {
		cred.credentialBlobSize = uint32(len(blob))
		cred.credentialBlob = &blob[0]
	}
	r, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r == 0 {
		return callErr
	}
	return nil
}

// CredRead 读一条通用凭据；不存在时返回 syscall.ERROR_NOT_FOUND。
func CredRead(target string) ([]byte, error) {
	targetW, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var out *credentialW
	r, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetW)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, callErr
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(out)))
	if out.credentialBlobSize == 0 || out.credentialBlob == nil {
		return []byte{}, nil
	}
	blob := unsafe.Slice(out.credentialBlob, out.credentialBlobSize)
	return append([]byte(nil), blob...), nil
}

// CredDelete 删除一条通用凭据；不存在时返回 syscall.ERROR_NOT_FOUND。
func CredDelete(target string) error {
	targetW, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, callErr := procCredDeleteW.Call(uintptr(unsafe.Pointer(targetW)), credTypeGeneric, 0)
	if r == 0 {
		return callErr
	}
	return nil
}
