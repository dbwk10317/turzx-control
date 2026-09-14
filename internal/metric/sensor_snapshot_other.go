// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package metric

import (
	"errors"
	"fmt"
	"os"
)

// The elevated helper and its protected ProgramData snapshot exist only on
// Windows; elsewhere nothing can vouch for the file, so it is never trusted.
func validateTrustedSnapshotPath(string) error {
	return fmt.Errorf("sensor snapshot: %w", errors.ErrUnsupported)
}

func openSnapshotFileForRead(path string, fixture bool) (*os.File, error) {
	if !fixture {
		return nil, fmt.Errorf("sensor snapshot: %w", errors.ErrUnsupported)
	}
	return os.Open(path)
}
