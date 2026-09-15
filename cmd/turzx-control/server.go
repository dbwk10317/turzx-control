// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/codex"
	"github.com/dbwk10317/turzx-control/internal/daemon"
	"github.com/dbwk10317/turzx-control/internal/render"
)

//go:embed web/*
var webFiles embed.FS

type claudeLoginSession interface {
	Wait(context.Context) error
	Close()
}

type loginSession interface {
	claudeLoginSession
	AuthURL() string
}

type loginStarter func(context.Context, string, string) (loginSession, error)

type claudeLoginStarter func(context.Context, string, string) (claudeLoginSession, error)
type claudeStatusInstaller func(string, string, string, string) error
type profileLogout func(context.Context, string, string) error
type claudeStatusUninstaller func(string) error
type claudeAccountStatus func(context.Context, string) (claude.Account, error)

type app struct {
	ctx             context.Context
	host            string
	token           string
	codexBin        string
	codexHome       string
	start           loginStarter
	claudeBin       string
	claudeConfigDir string
	claudeStatusBin string
	claudeInboxDir  string
	startClaude     claudeLoginStarter
	installClaude   claudeStatusInstaller
	logoutCodex     profileLogout
	uninstallClaude claudeStatusUninstaller
	confirmClaude   func(string, string, claude.Account) error
	forgetClaude    func(string) error
	statusClaude    claudeAccountStatus
	// Account the current binding was issued for. The statusline payload has no
	// account identity, so a change is only visible by asking the CLI.
	claudeAccount claude.Account
	sources       *daemon.Sources
	display       daemon.DisplayState
	index         *template.Template
	static        http.Handler

	mu            sync.Mutex
	workers       sync.WaitGroup
	stopping      bool
	status        string
	message       string
	claudeStatus  string
	claudeMessage string
}

type state struct {
	CodexStatus   string              `json:"codex_status"`
	CodexMessage  string              `json:"codex_message"`
	ClaudeStatus  string              `json:"claude_status"`
	ClaudeMessage string              `json:"claude_message"`
	Display       daemon.DisplayState `json:"display"`
	Dashboard     *render.Dashboard   `json:"dashboard,omitempty"`
}

