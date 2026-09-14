// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var replaceFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")

func atomicReplace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	source, err := syscall.UTF16PtrFromString(tmpName)
	if err != nil {
		return err
	}
	result, _, callErr := replaceFileW.Call(uintptr(unsafe.Pointer(target)), uintptr(unsafe.Pointer(source)), 0, 0, 0, 0)
	if result != 0 {
		return nil
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return os.Rename(tmpName, path)
	}
	return fmt.Errorf("ReplaceFileW: %w", callErr)
}
