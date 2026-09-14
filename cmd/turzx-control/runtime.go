// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log"

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
		a.sources.SetClaude(binding)
		a.setClaudeState("installed", "수동 확인한 Claude 계정입니다. 새 statusline 수신 전에는 과거 값으로 표시합니다.")
	} else {
		a.setClaudeState("disconnected", "Claude를 연결하면 확인된 계정의 새 세션 사용량을 표시합니다. 이전 버전 연결은 다시 확인해 주세요.")
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