func newApp(ctx context.Context, host, codexBin, codexHome, claudeBin, claudeConfigDir, claudeStatusBin, claudeInboxDir string) (*app, error) {
	token, err := sessionToken()
	if err != nil {
		return nil, err
	}
	index, err := template.ParseFS(webFiles, "web/index.html")
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, err
	}
	return &app{
		ctx: ctx, host: host, token: token, codexBin: codexBin, codexHome: codexHome,
		start: func(ctx context.Context, executable, home string) (loginSession, error) {
			return codex.StartChatGPTLogin(ctx, executable, home)
		},
		claudeBin: claudeBin, claudeConfigDir: claudeConfigDir, claudeStatusBin: claudeStatusBin, claudeInboxDir: claudeInboxDir,
		startClaude: func(ctx context.Context, executable, configDir string) (claudeLoginSession, error) {
			return claude.StartLogin(ctx, executable, configDir)
		},
		installClaude: claude.InstallStatusline,
		logoutCodex:   codex.Logout, uninstallClaude: claude.UninstallStatusline,
		confirmClaude: claude.ConfirmStatusline, forgetClaude: claude.ForgetStatuslineConfirmation,
		statusClaude: claude.Status,
		index:        index, static: http.FileServer(http.FS(staticFS)), status: "disconnected", claudeStatus: "disconnected",
	}, nil
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		http.Error(w, "server stopping", http.StatusServiceUnavailable)
		return
	}
	a.workers.Add(1)
	a.mu.Unlock()
	defer a.workers.Done()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Host != a.host {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && !a.validMutation(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := a.index.Execute(w, map[string]string{"Token": a.token}); err != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/api/state":
		a.writeState(w)
	case r.Method == http.MethodPost && r.URL.Path == "/api/codex/login":
		a.startCodexLogin(w)
	case r.Method == http.MethodPost && r.URL.Path == "/api/claude/login":
		a.startClaudeLogin(w)
	case r.Method == http.MethodPost && r.URL.Path == "/api/codex/logout":
		a.startLogout(w, "codex")
	case r.Method == http.MethodPost && r.URL.Path == "/api/claude/logout":
		a.startLogout(w, "claude")
	case r.Method == http.MethodGet && (r.URL.Path == "/style.css" || r.URL.Path == "/app.js"):
		a.static.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *app) close(cancel context.CancelFunc) {
	a.mu.Lock()
	a.stopping = true
	a.mu.Unlock()
	cancel()
	a.workers.Wait()
}

func (a *app) startLogout(w http.ResponseWriter, provider string) {
	a.mu.Lock()
	status := a.status
	if provider == "claude" {
		status = a.claudeStatus
	}
	if status == "starting" || status == "waiting" || status == "disconnecting" {
		a.mu.Unlock()
		http.Error(w, "provider operation already in progress", http.StatusConflict)
		return
	}
	if provider == "claude" {
		a.claudeStatus, a.claudeMessage = "disconnecting", "Claude 연결 해제 중"
	} else {
		a.status, a.message = "disconnecting", "Codex 연결 해제 중"
	}
	a.mu.Unlock()

	if err := a.stopProvider(provider); err != nil {
		log.Printf("stop %s collection: %v", provider, err)
		setState := a.setState
		if provider == "claude" {
			setState = a.setClaudeState
		}
		setState("error", "저장된 연결을 해제하지 못했습니다. 진단 로그를 확인해 주세요.")
		http.Error(w, "connection state unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "disconnecting"})
	a.workers.Add(1)
	go func() { defer a.workers.Done(); a.finishLogout(provider) }()
}

func (a *app) finishLogout(provider string) {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if provider == "claude" {
		// The hook lives in the user's own profile, so disconnecting removes the
		// hook and restores their previous statusline. Signing them out of
		// Claude Code is not ours to do.
		if err := a.uninstallClaude(a.claudeConfigDir); err != nil {
			log.Printf("Claude disconnect failed: %v", err)
			a.setClaudeState("error", "Claude 연결을 완전히 해제하지 못했습니다. 다시 시도해 주세요.")
			return
		}
		a.setClaudeAccount(claude.Account{})
		a.setClaudeState("disconnected", "연결이 해제되었습니다. Claude Code 로그인은 그대로 유지됩니다.")
		return
	}
	if err := a.logoutCodex(ctx, a.codexBin, a.codexHome); err != nil {
		log.Printf("Codex logout failed: %v", err)
		a.setState("error", "Codex 연결을 해제하지 못했습니다. 다시 시도해 주세요.")
		return
	}
	a.setState("disconnected", "연결이 해제되었습니다. 다른 Codex 계정으로 연결할 수 있습니다.")
}

// setClaudeAccount records which account the current binding belongs to.
func (a *app) setClaudeAccount(account claude.Account) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claudeAccount = account
}

func (a *app) currentClaudeAccount() claude.Account {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claudeAccount
}

// bindClaude installs a fresh generation of the statusline hook for account and
// starts accepting its usage. Every connection gets a new binding so values and
// reset history from a previous account cannot carry over.
func (a *app) bindClaude(account claude.Account) error {
	bindingID, err := sessionToken()
	if err != nil {
		return err
	}
	if err := a.installClaude(a.claudeConfigDir, a.claudeStatusBin, a.claudeInboxDir, bindingID); err != nil {
		return err
	}
	if err := a.confirmClaude(a.claudeConfigDir, bindingID, account); err != nil {
		if rollbackErr := a.uninstallClaude(a.claudeConfigDir); rollbackErr != nil {
			log.Printf("Claude statusline rollback failed: %v", rollbackErr)
		}
		return err
	}
	a.setClaudeAccount(account)
	if a.sources != nil {
		a.sources.SetClaude(bindingID)
	}
	return nil
}

func (a *app) startClaudeLogin(w http.ResponseWriter) {
	a.mu.Lock()
	if a.claudeStatus == "starting" || a.claudeStatus == "waiting" || a.claudeStatus == "disconnecting" {
		a.mu.Unlock()
		http.Error(w, "login already in progress", http.StatusConflict)
		return
	}
	a.claudeStatus, a.claudeMessage = "starting", "Claude 연결을 준비하고 있습니다."
	a.mu.Unlock()

	if err := a.stopProvider("claude"); err != nil {
		a.setClaudeState("error", "이전 연결을 해제하지 못했습니다. 진단 로그를 확인해 주세요.")
		http.Error(w, "connection state unavailable", http.StatusServiceUnavailable)
		return
	}
	// The hook targets the profile the user already runs Claude Code with, so a
	// profile that is signed in needs no login at all.
	statusCtx, cancelStatus := context.WithTimeout(a.ctx, profileStatusTimeout)
	account, statusErr := a.statusClaude(statusCtx, a.claudeBin)
	cancelStatus()
	if statusErr == nil && !sameProfile(account.ConfigDirectory, a.claudeConfigDir) {
		statusErr = fmt.Errorf("Claude Code uses %q, not the configured %q", account.ConfigDirectory, a.claudeConfigDir)
	}
	if statusErr != nil {
		log.Printf("Claude auth status failed: %v", statusErr)
		a.setClaudeState("error", "Claude 로그인 상태를 확인하지 못했습니다. 실행 파일과 프로필 경로를 확인하세요.")
		http.Error(w, "claude status unavailable", http.StatusServiceUnavailable)
		return
	}
	if account.LoggedIn {
		if err := a.bindClaude(account); err != nil {
			log.Printf("Claude statusline install failed: %v", err)
			a.setClaudeState("error", "statusline 어댑터를 설치하지 못했습니다. 실행 파일과 설정 경로를 확인하세요.")
			http.Error(w, "statusline unavailable", http.StatusServiceUnavailable)
			return
		}
		a.setClaudeState("installed", claudeInstalledMessage(account))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "installed"})
		return
	}
	bindingID, err := sessionToken()
	if err == nil {
		err = a.installClaude(a.claudeConfigDir, a.claudeStatusBin, a.claudeInboxDir, bindingID)
	}
	if err != nil {
		log.Printf("Claude statusline install failed: %v", err)
		a.setClaudeState("error", "statusline 어댑터를 설치하지 못했습니다. 실행 파일과 설정 경로를 확인하세요.")
		http.Error(w, "statusline unavailable", http.StatusServiceUnavailable)
		return
	}
	session, err := a.startClaude(a.ctx, a.claudeBin, a.claudeConfigDir)
	if err != nil {
		log.Printf("Claude login start failed: %v", err)
		if rollbackErr := a.uninstallClaude(a.claudeConfigDir); rollbackErr != nil {
			log.Printf("Claude statusline rollback failed: %v", rollbackErr)
		}
		a.setClaudeState("error", "Claude 로그인을 시작하지 못했습니다. 실행 파일과 설치 상태를 확인하세요.")
		http.Error(w, "login unavailable", http.StatusServiceUnavailable)
		return
	}
	a.setClaudeState("waiting", "브라우저에서 Claude 로그인을 완료하세요.")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "waiting"})
	a.workers.Add(1)
	go func() { defer a.workers.Done(); a.waitForClaudeLogin(session, bindingID) }()
}

