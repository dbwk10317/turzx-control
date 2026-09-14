// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installBinding(t *testing.T, binding string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	adapter := filepath.Join(dir, "adapter")
	inbox := filepath.Join(dir, "inbox")
	if err := os.WriteFile(adapter, []byte("adapter"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InstallStatusline(dir, adapter, inbox, binding); err != nil {
		t.Fatal(err)
	}
	return dir, inbox
}

func TestInstalledBindingRequiresConfirmation(t *testing.T) {
	dir, inbox := installBinding(t, "bind")
	got, err := InstalledBinding(dir, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("pending binding = %q", got)
	}

	var side managedStatusline
	b, err := os.ReadFile(filepath.Join(dir, profileSidecar))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &side); err != nil {
		t.Fatal(err)
	}
	if side.BindingID != "bind" || side.InboxDir != inbox || side.Confirmed {
		t.Fatalf("unexpected pending sidecar: %+v", side)
	}
}

func TestStatuslineBindingConfirmationAndValidation(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir, inbox := installBinding(t, "bind")
		if err := ConfirmStatusline(dir, "bind"); err != nil {
			t.Fatal(err)
		}
		got, err := InstalledBinding(dir, inbox)
		if err != nil {
			t.Fatal(err)
		}
		if got != "bind" {
			t.Fatalf("confirmed binding = %q", got)
		}
	})

	t.Run("binding mismatch", func(t *testing.T) {
		dir, inbox := installBinding(t, "bind")
		if err := ConfirmStatusline(dir, "other"); err == nil {
			t.Fatal("binding mismatch was accepted")
		}
		got, err := InstalledBinding(dir, inbox)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("mismatched binding = %q", got)
		}
	})

	t.Run("command changed", func(t *testing.T) {
		dir, inbox := installBinding(t, "bind")
		if err := ConfirmStatusline(dir, "bind"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"changed"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := InstalledBinding(dir, inbox)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("changed command binding = %q", got)
		}
	})

	t.Run("inbox mismatch", func(t *testing.T) {
		dir, _ := installBinding(t, "bind")
		if err := ConfirmStatusline(dir, "bind"); err != nil {
			t.Fatal(err)
		}
		got, err := InstalledBinding(dir, filepath.Join(dir, "other-inbox"))
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("mismatched inbox binding = %q", got)
		}
	})
}

func TestForgetStatuslineConfirmation(t *testing.T) {
	dir, inbox := installBinding(t, "bind")
	if err := ConfirmStatusline(dir, "bind"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetStatuslineConfirmation(dir); err != nil {
		t.Fatal(err)
	}
	got, err := InstalledBinding(dir, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("forgotten binding = %q", got)
	}
}

func TestInstalledBindingLegacyAndOversizedSidecar(t *testing.T) {
	t.Run("legacy metadata", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, profileSidecar), []byte(`{"managed_command":"managed","had_statusline":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"managed"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := InstalledBinding(dir, filepath.Join(dir, "inbox"))
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("legacy binding = %q", got)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		dir := t.TempDir()
		body := strings.Repeat("x", profileOutputLimit+1)
		if err := os.WriteFile(filepath.Join(dir, profileSidecar), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InstalledBinding(dir, filepath.Join(dir, "inbox")); err == nil {
			t.Fatal("oversized sidecar accepted")
		}
	})
}
