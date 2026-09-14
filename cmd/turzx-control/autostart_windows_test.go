// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestQuoteWindowsArg(t *testing.T) {
	if got := syscall.EscapeArg(`C:\Program Files\TURZX\turzx-control.exe`); got != `"C:\Program Files\TURZX\turzx-control.exe"` {
		t.Fatalf("EscapeArg = %q", got)
	}
	if got := syscall.EscapeArg(`C:\TURZX\turzx-control.exe`); got != `C:\TURZX\turzx-control.exe` {
		t.Fatalf("EscapeArg = %q", got)
	}
}

func TestAutostartRegistryPreservesOtherValuesAndDisableIsIdempotent(t *testing.T) {
	root := `Software\TURZXControlTests-` + strings.NewReplacer("/", "-", "\\", "-").Replace(t.Name())
	key, _, err := registry.CreateKey(registry.CURRENT_USER, root, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	key.SetStringValue("Other", "keep")
	command := syscall.EscapeArg(`C:\Program Files\TURZX\turzx-control.exe`)
	if err := key.SetStringValue(autostartValueName, command); err != nil {
		t.Fatal(err)
	}
	key.Close()
	if err := disableAutostart(root, command); err != nil {
		t.Fatal(err)
	}
	key, err = registry.OpenKey(registry.CURRENT_USER, root, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := key.GetStringValue("Other"); err != nil || got != "keep" {
		t.Fatalf("other value = %q, %v", got, err)
	}
	if _, _, err := key.GetStringValue(autostartValueName); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("autostart value = %v", err)
	}
	key.Close()
	if err := disableAutostart(root, command); err != nil {
		t.Fatal(err)
	}
	registry.DeleteKey(registry.CURRENT_USER, root)
}
