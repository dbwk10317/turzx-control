// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

func TestLiveDataConfigRequiresDedicatedInputs(t *testing.T) {
	base := liveDataConfig{
		CodexBin: "codex", CodexHome: filepath.Join(t.TempDir(), "codex"), CodexBucket: "codex",
		ClaudeInboxFile: filepath.Join(t.TempDir(), "claude.json"), ClaudeBinding: "binding-1",
	}
	if err := base.validate("azure-ribbon"); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]liveDataConfig{
		"theme":       func() liveDataConfig { c := base; return c }(),
		"codex home":  func() liveDataConfig { c := base; c.CodexHome = ""; return c }(),
		"claude file": func() liveDataConfig { c := base; c.ClaudeInboxFile = ""; return c }(),
		"binding":     func() liveDataConfig { c := base; c.ClaudeBinding = ""; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "theme" {
				if err := cfg.validate(""); err == nil {
					t.Fatal("accepted live data without a theme")
				}
				return
			}
			if err := cfg.validate("azure-ribbon"); err == nil {
				t.Fatalf("accepted invalid config %q", name)
			}
		})
	}
}

func TestLiveDataUsesExactScopes(t *testing.T) {
	cfg := liveDataConfig{
		CodexBucket: "codex", ClaudeBinding: "binding",
		CodexScope:  usage.Scope{Provider: "codex", Binding: "selected-codex", Account: "work", Bucket: "codex"},
		ClaudeScope: usage.Scope{Provider: "claude", Binding: "binding", Account: "personal", Bucket: "claude"},
	}
	codexScope, claudeScope := cfg.scopes()
	if codexScope.Binding != "selected-codex" || codexScope.Account != "work" {
		t.Fatalf("codex scope = %#v", codexScope)
	}
	if claudeScope.Account != "personal" {
		t.Fatalf("claude scope = %#v", claudeScope)
	}
}

func TestClaudeHistoricalBaselineThenFreshUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	reset := time.Now().Add(time.Hour).Unix()
	write := func(sequence uint64, used float64) {
		e := claude.Envelope{
			SchemaVersion: claude.SchemaVersion, BindingID: "binding", SessionID: "session", Sequence: sequence,
			FiveHour: &claude.Window{UsedPercentage: used, ResetsAt: reset},
			Weekly:   &claude.Window{UsedPercentage: used + 1, ResetsAt: reset + int64(24*time.Hour/time.Second)},
		}
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(1, 20)
	receiver, err := claude.NewReceiver(path, "binding")
	if err != nil {
		t.Fatal(err)
	}
	model := usage.New(0)
	d := &liveData{
		model:       model,
		claudeScope: usage.Scope{Provider: "claude", Binding: "binding", Account: "personal", Bucket: "claude"},
		claude:      receiver,
	}
	d.observeClaude()
	view := model.ViewAt(time.Now())
	baseline := view.Scopes[d.claudeScope].Windows[usage.FiveHour]
	if baseline.Status != usage.Stale || baseline.Current != nil || baseline.Previous == nil {
		t.Fatalf("historical baseline = %#v", baseline)
	}
	write(2, 25)
	d.observeClaude()
	view = model.ViewAt(time.Now())
	fresh := view.Scopes[d.claudeScope].Windows[usage.FiveHour]
	if fresh.Status != usage.OK || fresh.Current == nil || fresh.ReceivedAt == nil {
		t.Fatalf("fresh update = %#v", fresh)
	}
}

func TestMergeHardwareSnapshot(t *testing.T) {
	base := 10.0
	snapshot := metric.HardwareSnapshot{Readings: []metric.Reading{{ID: "cpu.usage", Value: &base, State: "ok"}}}
	value := 42.0
	got := mergeHardwareSnapshot(snapshot, []metric.Reading{{ID: "cpu.usage", Value: &value, State: "ok"}})
	if got.Readings[0].Value == nil || *got.Readings[0].Value != value {
		t.Fatalf("merged reading = %#v", got.Readings[0])
	}
}

func TestLiveDataCloseCancelsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d := &liveData{cancel: cancel}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		<-ctx.Done()
		close(done)
	}()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker was not cancelled")
	}
}
