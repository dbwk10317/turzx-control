//go:build !windows

// SPDX-License-Identifier: GPL-3.0-or-later

package render

import "os/exec"

func configureProcess(*exec.Cmd) {}