func (a *app) validMutation(r *http.Request) bool {
	return r.Header.Get("Origin") == "http://"+a.host && r.Header.Get("X-TURZX-Token") == a.token
}

func (a *app) startCodexLogin(w http.ResponseWriter) {
	a.mu.Lock()
	if a.status == "starting" || a.status == "waiting" || a.status == "disconnecting" {
		a.mu.Unlock()
		http.Error(w, "login already in progress", http.StatusConflict)
		return
	}
	a.status, a.message = "starting", "Codex 로그인 준비 중"
	a.mu.Unlock()

	_ = a.stopProvider("codex")
	session, err := a.start(a.ctx, a.codexBin, a.codexHome)
	if err != nil {
		log.Printf("Codex login start failed: %v", err)
		a.setState("error", "Codex 로그인을 시작하지 못했습니다. 실행 파일과 설치 상태를 확인하세요.")
		http.Error(w, "login unavailable", http.StatusServiceUnavailable)
		return
	}
	a.setState("waiting", "브라우저에서 로그인을 완료하세요.")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"auth_url": session.AuthURL()})
	a.workers.Add(1)
	go func() { defer a.workers.Done(); a.waitForLogin(session) }()
}

func (a *app) waitForLogin(session loginSession) {
	closeSession := sync.OnceFunc(session.Close)
	defer closeSession()
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	if err := session.Wait(ctx); err != nil {
		if errors.Is(err, context.Canceled) && a.ctx.Err() != nil {
			return
		}
		a.setState("error", "로그인이 완료되지 않았습니다. 다시 연결해 주세요.")
		return
	}
	closeSession()
	// Publish the state first, then start the collector outside a.mu: the
	// collector reports through observedCodex, which takes a.mu itself.
	a.mu.Lock()
	stopping := a.stopping || a.ctx.Err() != nil
	if !stopping {
		a.status, a.message = "connected", "전용 Codex 프로필이 연결되었습니다."
	}
	a.mu.Unlock()
	if !stopping && a.sources != nil {
		a.sources.SetCodex(true)
	}
}

