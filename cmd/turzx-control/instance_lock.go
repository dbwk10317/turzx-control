// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func acquireInstance(configDir string) (*os.File, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, errors.New("config dir is empty")
	}
	baseDir := filepath.Join(configDir, "turzx-control")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	lockPath := filepath.Join(baseDir, "instance.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	locked, err := tryLockFile(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire instance lock: %w", err)
	}
	if !locked {
		_ = f.Close()
		return nil, errors.New("turzx-control is already running")
	}
	return f, nil
}
