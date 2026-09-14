// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLogin struct {
	result error
	closed atomic.Bool
}

func (f *fakeLogin) AuthURL() string            { return "https://chatgpt.com/auth/test" }
func (f *fakeLogin) Wait(context.Context) error { return f.result }
func (f *fakeLogin) Close()                     { f.closed.Store(true) }

func TestIndexAndHostValidation(t *testing.T) {
	temp := t.TempDir()
	a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", temp, "claude", temp, filepath.Join(temp, "turzx-claude-status.exe"), filepath.Join(temp, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9123/", nil)
	req.Host = a.host
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "TURZX Control") || !strings.Contains(w.Body.String(), a.token) {
		t.Fatalf("unexpected index response: %d %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing content security policy")
	}

	req = httptest.NewRequest(http.MethodGet, "http://evil.test/", nil)
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong host status = %d", w.Code)
	}
}

func TestCodexLoginRequiresOriginAndToken(t *testing.T) {
	temp := t.TempDir()
	a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", temp, "claude", temp, filepath.Join(temp, "turzx-claude-status.exe"), filepath.Join(temp, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	a.start = func(context.Context, string, string) (loginSession, error) {
		starts.Add(1)
		return &fakeLogin{}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/codex/login", nil)
	req.Host = a.host
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || starts.Load() != 0 {
		t.Fatalf("untrusted mutation status=%d starts=%d", w.Code, starts.Load())
	}

	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/codex/login", nil)
	req.Host = a.host
	req.Header.Set("Origin", "http://"+a.host)
	req.Header.Set("X-TURZX-Token", a.token)
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "https://chatgpt.com/auth/test") || starts.Load() != 1 {
		t.Fatalf("trusted mutation status=%d body=%q starts=%d", w.Code, w.Body.String(), starts.Load())
	}

	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		status := a.status
		a.mu.Unlock()
		if status == "connected" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("login status = %q", status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClaudeLoginInstallsStatuslineAndRequiresOriginAndToken(t *testing.T) {
	temp := t.TempDir()
	a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", temp, "claude", temp, filepath.Join(temp, "turzx-claude-status.exe"), filepath.Join(temp, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	var starts, installs atomic.Int32
	a.confirmClaude = func(string, string) error { return nil }
	a.startClaude = func(context.Context, string, string) (claudeLoginSession, error) {
		starts.Add(1)
		return &fakeLogin{}, nil
	}
	a.installClaude = func(configDir, adapterPath, inboxDir, bindingID string) error {
		installs.Add(1)
		if configDir != temp || adapterPath == "" || inboxDir == "" || bindingID == "" {
			t.Fatalf("unexpected install args: %q %q %q %q", configDir, adapterPath, inboxDir, bindingID)
		}
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/claude/login", nil)
	req.Host = a.host
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || starts.Load() != 0 || installs.Load() != 0 {
		t.Fatalf("untrusted mutation status=%d starts=%d installs=%d", w.Code, starts.Load(), installs.Load())
	}

	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/claude/login", nil)
	req.Host = a.host
	req.Header.Set("Origin", "http://"+a.host)
	req.Header.Set("X-TURZX-Token", a.token)
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || starts.Load() != 1 || installs.Load() != 1 {
		t.Fatalf("trusted mutation status=%d starts=%d installs=%d", w.Code, starts.Load(), installs.Load())
	}

	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		status := a.claudeStatus
		a.mu.Unlock()
		if status == "installed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Claude login status = %q", status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProviderLogoutUsesDedicatedProfiles(t *testing.T) {
	temp := t.TempDir()
	claudeConfig := filepath.Join(temp, "claude")
	claudeInbox := filepath.Join(temp, "inbox")
	a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", filepath.Join(temp, "codex"), "claude", claudeConfig, filepath.Join(temp, "turzx-claude-status.exe"), claudeInbox)
	if err != nil {
		t.Fatal(err)
	}
	var codexLogouts, claudeLogouts, uninstalls atomic.Int32
	a.logoutCodex = func(_ context.Context, executable, home string) error {
		codexLogouts.Add(1)
		if executable != "codex" || home != filepath.Join(temp, "codex") {
			t.Fatalf("unexpected Codex logout target: %q %q", executable, home)
		}
		return nil
	}
	a.logoutClaude = func(_ context.Context, executable, home string) error {
		claudeLogouts.Add(1)
		if executable != "claude" || home != claudeConfig {
			t.Fatalf("unexpected Claude logout target: %q %q", executable, home)
		}
		return nil
	}
	a.uninstallClaude = func(home string) error {
		uninstalls.Add(1)
		if home != claudeConfig {
			t.Fatalf("unexpected Claude uninstall target: %q", home)
		}
		return nil
	}

	for _, provider := range []string{"codex", "claude"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/"+provider+"/logout", nil)
		req.Host = a.host
		req.Header.Set("Origin", "http://"+a.host)
		req.Header.Set("X-TURZX-Token", a.token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("%s logout status=%d", provider, w.Code)
		}
	}

	deadline := time.Now().Add(time.Second)
	for codexLogouts.Load() != 1 || claudeLogouts.Load() != 1 || uninstalls.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("codex=%d claude=%d uninstall=%d", codexLogouts.Load(), claudeLogouts.Load(), uninstalls.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLoginIsBlockedWhileDisconnecting(t *testing.T) {
	temp := t.TempDir()
	a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", temp, "claude", temp, filepath.Join(temp, "adapter"), filepath.Join(temp, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	a.status = "disconnecting"
	a.claudeStatus = "disconnecting"
	for _, provider := range []string{"codex", "claude"} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/"+provider+"/login", nil)
		req.Host = a.host
		req.Header.Set("Origin", "http://"+a.host)
		req.Header.Set("X-TURZX-Token", a.token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s login status=%d", provider, w.Code)
		}
	}
}

func TestClaudeLoginFailureRestoresStatusline(t *testing.T) {
	for _, test := range []struct {
		name      string
		startFail bool
	}{
		{name: "start failure", startFail: true},
		{name: "wait failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			temp := t.TempDir()
			a, err := newApp(context.Background(), "127.0.0.1:9123", "codex", temp, "claude", temp, filepath.Join(temp, "adapter"), filepath.Join(temp, "inbox"))
			if err != nil {
				t.Fatal(err)
			}
			var uninstalls atomic.Int32
			a.installClaude = func(string, string, string, string) error { return nil }
			a.uninstallClaude = func(string) error { uninstalls.Add(1); return nil }
			a.startClaude = func(context.Context, string, string) (claudeLoginSession, error) {
				if test.startFail {
					return nil, errors.New("start failed")
				}
				return &fakeLogin{result: errors.New("wait failed")}, nil
			}
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9123/api/claude/login", nil)
			req.Host = a.host
			req.Header.Set("Origin", "http://"+a.host)
			req.Header.Set("X-TURZX-Token", a.token)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, req)
			deadline := time.Now().Add(time.Second)
			for uninstalls.Load() != 1 {
				if time.Now().After(deadline) {
					t.Fatal("statusline was not restored")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
