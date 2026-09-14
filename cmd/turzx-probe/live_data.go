// SPDX-License-Identifier: GPL-3.0-or-later
// Claude statusline and Codex App Server inputs use the local adapters in this repository.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/codex"
	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/render"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

const (
	liveDataQueryTimeout  = 15 * time.Second
	liveDataHardwareTick  = time.Second
	liveDataClaudeTick    = 2 * time.Second
	liveDataCodexTick     = 30 * time.Second
	liveDataSensorTimeout = 2 * time.Second
)

// liveDataConfig is deliberately probe-specific. It wires one dedicated
// provider profile and one exact Claude inbox file; it is not daemon config.
type liveDataConfig struct {
	CodexBin    string
	CodexHome   string
	CodexBucket string

	ClaudeInboxFile string
	ClaudeBinding   string

	SensorHelper string
	Selection    metric.HardwareSensorSelection

	// Tests and future callers may provide the exact scopes. The CLI derives
	// stable probe scopes when these are omitted.
	CodexScope  usage.Scope
	ClaudeScope usage.Scope
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
	if strings.TrimSpace(c.CodexBin) == "" || strings.TrimSpace(c.CodexBucket) == "" {
		return errors.New("-codex-bin and -bucket must not be empty")
	}
	if strings.TrimSpace(c.ClaudeInboxFile) == "" || strings.TrimSpace(c.ClaudeBinding) == "" {
		return errors.New("-claude-inbox-file and -claude-binding are required")
	}
	if !filepath.IsAbs(c.ClaudeInboxFile) {
		return errors.New("-claude-inbox-file must be an absolute path")
	}
	if err := c.Selection.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.SensorHelper) == "" && c.Selection != (metric.HardwareSensorSelection{}) {
		return errors.New("sensor selection requires -sensor-helper")
	}
	return nil
}

func (c liveDataConfig) scopes() (usage.Scope, usage.Scope) {
	codexScope := c.CodexScope
	if codexScope.Provider == "" {
		codexScope = usage.Scope{
			Provider: "codex", Binding: "dedicated", Account: "dedicated", Bucket: c.CodexBucket,
		}
	}
	claudeScope := c.ClaudeScope
	if claudeScope.Provider == "" {
		claudeScope = usage.Scope{
			Provider: "claude", Binding: c.ClaudeBinding, Account: "dedicated", Bucket: "claude",
		}
	}
	return codexScope, claudeScope
}

