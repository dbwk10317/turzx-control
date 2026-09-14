//go:build !windows

// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import (
	"os"
	"path/filepath"
)

func atomicReplace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
