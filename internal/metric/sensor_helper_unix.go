// SPDX-License-Identifier: GPL-3.0-or-later
// Uses Go's standard process configuration; no upstream code copied.

//go:build !windows

package metric

import "os/exec"

func configureSensorCommand(cmd *exec.Cmd) {
	// Unix does not require special flags for backgrounded helper processes.
}
