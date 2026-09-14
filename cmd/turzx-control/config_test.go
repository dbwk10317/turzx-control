// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	base, err := filepath.Abs("profiles")
	if err != nil {
		t.Fatal(err)
	}
	want := settings{
		ListenAddress:   "127.0.0.1:1234",
		CodexBin:        "codex",
		CodexHome:       base,
		ClaudeBin:       "claude",
		ClaudeConfigDir: filepath.Join(base, "claude"),
		ClaudeStatusBin: filepath.Join(base, "turzx-claude-status"),
		ClaudeInboxDir:  filepath.Join(base, "inbox"),
	}
	if err := saveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("settings mismatch: got %#v want %#v", got, want)
	}
	if runtime.GOOS != "windows" {
		if mode := func() os.FileMode { info, _ := os.Stat(path); return info.Mode().Perm() }(); mode != 0o600 {
			t.Fatalf("settings mode = %o, want 600", mode)
		}
	}
}

func TestSaveSettingsRejectsInvalidAndPreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"listen":"127.0.0.1:1234","codex-bin":"codex","codex-home":"/tmp/codex","claude-bin":"claude","claude-config-dir":"/tmp/claude","claude-status-bin":"./adapter","claude-inbox-dir":"/tmp/inbox"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(path, settings{ListenAddress: "localhost:0", CodexBin: ""}); err == nil {
		t.Fatal("expected saveSettings error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("settings file changed on invalid save: got=%q want=%q", got, original)
	}
}

func TestSaveSettingsNormalizesRelativePaths(t *testing.T) {
	base := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	path := filepath.Join(base, "nested", "settings.json")
	want := settings{
		ListenAddress:   "127.0.0.1:0",
		CodexBin:        filepath.FromSlash("tools/codex"),
		CodexHome:       filepath.FromSlash("profiles/codex"),
		ClaudeBin:       "claude",
		ClaudeConfigDir: filepath.FromSlash("profiles/claude"),
		ClaudeStatusBin: filepath.FromSlash("tools/turzx-claude-status"),
		ClaudeInboxDir:  filepath.FromSlash("profiles/inbox"),
	}
	if err := saveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}

	expects := settings{
		ListenAddress:   want.ListenAddress,
		CodexBin:        filepath.Clean(filepath.Join(base, "tools", "codex")),
		CodexHome:       filepath.Clean(filepath.Join(base, "profiles", "codex")),
		ClaudeBin:       "claude",
		ClaudeConfigDir: filepath.Clean(filepath.Join(base, "profiles", "claude")),
		ClaudeStatusBin: filepath.Clean(filepath.Join(base, "tools", "turzx-claude-status")),
		ClaudeInboxDir:  filepath.Clean(filepath.Join(base, "profiles", "inbox")),
	}
	if got != expects {
		t.Fatalf("settings mismatch: got %#v want %#v", got, expects)
	}
}

func TestSaveSettingsOverwritesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	base := t.TempDir()
	first := settings{
		ListenAddress:   "127.0.0.1:1",
		CodexBin:        "codex",
		CodexHome:       filepath.Join(base, "codex1"),
		ClaudeBin:       "claude",
		ClaudeConfigDir: filepath.Join(base, "claude1"),
		ClaudeStatusBin: filepath.Join(base, "adapter1"),
		ClaudeInboxDir:  filepath.Join(base, "inbox1"),
	}
	if err := saveSettings(path, first); err != nil {
		t.Fatal(err)
	}
	second := settings{
		ListenAddress:   "127.0.0.1:2",
		CodexBin:        "codex",
		CodexHome:       filepath.Join(base, "codex2"),
		ClaudeBin:       "claude",
		ClaudeConfigDir: filepath.Join(base, "claude2"),
		ClaudeStatusBin: filepath.Join(base, "adapter2"),
		ClaudeInboxDir:  filepath.Join(base, "inbox2"),
	}
	if err := saveSettings(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := normalizedSettings(second)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("settings overwrite failed: got %#v", got)
	}
}

func TestLoadSettingsRejectsUnknownAndTrailingJSON(t *testing.T) {
	for name, content := range map[string]string{
		"unknown":  `{"nope":"value"}`,
		"trailing": `{"listen":"127.0.0.1:1"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadSettings(path); err == nil || !strings.Contains(err.Error(), "settings") {
				t.Fatalf("loadSettings error = %v", err)
			}
		})
	}
}
