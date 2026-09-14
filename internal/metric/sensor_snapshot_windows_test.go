// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package metric

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidateSnapshotDescriptorACL(t *testing.T) {
	for name, sddl := range map[string]string{
		"protected administrators and system": `O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;FR;;;BU)`,
		"deny ace tightens only":              `O:BAD:P(D;;FW;;;WD)(A;;FA;;;BA)(A;;FA;;;SY)(A;;FR;;;BU)`,
		"untrusted delete child":              `O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;0x40;;;BU)`,
		"untrusted write data":                `O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;0x2;;;BU)`,
		"nil dacl":                            `O:BA`,
		"untrusted owner":                     `O:BU D:P(A;;FA;;;BA)(A;;FA;;;SY)`,
	} {
		t.Run(name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			err = validateSnapshotDescriptor(sd, true, writeAccess)
			if name == "protected administrators and system" || name == "deny ace tightens only" {
				if err != nil {
					t.Fatalf("valid descriptor rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe descriptor accepted")
			}
		})
	}
}

// Ancestors of the snapshot directory are held to the smaller replaceAccess
// set: %ProgramData% lets every user create subdirectories, which cannot touch
// an existing protected directory, but delete-child or an untrusted owner can.
func TestValidateSnapshotAncestorDescriptorACL(t *testing.T) {
	for name, sddl := range map[string]string{
		"inherit only creator owner":   `O:BAD:(A;;FA;;;BA)(A;;FA;;;SY)(A;OICIIO;GA;;;CO)`,
		"users may add subdirectories": `O:BAD:(A;;FA;;;BA)(A;;FA;;;SY)(A;;0x1200a9;;;BU)(A;;0x4;;;BU)`,
		"users may delete themselves":  `O:BAD:(A;;FA;;;BA)(A;;FA;;;SY)(A;;SD;;;BU)`,
		"untrusted delete child":       `O:BAD:(A;;FA;;;BA)(A;;FA;;;SY)(A;;0x40;;;BU)`,
		"untrusted dacl write":         `O:BAD:(A;;FA;;;BA)(A;;FA;;;SY)(A;;WD;;;BU)`,
		"untrusted owner":              `O:BUD:(A;;FA;;;BA)(A;;FA;;;SY)`,
	} {
		t.Run(name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			err = validateSnapshotDescriptor(sd, false, replaceAccess)
			switch name {
			case "inherit only creator owner", "users may add subdirectories", "users may delete themselves":
				if err != nil {
					t.Fatalf("valid ancestor rejected: %v", err)
				}
			default:
				if err == nil {
					t.Fatal("unsafe ancestor accepted")
				}
			}
		})
	}
}

// The default installation lives under %ProgramData%, so its real ancestors on
// this machine must pass the ancestor rule: TrustedInstaller owns the volume
// root and both carry inherit-only ACEs for other principals.
func TestValidateSnapshotAncestorsAcceptStockWindowsDirectories(t *testing.T) {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		t.Skip("ProgramData is unavailable")
	}
	for _, dir := range []string{programData, filepath.VolumeName(programData) + string(filepath.Separator)} {
		if err := validateAncestorACL(dir); err != nil {
			t.Errorf("stock ancestor %s rejected: %v", dir, err)
		}
	}
}
