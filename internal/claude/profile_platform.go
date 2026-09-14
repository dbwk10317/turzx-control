//go:build !windows

// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import "os/exec"

func configureProfileProcess(cmd *exec.Cmd) {}
func isWindows() bool                       { return false }
