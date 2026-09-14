// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package main

import "os"

func replaceSettingsFile(source, target string) error { return os.Rename(source, target) }
