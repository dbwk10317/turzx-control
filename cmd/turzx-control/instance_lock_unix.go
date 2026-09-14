// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

func tryLockFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
