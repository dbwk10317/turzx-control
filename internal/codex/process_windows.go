// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package codex

import (
	"os/exec"
	"syscall"
)

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
