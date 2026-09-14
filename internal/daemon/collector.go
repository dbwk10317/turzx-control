// SPDX-License-Identifier: GPL-3.0-or-later
// Uses the local Claude, Codex and hardware adapters; no upstream code copied.

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/codex"
	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/render"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

type SourceOptions struct {
	CodexBin, CodexHome, ClaudeInboxDir, SensorHelper, SensorSnapshot string
	Selection                                                         metric.HardwareSensorSelection
}

type sourceRun struct {
	model  *usage.Model
	scope  usage.Scope
	cancel context.CancelFunc
	done   chan struct{}
}

// Sources keeps collector work off the rendering path. Each replacement owns a
// new model, so a retired request can never write into the new connection.
type Sources struct {
	ctx           context.Context
	cancel        context.CancelFunc
	opts          SourceOptions
	onCodex       func(usage.Status)
	change        sync.Mutex
	mu            sync.RWMutex
	closed        bool
	serial        uint64
	codex, claude *sourceRun
	hardware      metric.HardwareSnapshot
	hardwareDone  chan struct{}
}

func NewSources(ctx context.Context, opts SourceOptions, onCodex func(usage.Status)) *Sources {
	ctx, cancel := context.WithCancel(ctx)
	s := &Sources{ctx: ctx, cancel: cancel, opts: opts, onCodex: onCodex, hardwareDone: make(chan struct{})}
	s.replace("codex", "", nil)
	s.replace("claude", "", nil)
	go s.hardwareLoop()
	return s
}

func (s *Sources) SetCodex(enabled bool) {
	if !enabled {
		s.replace("codex", "", nil)
		return
	}
	s.replace("codex", "", s.codexLoop)
}

func (s *Sources) SetClaude(binding string) {
	if binding == "" {
		s.replace("claude", "", nil)
		return
	}
	s.replace("claude", binding, s.claudeLoop)
}

