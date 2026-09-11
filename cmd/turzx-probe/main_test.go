// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "testing"

// Invalid options must fail before opening hardware, even with no device attached.
func TestRejectOptionsBeforeUSB(t *testing.T) {
	for _, args := range [][]string{
		{"-png", "unused.png", "-test-pattern"},
		{"-png", "unused.png", "-h264", "unused.h264"},
		{"-timeout", "0"},
		{"-flush-timeout", "-1ms"},
		{"-timeout", "1ms", "-flush-timeout", "2ms"},
		{"-frame-rate", "0"},
		{"-brightness", "103"},
		{"-queue-timeout", "0"},
		{"-background", "unused.mp4", "-h264", "unused.h264"},
		{"-render-only", "unused.h264"},
		{"-duration", "0"},
		{"-chunk-wait", "0"},
		{"-background", "does-not-exist.mp4"},
		{"unexpected"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("accepted invalid options %v", args)
		}
	}
}