type liveData struct {
	model       *usage.Model
	codexScope  usage.Scope
	claudeScope usage.Scope

	codex              *codex.Process
	claude             *claude.Receiver
	sensors            *metric.SensorProcess
	selects            metric.HardwareSensorSelection
	hardwareCollector  *metric.Hardware
	claudeBaselineSeen bool

	hardwareMu       sync.RWMutex
	hardwareSnapshot metric.HardwareSnapshot

	logMu sync.Mutex
	log   io.Writer

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// newLiveData starts optional inputs and always leaves a usable hardware/render
// path. Provider/helper startup failures are retained as diagnostics and usage
// status, so a bounded visual probe can still run.
func newLiveData(ctx context.Context, cfg liveDataConfig, log io.Writer) (*liveData, error) {
	if ctx == nil {
		return nil, errors.New("live-data: nil context")
	}
	codexScope, claudeScope := cfg.scopes()
	if err := validateLiveScopes(codexScope, claudeScope); err != nil {
		return nil, err
	}
	receiver, err := claude.NewReceiver(cfg.ClaudeInboxFile, cfg.ClaudeBinding)
	if err != nil {
		return nil, fmt.Errorf("create Claude receiver: %w", err)
	}
	childCtx, cancel := context.WithCancel(ctx)
	d := &liveData{
		model: usage.New(0), codexScope: codexScope, claudeScope: claudeScope,
		claude: receiver, selects: cfg.Selection, hardwareCollector: metric.NewHardware(),
		cancel: cancel, log: log,
	}
	_ = d.model.Update(usage.Update{Scope: codexScope, Status: usage.Collecting, Now: time.Now()})
	_ = d.model.Update(usage.Update{Scope: claudeScope, Status: usage.Collecting, Now: time.Now()})

	if process, startErr := codex.Start(childCtx, cfg.CodexBin, cfg.CodexHome); startErr != nil {
		d.usageError("codex", startErr)
	} else {
		d.codex = process
	}
	if strings.TrimSpace(cfg.SensorHelper) != "" {
		process, startErr := metric.StartSensors(childCtx, cfg.SensorHelper)
		if startErr != nil {
			d.logEvent("hardware", "sensor_start_error", startErr, nil)
		} else {
			d.sensors = process
		}
	}

	d.wg.Add(2)
	go d.hardwareLoop(childCtx)
	go d.claudeLoop(childCtx)
	if d.codex != nil {
		d.wg.Add(1)
		go d.codexLoop(childCtx)
	}
	return d, nil
}

func validateLiveScopes(codexScope, claudeScope usage.Scope) error {
	if codexScope.Provider != "codex" || codexScope.Binding == "" || codexScope.Account == "" || codexScope.Bucket == "" {
		return errors.New("invalid live-data Codex scope")
	}
	if claudeScope.Provider != "claude" || claudeScope.Binding == "" || claudeScope.Account == "" || claudeScope.Bucket == "" {
		return errors.New("invalid live-data Claude scope")
	}
	return nil
}

func (d *liveData) dashboard(at time.Time) render.Dashboard {
	view := d.model.ViewAt(at)
	codexView := view.Scopes[d.codexScope]
	claudeView := view.Scopes[d.claudeScope]
	d.hardwareMu.RLock()
	hardware := d.hardwareSnapshot
	d.hardwareMu.RUnlock()
	return render.DashboardFromSnapshots(at, codexView, claudeView, hardware)
}

func (d *liveData) latestHardware() metric.HardwareSnapshot {
	d.hardwareMu.RLock()
	defer d.hardwareMu.RUnlock()
	return d.hardwareSnapshot
}

func (d *liveData) setHardware(snapshot metric.HardwareSnapshot) {
	d.hardwareMu.Lock()
	d.hardwareSnapshot = snapshot
	d.hardwareMu.Unlock()
}

func (d *liveData) hardwareLoop(ctx context.Context) {
	defer d.wg.Done()
	sensors := d.sensors
	ticker := time.NewTicker(liveDataHardwareTick)
	defer ticker.Stop()
	var samples uint64
	for {
		if ctx.Err() != nil {
			return
		}
		snapshot := d.hardwareCollector.Sample(ctx)
		if sensors != nil {
			sampleCtx, cancel := context.WithTimeout(ctx, liveDataSensorTimeout)
			helperSnapshot, sampleErr := sensors.Sample(sampleCtx)
			cancel()
			if sampleErr != nil {
				d.logEvent("hardware", "sensor_sample_error", sampleErr, nil)
				// SensorProcess closes itself on a failed sample. Drop it so a
				// transient helper failure cannot produce one log line per second.
				sensors.Close()
				sensors = nil
			}
			snapshot = mergeHardwareSnapshot(snapshot, d.selects.Readings(helperSnapshot, sampleErr, time.Now()))
		}
		d.setHardware(snapshot)
		samples++
		if samples%60 == 0 {
			now := time.Now()
			d.logEvent("hardware", "snapshot", nil, struct {
				Snapshot  metric.HardwareSnapshot `json:"snapshot"`
				Dashboard render.Dashboard        `json:"dashboard"`
			}{snapshot, d.dashboard(now)})
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func mergeHardwareSnapshot(snapshot metric.HardwareSnapshot, mapped []metric.Reading) metric.HardwareSnapshot {
	for _, replacement := range mapped {
		for i := range snapshot.Readings {
			if snapshot.Readings[i].ID == replacement.ID {
				snapshot.Readings[i] = replacement
			}
		}
	}
	return snapshot
}

func (d *liveData) claudeLoop(ctx context.Context) {
	defer d.wg.Done()
	d.observeClaude()
	ticker := time.NewTicker(liveDataClaudeTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.observeClaude()
		}
	}
}

func (d *liveData) observeClaude() {
	envelope, fresh, err := d.claude.Observe()
	if err != nil {
		d.usageError("claude", err)
		return
	}
	if envelope.SessionID == "" {
		return
	}
	if !fresh && d.claudeBaselineSeen {
		return
	}
	if !fresh {
		d.claudeBaselineSeen = true
	}
	var update usage.Update
	if fresh {
		update, err = usage.ClaudeUpdate(d.claudeScope, envelope, time.Now(), time.Now())
	} else {
		update, err = usage.ClaudeHistoricalUpdate(d.claudeScope, envelope, time.Now())
	}
	if err != nil {
		d.usageError("claude", err)
		return
	}
	if err := d.model.Update(update); err != nil {
		d.usageError("claude", err)
		return
	}
	d.logEvent("claude", map[bool]string{true: "update", false: "historical"}[fresh], nil, struct {
		SessionID string `json:"session_id"`
		Sequence  uint64 `json:"sequence"`
	}{envelope.SessionID, envelope.Sequence})
}

func (d *liveData) codexLoop(ctx context.Context) {
	defer d.wg.Done()
	read := func() {
		readCtx, cancel := context.WithTimeout(ctx, liveDataQueryTimeout)
		snapshot, err := d.codex.Read(readCtx, d.codexScope.Bucket)
		cancel()
		if err != nil {
			d.usageError("codex", err)
			return
		}
		update, err := usage.CodexUpdate(d.codexScope, snapshot, time.Now())
		if err == nil {
			err = d.model.Update(update)
		}
		if err != nil {
			d.usageError("codex", err)
			return
		}
		d.logEvent("codex", "update", nil, struct {
			ReceivedAt time.Time `json:"received_at"`
		}{snapshot.ReceivedAt})
	}

	read()
	timer := time.NewTimer(liveDataCodexTick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-d.codex.Updates():
			if !open {
				if ctx.Err() == nil {
					d.usageError("codex", errors.New("Codex app-server stopped"))
				}
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			read()
			timer.Reset(liveDataCodexTick)
		case <-timer.C:
			read()
			timer.Reset(liveDataCodexTick)
		}
	}
}

func (d *liveData) usageError(provider string, err error) {
	if err == nil {
		return
	}
	scope := d.codexScope
	if provider == "claude" {
		scope = d.claudeScope
	}
	status := usage.Error
	if provider == "codex" {
		switch {
		case codex.IsAccountChanged(err):
			status = usage.AccountCheckRequired
		case codex.IsAuthRequired(err):
			status = usage.AuthRequired
		}
	}
	_ = d.model.Update(usage.Update{Scope: scope, Status: status, Now: time.Now()})
	d.logEvent(provider, "error", err, nil)
}

func (d *liveData) logEvent(provider, event string, eventErr error, details any) {
	if d.log == nil {
		return
	}
	d.logMu.Lock()
	defer d.logMu.Unlock()
	payload := struct {
		At       time.Time `json:"at"`
		Step     string    `json:"step"`
		Provider string    `json:"provider"`
		Event    string    `json:"event"`
		Error    string    `json:"error,omitempty"`
		Details  any       `json:"details,omitempty"`
	}{time.Now(), "live-data", provider, event, "", details}
	if eventErr != nil {
		payload.Error = eventErr.Error()
	}
	_ = json.NewEncoder(d.log).Encode(payload)
}

func (d *liveData) Close() error {
	d.closeOnce.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
		d.wg.Wait()
		if d.codex != nil {
			d.codex.Close()
		}
		if d.sensors != nil {
			d.sensors.Close()
		}
	})
	return d.closeErr
}
