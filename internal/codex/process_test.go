// SPDX-License-Identifier: GPL-3.0-or-later

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("TURZX_CODEX_FAKE") == "1" {
		fakeServer()
		return
	}
	os.Exit(m.Run())
}

func TestStartReadAndUpdate(t *testing.T) {
	withFake(t, "good")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	select {
	case <-p.Updates():
	case <-time.After(time.Second):
		t.Fatal("missing update notification")
	}
	snapshot, err := p.Read(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 125 || snapshot.FiveHour.RemainingPercent != 0 {
		t.Fatalf("unexpected five-hour window: %#v", snapshot.FiveHour)
	}
	if snapshot.Weekly == nil || snapshot.Weekly.Kind != Weekly {
		t.Fatalf("unexpected weekly window: %#v", snapshot.Weekly)
	}
	if len(snapshot.Other) != 0 {
		t.Fatalf("unexpected other windows: %#v", snapshot.Other)
	}
	if snapshot.Plan != "pro" || snapshot.ReceivedAt.IsZero() {
		t.Fatalf("missing snapshot metadata: %#v", snapshot)
	}
}

func TestChatGPTLogin(t *testing.T) {
	withFake(t, "login-good")
	session, err := StartChatGPTLogin(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.AuthURL() != "https://chatgpt.com/auth/test" {
		t.Fatalf("AuthURL = %q", session.AuthURL())
	}
	if err := session.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestChatGPTLoginFailure(t *testing.T) {
	withFake(t, "login-failed")
	session, err := StartChatGPTLogin(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Wait(context.Background()); !errors.Is(err, errLoginFailed) {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestChatGPTLoginDetectsProcessExit(t *testing.T) {
	withFake(t, "login-exit")
	session, err := StartChatGPTLogin(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Wait(ctx); !errors.Is(err, errProcessStopped) {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestLogoutUsesDedicatedProfileAndVerifiesAccount(t *testing.T) {
	home := t.TempDir()
	withFake(t, "logout-good")
	t.Setenv("TURZX_CODEX_EXPECT_HOME", home)
	if err := Logout(context.Background(), os.Args[0], home); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutPropagatesServerFailure(t *testing.T) {
	withFake(t, "logout-failed")
	if err := Logout(context.Background(), os.Args[0], t.TempDir()); err == nil {
		t.Fatal("logout unexpectedly succeeded")
	}
}

func TestLogoutAcceptsAlreadyLoggedOutProfile(t *testing.T) {
	for _, scenario := range []string{"logout-error-logged-out", "logout-error-unauthenticated"} {
		t.Run(scenario, func(t *testing.T) {
			withFake(t, scenario)
			if err := Logout(context.Background(), os.Args[0], t.TempDir()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthURLValidation(t *testing.T) {
	for _, raw := range []string{
		"http://chatgpt.com/auth",
		"https://chatgpt.com.evil.test/auth",
		"https://login.chatgpt.com/auth",
		"https://auth.openai.com:444/auth",
		"https://user@chatgpt.com/auth",
		"https://openai.com/auth",
	} {
		if validAuthURL(raw) {
			t.Fatalf("accepted auth URL %q", raw)
		}
	}
	if !validAuthURL("https://chatgpt.com/auth") {
		t.Fatal("rejected ChatGPT auth host")
	}
	if !validAuthURL("https://auth.openai.com/oauth/authorize") {
		t.Fatal("rejected official OpenAI auth host")
	}
}

func TestBucketFallbackAndNoFallback(t *testing.T) {
	withFake(t, "fallback")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Read(context.Background(), "codex"); err != nil {
		t.Fatalf("single fallback failed: %v", err)
	}
	withFake(t, "bucket-missing")
	p2, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	if _, err := p2.Read(context.Background(), "codex"); err == nil {
		t.Fatal("missing bucket unexpectedly fell back")
	}
}

func TestAbsentBucketMapUsesSingleFallback(t *testing.T) {
	raw := json.RawMessage(`{"rateLimits":{"limitId":"codex","primary":null,"secondary":{"windowDurationMins":10080,"usedPercent":20,"resetsAt":1700000000}}}`)
	snapshot, err := parseRateLimits(raw, "codex", "pro", time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHour != nil || snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 20 {
		t.Fatalf("unexpected fallback snapshot: %#v", snapshot)
	}
}

func TestSingleFallbackRejectsDifferentBucket(t *testing.T) {
	raw := json.RawMessage(`{"rateLimits":{"limitId":"other","primary":{"windowDurationMins":300,"usedPercent":20,"resetsAt":1700000000}}}`)
	if _, err := parseRateLimits(raw, "codex", "pro", time.Unix(2, 0)); !errors.Is(err, errRateLimitMissing) {
		t.Fatalf("parseRateLimits error = %v", err)
	}
}

func TestAccountChangeAndIdentityFailure(t *testing.T) {
	withFake(t, "changed")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), "codex"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected account change error, got %v", err)
	} else if !IsAccountChanged(err) || IsAuthRequired(err) {
		t.Fatalf("account change classification = %v", err)
	}
	p.Close()

	withFake(t, "null-account")
	if _, err = Start(context.Background(), os.Args[0], t.TempDir()); err == nil || strings.Contains(err.Error(), "@") {
		t.Fatalf("expected non-identifying account error, got %v", err)
	} else if !IsAuthRequired(err) || IsAccountChanged(err) {
		t.Fatalf("auth classification = %v", err)
	}
}

func TestAccountChangeBetweenReadsIsRejected(t *testing.T) {
	withFake(t, "between-polls")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Read(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), "codex"); !errors.Is(err, errAccountChanged) {
		t.Fatalf("second read error = %v", err)
	}
}

func TestMissingPlanDoesNotHideIdentifiedAccount(t *testing.T) {
	withFake(t, "no-plan")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	snapshot, err := p.Read(context.Background(), "codex")
	if err != nil || snapshot.Plan != "" {
		t.Fatalf("snapshot, error = %#v, %v", snapshot, err)
	}
}

func TestReadTimeoutLeavesProcessUsable(t *testing.T) {
	withFake(t, "hang-once")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Read(ctx, "codex"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Read() error = %v, want deadline", err)
	}
	// The request was abandoned, not the child: the next read must succeed.
	if _, err := p.Read(context.Background(), "codex"); err != nil {
		t.Fatalf("Read() after timeout = %v", err)
	}
}

func TestOversizeResponseIsInvalid(t *testing.T) {
	withFake(t, "oversize")
	if _, err := Start(context.Background(), os.Args[0], t.TempDir()); !errors.Is(err, errInvalidResponse) {
		t.Fatalf("Start() error = %v, want invalid response", err)
	}
}

func TestCloseReleasesBlockedRead(t *testing.T) {
	withFake(t, "hang")
	p, err := Start(context.Background(), os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := p.Read(context.Background(), "codex")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	p.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked read succeeded during close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not release blocked Read")
	}
}

func TestReadLineLimitRejectsUnterminatedOversize(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 33)), 8)
	if _, err := readLineLimit(r, 32); !errors.Is(err, errInvalidResponse) {
		t.Fatalf("readLineLimit error = %v", err)
	}
}

func TestParseWindowValidation(t *testing.T) {
	valid := json.RawMessage(`{"primary":{"windowDurationMins":300,"usedPercent":0,"resetsAt":1700000000},"secondary":{"windowDurationMins":10080,"usedPercent":100,"resetsAt":1700000000}}`)
	if _, err := parseWindows(valid); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"primary":{"windowDurationMins":300,"usedPercent":-1,"resetsAt":1}}`,
		`{"primary":{"windowDurationMins":300,"usedPercent":1,"resetsAt":null}}`,
		`{"primary":{"windowDurationMins":300,"usedPercent":1,"resetsAt":1},"secondary":{"windowDurationMins":300,"usedPercent":2,"resetsAt":2}}`,
		`{"primary":{"windowDurationMins":0,"usedPercent":1,"resetsAt":1}}`,
	} {
		if _, err := parseWindows(json.RawMessage(input)); err == nil {
			t.Fatalf("invalid window accepted: %s", input)
		}
	}
	other, err := parseWindows(json.RawMessage(`{"primary":{"windowDurationMins":60,"usedPercent":1,"resetsAt":1}}`))
	if err != nil || len(other) != 1 || other[0].Kind != Other {
		t.Fatalf("unknown positive duration not preserved: %#v, %v", other, err)
	}
}

func withFake(t *testing.T, scenario string) {
	t.Helper()
	t.Setenv("TURZX_CODEX_FAKE", "1")
	t.Setenv("TURZX_CODEX_SCENARIO", scenario)
}

func fakeServer() {
	if len(os.Args) != 3 || os.Args[1] != "app-server" || os.Args[2] != "--stdio" {
		os.Exit(2)
	}
	scenario := os.Getenv("TURZX_CODEX_SCENARIO")
	if expected := os.Getenv("TURZX_CODEX_EXPECT_HOME"); expected != "" && os.Getenv("CODEX_HOME") != expected {
		os.Exit(4)
	}
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	accountReads := 0
	initialized := false
	loggedOut := false
	for in.Scan() {
		var req struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return
		}
		switch req.Method {
		case "initialize":
			if scenario == "oversize" {
				_, _ = io.WriteString(os.Stdout, strings.Repeat("x", maxStdoutLine+1)+"\n")
				return
			}
			_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"ok": true}})
		case "initialized":
			if string(req.Params) != "{}" {
				os.Exit(3)
			}
			initialized = true
			if scenario == "good" {
				_ = out.Encode(map[string]any{"method": "account/updated"})
			}
		case "account/read":
			if !initialized {
				_ = out.Encode(map[string]any{"id": req.ID, "error": map[string]any{"message": "not initialized"}})
				continue
			}
			accountReads++
			if (scenario == "logout-good" || scenario == "logout-error-logged-out") && loggedOut {
				_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"account": nil}})
				continue
			}
			if scenario == "logout-error-unauthenticated" && loggedOut {
				_ = out.Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": "not_authenticated", "message": "not logged in"}})
				continue
			}
			if scenario == "hang" && accountReads > 1 || scenario == "hang-once" && accountReads == 2 {
				continue
			}
			if scenario == "null-account" {
				_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"account": nil}})
				continue
			}
			account := map[string]any{"type": "chatgpt", "email": "person@example.test", "planType": "pro"}
			if scenario == "changed" && accountReads > 1 || scenario == "between-polls" && accountReads > 3 {
				account["email"] = "other@example.test"
			}
			if scenario == "no-plan" {
				delete(account, "planType")
			}
			_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"account": account}})
		case "account/logout":
			if scenario == "logout-failed" || scenario == "logout-error-logged-out" || scenario == "logout-error-unauthenticated" {
				loggedOut = scenario != "logout-failed"
				_ = out.Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": "logout_failed", "message": "no"}})
				continue
			}
			if scenario != "logout-good" {
				_ = out.Encode(map[string]any{"id": req.ID, "error": map[string]any{"code": "unexpected_method", "message": "no"}})
				continue
			}
			loggedOut = true
			_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "account/login/start":
			_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{
				"type": "chatgpt", "loginId": "login-test", "authUrl": "https://chatgpt.com/auth/test",
			}})
			if scenario == "login-exit" {
				return
			}
			success := scenario != "login-failed"
			var loginError any
			if !success {
				loginError = "cancelled"
			}
			_ = out.Encode(map[string]any{"method": "account/login/completed", "params": map[string]any{
				"loginId": "login-test", "success": success, "error": loginError,
			}})
		case "account/rateLimits/read":
			if !initialized {
				_ = out.Encode(map[string]any{"id": req.ID, "error": map[string]any{"message": "not initialized"}})
				continue
			}
			if scenario == "bucket-missing" {
				_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"rateLimitsByLimitId": map[string]any{"other": basicLimits()}}})
			} else if scenario == "fallback" {
				_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"rateLimitsByLimitId": nil, "rateLimits": basicLimits()}})
			} else {
				limits := basicLimits()
				limits["primary"].(map[string]any)["usedPercent"] = 125
				_ = out.Encode(map[string]any{"id": req.ID, "result": map[string]any{"rateLimitsByLimitId": map[string]any{"codex": limits}}})
			}
		}
	}
}

func basicLimits() map[string]any {
	return map[string]any{
		"limitId":   "codex",
		"limitName": nil,
		"primary":   map[string]any{"windowDurationMins": 300, "usedPercent": 10, "resetsAt": 1700000000},
		"secondary": map[string]any{"windowDurationMins": 10080, "usedPercent": 20, "resetsAt": 1700000000},
	}
}

func TestStartRejectsEmptyInputs(t *testing.T) {
	if _, err := Start(context.Background(), "", t.TempDir()); err == nil {
		t.Fatal("empty executable accepted")
	}
	if _, err := Start(context.Background(), filepath.Join(t.TempDir(), "x"), " "); err == nil {
		t.Fatal("empty home accepted")
	}
	if _, err := Start(context.Background(), os.Args[0], "relative-home"); err == nil {
		t.Fatal("relative home accepted")
	}
}

func TestParseAccountSeparatesProtocolFromIdentity(t *testing.T) {
	if _, err := parseAccount(json.RawMessage(`not json`)); !errors.Is(err, errInvalidResponse) || IsAuthRequired(err) {
		t.Fatalf("malformed account/read = %v, want protocol error", err)
	}
	if _, err := parseAccount(json.RawMessage(`{"account":null}`)); !IsAuthRequired(err) {
		t.Fatalf("null account = %v, want auth required", err)
	}
	if _, err := parseAccount(json.RawMessage(`{"account":{"type":"apikey"}}`)); !IsAuthRequired(err) {
		t.Fatalf("non-chatgpt account = %v, want auth required", err)
	}
}
