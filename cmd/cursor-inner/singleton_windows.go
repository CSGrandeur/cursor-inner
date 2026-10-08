//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func acquireSingleton(dir string) (func(), bool, error) {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Local\cursor-inner-%x`, sum[:8]))
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, false, err
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(handle)
		return nil, true, nil
	}
	return func() { windows.CloseHandle(handle) }, false, nil
}
