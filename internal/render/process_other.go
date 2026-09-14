// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package render

import "os/exec"

func configureProcess(*exec.Cmd) {}
