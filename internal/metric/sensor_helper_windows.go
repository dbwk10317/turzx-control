//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
// Uses Go's standard Windows process configuration; no upstream code copied.

package metric

import (
	"os/exec"
	"syscall"
)

func configureSensorCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
}
