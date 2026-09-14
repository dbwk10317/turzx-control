// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package main

import "golang.org/x/sys/windows"

func replaceSettingsFile(source, target string) error {
	return windows.MoveFileEx(windows.StringToUTF16Ptr(source), windows.StringToUTF16Ptr(target), windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
