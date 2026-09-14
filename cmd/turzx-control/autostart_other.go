// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package main

import "errors"

func handleAutostart(args []string) (bool, error) {
	for _, arg := range args {
		if arg == "-autostart" || len(arg) > len("-autostart=") && arg[:len("-autostart=")] == "-autostart=" {
			return true, errors.New("autostart is unsupported on this OS")
		}
	}
	return false, nil
}
