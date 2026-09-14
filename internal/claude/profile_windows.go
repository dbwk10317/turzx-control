// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package claude

import (
	"os/exec"
	"syscall"
)

func configureProfileProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
func isWindows() bool                       { return true }