func (a *app) waitForClaudeLogin(session claudeLoginSession, binding string) {
	closeSession := sync.OnceFunc(session.Close)
	defer closeSession()
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	if err := session.Wait(ctx); err != nil {
		if errors.Is(err, context.Canceled) && a.ctx.Err() != nil {
			return
		}
		if rollbackErr := a.uninstallClaude(a.claudeConfigDir); rollbackErr != nil {
			log.Printf("Claude statusline rollback failed: %v", rollbackErr)
		}
		a.setClaudeState("error", "로그인이 완료되지 않았습니다. 다시 연결해 주세요.")
		return
	}
	account, err := a.statusClaude(ctx, a.claudeBin)
	if err == nil {
		err = a.confirmClaude(a.claudeConfigDir, binding, account)
	}
	if err != nil {
		log.Printf("confirm Claude binding: %v", err)
		a.setClaudeState("error", "Claude 로그인은 완료했지만 연결 확인을 저장하지 못했습니다. 다시 연결해 주세요.")
		return
	}
	closeSession()
	a.setClaudeAccount(account)
	if a.sources != nil {
		a.sources.SetClaude(binding)
	}
	a.setClaudeState("installed", claudeInstalledMessage(account))
}

func (a *app) setState(status, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.message = status, message
}

func (a *app) setClaudeState(status, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claudeStatus, a.claudeMessage = status, message
}

func (a *app) writeState(w http.ResponseWriter) {
	a.mu.Lock()
	value := state{
		CodexStatus: a.status, CodexMessage: a.message,
		ClaudeStatus: a.claudeStatus, ClaudeMessage: a.claudeMessage,
		Display: a.display,
	}
	a.mu.Unlock()
	value.Dashboard = a.dashboard()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func sessionToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// claudeInstalledMessage names the bound account when the CLI reported one.
func claudeInstalledMessage(account claude.Account) string {
	if label := account.Label(); label != "" {
		return label + " 계정의 사용량을 수집합니다. 새 Claude Code 세션부터 반영됩니다."
	}
	return "Claude 사용량을 수집합니다. 새 Claude Code 세션부터 반영됩니다."
}

// sameProfile reports whether two profile paths name the same directory.
func sameProfile(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	left, leftErr := filepath.Abs(a)
	right, rightErr := filepath.Abs(b)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
