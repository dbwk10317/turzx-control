// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/daemon"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

func TestClaudeLogoutStopsCollectionBeforeExternalMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	a, err := newApp(ctx, "127.0.0.1:9123", "missing-codex", dir, "missing-claude", dir, filepath.Join(dir, "adapter"), filepath.Join(dir, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	a.sources = daemon.NewSources(ctx, daemon.SourceOptions{ClaudeInboxDir: a.claudeInboxDir}, nil)
	t.Cleanup(func() { a.close(cancel); a.sources.Close() })
	a.sources.SetClaude("confirmed")
	forgot := false
	a.forgetClaude = func(string) error {
		if got := a.sources.Dashboard().Claude.FiveHour.Status; got != usage.Disconnected {
			t.Fatalf("source still active during invalidation: %s", got)
		}
		forgot = true
		return errors.New("disk unavailable")
	}
	a.uninstallClaude = func(string) error {
		t.Error("statusline removal ran before confirmation invalidation succeeded")
		return nil
	}
	r := httptest.NewRequest(http.MethodPost, "http://"+a.host+"/api/claude/logout", nil)
	r.Header.Set("Origin", "http://"+a.host)
	r.Header.Set("X-TURZX-Token", a.token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if !forgot || w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d forgot=%v", w.Code, forgot)
	}
}

func TestClaudeConfirmationFailureDoesNotConnectCollector(t *testing.T) {
	dir := t.TempDir()
	a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", dir, "claude", dir, filepath.Join(dir, "adapter"), dir)
	if err != nil {
		t.Fatal(err)
	}
	a.confirmClaude = func(string, string, claude.Account) error { return errors.New("disk unavailable") }
	session := &fakeLogin{}
	a.waitForClaudeLogin(session, "binding")
	if a.claudeStatus != "error" || !session.closed.Load() {
		t.Fatalf("status=%s closed=%v", a.claudeStatus, session.closed.Load())
	}
}
