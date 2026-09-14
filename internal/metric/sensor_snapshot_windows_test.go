// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package metric

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidateSnapshotDescriptorACL(t *testing.T) {
	for name, sddl := range map[string]string{
		"protected administrators and system": `O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;FR;;;BU)`,
		"untrusted delete child":              `O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;DC;;;BU)`,
		"nil dacl":                            `O:BA`,
		"untrusted owner":                     `O:BU D:P(A;;FA;;;BA)(A;;FA;;;SY)`,
	} {
		t.Run(name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			err = validateSnapshotDescriptor(sd, true)
			if name == "protected administrators and system" {
				if err != nil {
					t.Fatalf("valid descriptor rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe descriptor accepted")
			}
		})
	}
}
