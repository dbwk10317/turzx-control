// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package claude

import "os/exec"

func configureProfileProcess(cmd *exec.Cmd) {}
func isWindows() bool                       { return false }
