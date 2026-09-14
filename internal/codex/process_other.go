// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package codex

import "os/exec"

func configureCommand(*exec.Cmd) {}
