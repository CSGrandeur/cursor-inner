//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

func acquireSingleton() (func(), bool, error) {
	name, err := windows.UTF16PtrFromString(`Local\cursor-inner`)
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
