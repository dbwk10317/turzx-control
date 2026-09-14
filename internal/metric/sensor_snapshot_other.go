// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package metric

import (
	"errors"
	"os"
)

func openSnapshotFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("sensor snapshot must be a regular non-reparse file")
	}
	return os.Open(path)
}

func validateTrustedSnapshotPath(string) error { return nil }

func openSnapshotFileForRead(path string, _ bool) (*os.File, error) { return openSnapshotFile(path) }