func (s *Sources) replace(provider, binding string, collect func(context.Context, *sourceRun)) {
	s.change.Lock()
	defer s.change.Unlock()
	if s.closed {
		return
	}
	s.serial++
	if binding == "" {
		binding = fmt.Sprintf("run-%d", s.serial)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r := &sourceRun{model: usage.New(0), scope: usage.Scope{Provider: provider, Binding: binding, Account: "dedicated", Bucket: provider}, cancel: cancel, done: make(chan struct{})}
	status := usage.Disconnected
	if collect != nil {
		status = usage.Collecting
	}
	_ = r.model.Update(usage.Update{Scope: r.scope, Status: status})
	s.mu.Lock()
	previous := s.codex
	if provider == "codex" {
		s.codex = r
	} else {
		previous, s.claude = s.claude, r
	}
	s.mu.Unlock()
	if previous != nil {
		previous.cancel()
		<-previous.done
	}
	if collect == nil {
		cancel()
		close(r.done)
		return
	}
	go func() { defer close(r.done); collect(ctx, r) }()
}

func (s *Sources) Dashboard() render.Dashboard {
	at := time.Now()
	s.mu.RLock()
	c, cl, hardware := s.codex, s.claude, s.hardware
	s.mu.RUnlock()
	hardware.Readings = append([]metric.Reading(nil), hardware.Readings...)
	for i := range hardware.Readings {
		r := &hardware.Readings[i]
		last := hardware.ObservedAt
		if r.ObservedAt != nil {
			last = *r.ObservedAt
		}
		if r.ReceivedAt != nil {
			last = *r.ReceivedAt
		}
		if r.State == "ok" && (last.IsZero() || at.Sub(last) > 10*time.Second) {
			r.State = "stale"
		}
	}
	return render.DashboardFromSnapshots(at, c.model.ViewAt(at).Scopes[c.scope], cl.model.ViewAt(at).Scopes[cl.scope], hardware)
}

func (s *Sources) Close() {
	s.change.Lock()
	defer s.change.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.cancel()
	<-s.codex.done
	<-s.claude.done
	<-s.hardwareDone
}

func (s *Sources) hardwareLoop() {
	defer close(s.hardwareDone)
	collector := metric.NewHardware()
	var helper *metric.SensorProcess
	var helperErr error
	if s.opts.SensorHelper != "" {
		helper, helperErr = metric.StartSensors(s.ctx, s.opts.SensorHelper)
		if helperErr != nil {
			log.Printf("sensor helper: %v", helperErr)
		}
		if helper != nil {
			defer helper.Close()
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for s.ctx.Err() == nil {
		snapshot := collector.Sample(s.ctx)
		var sample metric.HelperSnapshot
		if s.opts.SensorSnapshot != "" {
			sample, helperErr = metric.ReadSensorSnapshot(s.opts.SensorSnapshot, time.Now())
		} else if helper != nil {
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
			sample, helperErr = helper.Sample(ctx)
			cancel()
			if helperErr != nil {
				helper.Close()
				helper = nil
				log.Printf("sensor sample: %v", helperErr)
			}
		}
		// Retain the helper error after shutdown; a failed sensor never turns into
		// an unsupported or unselected sensor on the next hardware tick.
		for _, replacement := range s.opts.Selection.Readings(sample, helperErr, time.Now()) {
			for i := range snapshot.Readings {
				if snapshot.Readings[i].ID == replacement.ID {
					snapshot.Readings[i] = replacement
				}
			}
		}
		s.mu.Lock()
		s.hardware = snapshot
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Sources) codexLoop(ctx context.Context, r *sourceRun) {
	process, err := codex.Start(ctx, s.opts.CodexBin, s.opts.CodexHome)
	if err != nil {
		s.codexError(ctx, r, err)
		return
	}
	defer process.Close()
	delay := 30 * time.Second
	for ctx.Err() == nil {
		readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		snapshot, err := process.Read(readCtx, "codex")
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			var update usage.Update
			update, err = usage.CodexUpdate(r.scope, snapshot, time.Now())
			if err == nil {
				err = r.model.Update(update)
			}
		}
		if err != nil {
			s.codexError(ctx, r, err)
			if codex.IsAuthRequired(err) || codex.IsAccountChanged(err) {
				return
			}
			delay = min(delay*2, 5*time.Minute)
		} else {
			delay = 30 * time.Second
			if s.onCodex != nil {
				s.onCodex(usage.OK)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case _, open := <-process.Updates():
			timer.Stop()
			if !open {
				s.codexError(ctx, r, errors.New("Codex app-server stopped"))
				return
			}
			// Do not let notifications bypass error backoff.
			if err != nil {
				if !waitSource(ctx, delay) {
					return
				}
			}
		case <-timer.C:
		}
	}
}

func (s *Sources) codexError(ctx context.Context, r *sourceRun, err error) {
	if ctx.Err() != nil {
		return
	}
	status := usage.Error
	if codex.IsAuthRequired(err) {
		status = usage.AuthRequired
	}
	if codex.IsAccountChanged(err) {
		status = usage.AccountCheckRequired
	}
	_ = r.model.Update(usage.Update{Scope: r.scope, Status: status})
	log.Printf("Codex collector: %v", err)
	if s.onCodex != nil {
		s.onCodex(status)
	}
}

type inboxScan struct {
	receivers      map[string]*claude.Receiver
	hasObservation bool
}

func (s *Sources) claudeLoop(ctx context.Context, r *sourceRun) {
	scan := inboxScan{receivers: make(map[string]*claude.Receiver)}
	for ctx.Err() == nil {
		if err := scan.observe(s.opts.ClaudeInboxDir, r, time.Now()); err != nil {
			_ = r.model.Update(usage.Update{Scope: r.scope, Status: usage.Error})
			log.Printf("Claude inbox: %v", err)
		}
		if !waitSource(ctx, time.Second) {
			return
		}
	}
}

func (s *inboxScan) observe(dir string, r *sourceRun, now time.Time) error {
	f, err := os.Open(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, readErr := f.ReadDir(513)
	closeErr := f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) > 512 {
		return errors.New("Claude inbox exceeds 512 entries; select a new inbox directory")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	seen := make(map[string]bool)
	var baseline *claude.Envelope
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := entry.Name()
		seen[name] = true
		receiver := s.receivers[name]
		if receiver == nil {
			receiver, err = claude.NewReceiver(filepath.Join(dir, name), r.scope.Binding)
			if err != nil {
				return err
			}
			s.receivers[name] = receiver
		}
		e, fresh, err := receiver.Observe()
		if err != nil {
			return err
		}
		if e.SessionID == "" {
			continue
		}
		if !fresh {
			if !s.hasObservation && (baseline == nil || ((baseline.FiveHour == nil && baseline.Weekly == nil) && (e.FiveHour != nil || e.Weekly != nil))) {
				copy := e
				baseline = &copy
			}
			continue
		}
		var update usage.Update
		update, err = usage.ClaudeUpdate(r.scope, e, now, now)
		if err == nil {
			err = r.model.Update(update)
		}
		if err != nil {
			return err
		}
		s.hasObservation = true
	}
	if !s.hasObservation && baseline != nil {
		update, err := usage.ClaudeHistoricalUpdate(r.scope, *baseline, now)
		if err == nil {
			err = r.model.Update(update)
		}
		if err != nil {
			return err
		}
		s.hasObservation = true
	}
	for name := range s.receivers {
		if !seen[name] {
			delete(s.receivers, name)
		}
	}
	return nil
}

func waitSource(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
