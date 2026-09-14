// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunDuplicateDoesNotCreateProfilesOrSaveSettings(t *testing.T) {
	root := isolatedConfig(t)
	lock, err := acquireInstance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	profiles := filepath.Join(t.TempDir(), "profiles")
	err = run(context.Background(), []string{
		"-save-config", "-listen", "127.0.0.1:0",
		"-codex-home", filepath.Join(profiles, "codex"),
		"-claude-config-dir", filepath.Join(profiles, "claude"),
		"-claude-inbox-dir", filepath.Join(profiles, "inbox"),
	})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("duplicate run error = %v", err)
	}
	for _, path := range []string{profiles, settingsPath(root)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("duplicate run changed %s: %v", path, err)
		}
	}
}

func TestRunStartupErrorReleasesInstance(t *testing.T) {
	root := isolatedConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := run(context.Background(), []string{"-listen", listener.Addr().String()}); err == nil {
		t.Fatal("run succeeded with occupied port")
	}
	lock, err := acquireInstance(root)
	if err != nil {
		t.Fatalf("startup error retained lock: %v", err)
	}
	lock.Close()
}
