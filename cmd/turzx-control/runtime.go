// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/daemon"
	"github.com/dbwk10317/turzx-control/internal/render"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

func (a *app) startRuntime(ctx context.Context, display *displaySettings) func() error {
	ctx, cancel := context.WithCancel(ctx)
	opts := daemon.SourceOptions{CodexBin: a.codexBin, CodexHome: a.codexHome, ClaudeInboxDir: a.claudeInboxDir}
	if display != nil {
		opts.SensorHelper, opts.SensorSnapshot, opts.Selection = display.SensorHelper, display.SensorSnapshot, display.Selection
	}
	a.sources = daemon.NewSources(ctx, opts, a.observedCodex)
	binding, err := claude.InstalledBinding(a.claudeConfigDir, a.claudeInboxDir)
	if err != nil {
		log.Printf("restore Claude binding: %v", err)
		a.setClaudeState("error", "저장된 Claude 연결을 확인하지 못했습니다. 다시 연결해 주세요.")
	} else if binding != "" {
		account, accountErr := claude.InstalledAccount(a.claudeConfigDir)
		if accountErr != nil {
			log.Printf("restore Claude account: %v", accountErr)
		}
		a.setClaudeAccount(account)
		a.sources.SetClaude(binding)
		a.setClaudeState("installed", claudeInstalledMessage(account))
		go a.watchClaudeAccount(ctx)
	} else {
		a.setClaudeState("disconnected", "Claude를 연결하면 지금 로그인한 계정의 사용량을 수집합니다.")
		go a.watchClaudeAccount(ctx)
	}
	a.sources.SetCodex(true)
	done := make(chan error, 1)
	if display == nil {
		a.setDisplayState(daemon.DisplayState{Status: "disabled", Message: "패널 출력이 꺼져 있습니다. 배경을 지정해 실행하면 패널을 연결합니다."})
		done <- nil
	} else {
		a.setDisplayState(daemon.DisplayState{Status: "starting", Message: "패널 연결을 준비하고 있습니다."})
		go func() {
			opts, err := display.options()
			if err == nil {
				err = daemon.RunDisplay(ctx, opts, a.sources.Dashboard, a.setDisplayState)
			}
			if err != nil {
				log.Printf("display stopped: %v", err)
				a.setDisplayState(daemon.DisplayState{Status: "error", Message: "출력을 중단했습니다. 진단 로그를 확인한 뒤 다시 실행해 주세요."})
			}
			done <- err
		}()
	}
	return func() error { cancel(); err := <-done; a.sources.Close(); return err }
}

func (a *app) observedCodex(status usage.Status) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping || a.status == "starting" || a.status == "waiting" || a.status == "disconnecting" {
		return
	}
	switch status {
	case usage.OK:
		a.status, a.message = "connected", "전용 Codex 계정을 확인하고 사용량을 수집하고 있습니다."
	case usage.AuthRequired:
		a.status, a.message = "disconnected", "Codex 로그인이 필요합니다."
	case usage.AccountCheckRequired:
		a.status, a.message = "error", "Codex 계정이 변경되었습니다. 다시 연결해 주세요."
	default:
		a.status, a.message = "error", "Codex 사용량을 확인하지 못했습니다. 설치 상태와 진단 로그를 확인해 주세요."
	}
}

func (a *app) setDisplayState(value daemon.DisplayState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.display = value
}

func (a *app) stopProvider(provider string) error {
	if a.sources != nil {
		if provider == "codex" {
			a.sources.SetCodex(false)
		} else {
			a.sources.SetClaude("")
		}
	}
	if provider == "claude" {
		return a.forgetClaude(a.claudeConfigDir)
	}
	return nil
}

func (a *app) dashboard() *render.Dashboard {
	if a.sources == nil {
		return nil
	}
	value := a.sources.Dashboard()
	return &value
}

const (
	profileStatusTimeout = 15 * time.Second
	claudeAccountPeriod  = 30 * time.Second
)

// watchClaudeAccount re-binds when the profile is signed into a different
// account. Claude Code's statusline payload carries no account identity, so
// asking the CLI is the only way to stop one account's usage and reset history
// from landing under another account's binding.
func (a *app) watchClaudeAccount(ctx context.Context) {
	ticker := time.NewTicker(claudeAccountPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		previous := a.currentClaudeAccount()
		if previous.Email == "" {
			// Nothing is bound; connecting is the user's decision, so a fresh
			// login must not install a hook on its own.
			continue
		}
		if a.claudeBusy() {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, profileStatusTimeout)
		account, err := a.statusClaude(checkCtx, a.claudeBin)
		cancel()
		if err != nil {
			log.Printf("Claude account check: %v", err)
			continue
		}
		if previous.SameAccount(account) {
			continue
		}
		if err := a.stopProvider("claude"); err != nil {
			log.Printf("drop Claude binding after account change: %v", err)
		}
		a.setClaudeAccount(claude.Account{})
		if !account.LoggedIn {
			a.setClaudeState("disconnected", "Claude Code에서 로그아웃되었습니다. 다시 로그인한 뒤 연결해 주세요.")
			continue
		}
		if err := a.bindClaude(account); err != nil {
			log.Printf("rebind Claude after account change: %v", err)
			a.setClaudeState("error", "계정이 바뀌었지만 statusline을 다시 설치하지 못했습니다. 다시 연결해 주세요.")
			continue
		}
		a.setClaudeState("installed", "계정이 바뀌어 이전 사용량과 리셋 이력을 지웠습니다. "+claudeInstalledMessage(account))
	}
}

// claudeBusy reports whether a connect or disconnect is in flight.
func (a *app) claudeBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claudeStatus == "starting" || a.claudeStatus == "waiting" || a.claudeStatus == "disconnecting" || a.stopping
}
