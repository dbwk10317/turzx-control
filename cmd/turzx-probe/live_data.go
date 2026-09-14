// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/daemon"
	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

// liveDataConfig selects the dedicated provider profiles the probe should read.
// Collection itself is the product daemon's; the probe only adds a JSON log.
type liveDataConfig struct {
	CodexBin, CodexHome           string
	ClaudeInboxDir, ClaudeBinding string
	SensorHelper                  string
	Selection                     metric.HardwareSensorSelection
}

func (c liveDataConfig) validate(theme string) error {
	if strings.TrimSpace(theme) == "" {
		return errors.New("-live-data requires -theme")
	}
	if strings.TrimSpace(c.CodexHome) == "" {
		return errors.New("-live-data requires -codex-home")
	}
	if !filepath.IsAbs(c.CodexHome) {
		return errors.New("-codex-home must be an absolute path")
	}
	if strings.TrimSpace(c.CodexBin) == "" {
		return errors.New("-codex-bin must not be empty")
	}
	if strings.TrimSpace(c.ClaudeInboxDir) == "" || strings.TrimSpace(c.ClaudeBinding) == "" {
		return errors.New("-claude-inbox-dir and -claude-binding are required")
	}
	if !filepath.IsAbs(c.ClaudeInboxDir) {
		return errors.New("-claude-inbox-dir must be an absolute path")
	}
	if err := c.Selection.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.SensorHelper) == "" && c.Selection != (metric.HardwareSensorSelection{}) {
		return errors.New("sensor selection requires -sensor-helper")
	}
	return nil
}

// start runs the daemon's collectors and logs Codex status changes and a
// dashboard sample every minute as JSON lines on log until ctx ends.
func (c liveDataConfig) start(ctx context.Context, log io.Writer) *daemon.Sources {
	enc := json.NewEncoder(log)
	event := func(event string, details any) {
		_ = enc.Encode(struct {
			At      time.Time `json:"at"`
			Step    string    `json:"step"`
			Event   string    `json:"event"`
			Details any       `json:"details,omitempty"`
		}{time.Now(), "live-data", event, details})
	}
	sources := daemon.NewSources(ctx, daemon.SourceOptions{
		CodexBin: c.CodexBin, CodexHome: c.CodexHome, ClaudeInboxDir: c.ClaudeInboxDir,
		SensorHelper: c.SensorHelper, Selection: c.Selection,
	}, func(status usage.Status) { event("codex", status) })
	sources.SetCodex(true)
	sources.SetClaude(c.ClaudeBinding)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				event("dashboard", sources.Dashboard())
			}
		}
	}()
	return sources
}
