// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("TURZX_CLAUDE_FAKE") == "1" {
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		os.Exit(20)
	}
	marker := filepath.Join(dir, ".fake-auth")
	args := os.Args
	if len(args) >= 4 && args[1] == "auth" && args[2] == "login" {
		if os.Getenv("TURZX_CLAUDE_CANCEL") == "1" {
			time.Sleep(time.Hour)
		}
		if os.Getenv("TURZX_CLAUDE_FAIL") == "1" {
			os.Exit(3)
		}
		_ = os.WriteFile(marker, []byte("yes"), 0o600)
		os.Exit(0)
	}
	if len(args) >= 3 && args[1] == "auth" && args[2] == "logout" {
		_ = os.Remove(marker)
		os.Exit(0)
	}
	if len(args) >= 4 && args[1] == "auth" && args[2] == "status" {
		_, err := os.Stat(marker)
		_, _ = os.Stdout.Write([]byte(`{"loggedIn":` + map[bool]string{true: "true", false: "false"}[err == nil] + `}`))
		os.Exit(0)
	}
	os.Exit(21)
}

func withClaudeFake(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("TURZX_CLAUDE_FAKE", "1")
	t.Setenv("TURZX_CLAUDE_FAIL", "")
	t.Setenv("TURZX_CLAUDE_CANCEL", "")
	if mode == "fail" {
		t.Setenv("TURZX_CLAUDE_FAIL", "1")
	}
	if mode == "cancel" {
		t.Setenv("TURZX_CLAUDE_CANCEL", "1")
	}
}

func TestProfileLoginAndLogout(t *testing.T) {
	withClaudeFake(t, "")
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "old")
	s, err := StartLogin(context.Background(), os.Args[0], dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := Logout(context.Background(), os.Args[0], dir); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceConfigDirRemovesCaseInsensitiveDuplicates(t *testing.T) {
	env := replaceConfigDir([]string{"A=1", "claude_config_dir=old", "CLAUDE_CONFIG_DIR=older"}, `C:\dedicated`)
	count := 0
	for _, value := range env {
		if key, _, ok := strings.Cut(value, "="); ok && strings.EqualFold(key, "CLAUDE_CONFIG_DIR") {
			count++
			if value != `CLAUDE_CONFIG_DIR=C:\dedicated` {
				t.Fatalf("unexpected config env: %q", value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("CLAUDE_CONFIG_DIR count = %d", count)
	}
}

func TestProfileLoginFailureAndCancel(t *testing.T) {
	withClaudeFake(t, "fail")
	s, err := StartLogin(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(context.Background()); err == nil {
		t.Fatal("expected login failure")
	}
	s.Close()
	withClaudeFake(t, "cancel")
	s, err = StartLogin(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v", err)
	}
	s.Close()
}

func TestProfileStatuslineInstallUninstallPreservesAndDoesNotNest(t *testing.T) {
	dir := t.TempDir()
	settings := map[string]any{"custom": map[string]any{"keep": true}, "statusLine": map[string]any{"type": "command", "command": "orig", "extra": "keep"}}
	b, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(dir, "adapter")
	if err := os.WriteFile(adapter, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InstallStatusline(dir, adapter, filepath.Join(dir, "inbox"), "bind"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err := InstallStatusline(dir, adapter, filepath.Join(dir, "inbox"), "bind"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(first) != string(second) {
		t.Fatal("reinstall changed managed command")
	}
	if err := UninstallStatusline(dir); err != nil {
		t.Fatal(err)
	}
	result, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	var restored map[string]map[string]any
	if err := json.Unmarshal(result, &restored); err != nil {
		t.Fatal(err)
	}
	line := restored["statusLine"]
	if line["command"] != "orig" || line["extra"] != "keep" {
		t.Fatalf("not restored: %s", result)
	}
}

func TestProfileStatuslineInstallRejectsUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	adapter := filepath.Join(dir, "adapter")
	if err := os.WriteFile(adapter, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"statusLine":{"type":"prompt","command":"keep"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallStatusline(dir, adapter, filepath.Join(dir, "inbox"), "bind"); err == nil {
		t.Fatal("unsupported statusLine type accepted")
	}
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, profileSidecar), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallStatusline(dir, adapter, filepath.Join(dir, "inbox"), "bind"); err == nil {
		t.Fatal("malformed sidecar accepted")
	}
}

func TestProfileStatuslineRoundTripsEmptyAndAbsentSettings(t *testing.T) {
	for _, test := range []struct {
		name     string
		settings string
		wantLine bool
	}{
		{name: "empty command", settings: `{"statusLine":{"type":"command","command":"","extra":"keep"}}`, wantLine: true},
		{name: "null", settings: `{"statusLine":null}`, wantLine: true},
		{name: "absent", settings: `{}`, wantLine: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			adapter := filepath.Join(dir, "adapter")
			if err := os.WriteFile(adapter, []byte("test"), 0o700); err != nil {
				t.Fatal(err)
			}
			settingsPath := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(settingsPath, []byte(test.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := InstallStatusline(dir, adapter, filepath.Join(dir, "inbox"), "bind"); err != nil {
				t.Fatal(err)
			}
			if err := UninstallStatusline(dir); err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			body, _ := os.ReadFile(settingsPath)
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			line, ok := got["statusLine"]
			if ok != test.wantLine {
				t.Fatalf("statusLine presence=%v body=%s", ok, body)
			}
			if test.wantLine {
				if test.name == "null" {
					if string(line) != "null" {
						t.Fatalf("statusLine not restored: %s", body)
					}
					return
				}
				var restored map[string]any
				if json.Unmarshal(line, &restored) != nil || restored["extra"] != "keep" || restored["command"] != "" {
					t.Fatalf("statusLine not restored: %s", body)
				}
			}
		})
	}
}

// Event-driven statusline runs go quiet while a session is idle, so the install
// sets a timer. A shorter interval the user already chose is left alone.
func TestInstallStatuslineSetsRefreshInterval(t *testing.T) {
	for name, test := range map[string]struct {
		existing string
		want     float64
	}{
		"no statusline":    {"", statuslineRefreshSeconds},
		"slower existing":  {`{"type":"command","command":"echo hi","refreshInterval":300}`, statuslineRefreshSeconds},
		"faster existing":  {`{"type":"command","command":"echo hi","refreshInterval":5}`, 5},
		"invalid existing": {`{"type":"command","command":"echo hi","refreshInterval":0}`, statuslineRefreshSeconds},
	} {
		t.Run(name, func(t *testing.T) {
			dir, adapter, inbox := t.TempDir(), filepath.Join(t.TempDir(), "adapter"), t.TempDir()
			if err := os.WriteFile(adapter, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.existing != "" {
				settings := []byte(`{"statusLine":` + test.existing + `}`)
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), settings, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := InstallStatusline(dir, adapter, inbox, "binding"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			var settings struct {
				StatusLine struct {
					RefreshInterval float64 `json:"refreshInterval"`
				} `json:"statusLine"`
			}
			if err := json.Unmarshal(raw, &settings); err != nil {
				t.Fatal(err)
			}
			if settings.StatusLine.RefreshInterval != test.want {
				t.Fatalf("refreshInterval = %v, want %v", settings.StatusLine.RefreshInterval, test.want)
			}
		})
	}
}
