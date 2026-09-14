// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type canceledLogin struct{ closed chan struct{} }

func (s *canceledLogin) AuthURL() string                { return "https://example.test" }
func (s *canceledLogin) Wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (s *canceledLogin) Close()                         { close(s.closed) }

func TestAppCloseWaitsForLoginAndRejectsNewRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := newApp(ctx, "127.0.0.1:1234", "codex", t.TempDir(), "claude", t.TempDir(), "adapter", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := &canceledLogin{closed: make(chan struct{})}
	a.start = func(context.Context, string, string) (loginSession, error) { return session, nil }
	r := httptest.NewRequest(http.MethodPost, "http://"+a.host+"/api/codex/login", nil)
	r.Header.Set("Origin", "http://"+a.host)
	r.Header.Set("X-TURZX-Token", a.token)
	a.ServeHTTP(httptest.NewRecorder(), r)
	a.close(cancel)
	select {
	case <-session.closed:
	default:
		t.Fatal("login not closed")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}
