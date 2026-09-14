// SPDX-License-Identifier: GPL-3.0-or-later

package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

func TestDashboardMarksOldHardwareWithoutChangingStoredSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sources{ctx: ctx, cancel: cancel, hardwareDone: make(chan struct{})}
	close(s.hardwareDone)
	s.replace("codex", "", nil)
	s.replace("claude", "", nil)
	defer s.Close()
	value := 42.0
	recent := time.Now()
	s.hardware = metric.HardwareSnapshot{ObservedAt: time.Now().Add(-time.Minute), Readings: []metric.Reading{
		{ID: "cpu.usage", State: "ok", Value: &value},
		{ID: "cpu.temperature", State: "ok", Value: &value},
		{ID: "gpu.usage", State: "ok", Value: &value, ReceivedAt: &recent},
	}}
	dashboard := s.Dashboard()
	if dashboard.Hardware.CPU.Usage != "오래됨" || dashboard.Hardware.CPU.Temperature != "오래됨" {
		t.Fatalf("old values displayed as current: %#v", dashboard.Hardware.CPU)
	}
	if dashboard.Hardware.GPU.Usage != "42%" {
		t.Fatal("new helper receipt marked stale")
	}
	if s.hardware.Readings[0].State != "ok" {
		t.Fatal("render mutated collector snapshot")
	}
}

func TestSourceReplacementJoinsRetiredWriterAndHidesItsValues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sources{ctx: ctx, cancel: cancel, hardwareDone: make(chan struct{})}
	close(s.hardwareDone)
	s.replace("codex", "", nil)
	s.replace("claude", "", nil)
	defer s.Close()
	started, retired := make(chan struct{}), make(chan struct{})
	s.replace("codex", "old", func(ctx context.Context, r *sourceRun) {
		close(started)
		<-ctx.Done()
		_ = r.model.Update(usage.Update{Scope: r.scope, Windows: map[usage.WindowKind]usage.Observation{
			usage.FiveHour: {UsedPercent: 90, Duration: 5 * time.Hour, ResetsAt: time.Now().Add(time.Hour), ReceivedAt: time.Now()},
		}})
		close(retired)
	})
	<-started
	var reads sync.WaitGroup
	reads.Add(1)
	go func() {
		defer reads.Done()
		for i := 0; i < 100; i++ {
			_ = s.Dashboard()
		}
	}()
	s.SetCodex(false)
	reads.Wait()
	select {
	case <-retired:
	default:
		t.Fatal("replacement returned before old writer stopped")
	}
	if got := s.Dashboard().Codex.FiveHour; got.Status != usage.Disconnected || got.Value != "—" {
		t.Fatalf("old value visible: %#v", got)
	}
}

func TestInboxBaselineNewSequenceAndOtherSession(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	store, err := claude.NewStore(dir, "binding")
	if err != nil {
		t.Fatal(err)
	}
	status := claude.Statusline{SessionID: "session", FiveHour: &claude.Window{UsedPercentage: 25, ResetsAt: now.Add(time.Hour).Unix()}}
	if _, err = store.Write(status); err != nil {
		t.Fatal(err)
	}
	r := &sourceRun{model: usage.New(0), scope: usage.Scope{Provider: "claude", Binding: "binding", Account: "dedicated", Bucket: "claude"}}
	scan := inboxScan{receivers: make(map[string]*claude.Receiver)}
	view := func() usage.WindowView { return r.model.ViewAt(now).Scopes[r.scope].Windows[usage.FiveHour] }
	observe := func() {
		t.Helper()
		if err := scan.observe(dir, r, now); err != nil {
			t.Fatal(err)
		}
	}
	observe()
	if got := view(); got.Status != usage.Stale || got.ReceivedAt != nil {
		t.Fatalf("baseline became fresh: %#v", got)
	}
	if _, err = store.Write(status); err != nil {
		t.Fatal(err)
	}
	observe()
	first := view()
	if first.Status != usage.OK || first.ReceivedAt == nil {
		t.Fatalf("new sequence not fresh: %#v", first)
	}
	now = now.Add(time.Minute)
	observe()
	if got := view(); !got.ReceivedAt.Equal(*first.ReceivedAt) {
		t.Fatal("duplicate refreshed receipt")
	}
	late := status
	late.SessionID = "late-session"
	late.FiveHour = &claude.Window{UsedPercentage: 99, ResetsAt: now.Add(time.Hour).Unix()}
	if _, err = store.Write(late); err != nil {
		t.Fatal(err)
	}
	observe()
	if got := view(); got.Current.UsedPercent != 25 || !got.ReceivedAt.Equal(*first.ReceivedAt) {
		t.Fatal("late baseline replaced active observation")
	}
	other, err := claude.NewStore(filepath.Join(dir, "unread-subdirectory"), "other-binding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Write(late); err != nil {
		t.Fatal(err)
	}
	observe()
	if got := view(); got.Current.UsedPercent != 25 {
		t.Fatal("unmanaged subdirectory consumed")
	}
	// A new receiver after restart must treat even the latest envelope as old.
	restart := inboxScan{receivers: make(map[string]*claude.Receiver)}
	r.model = usage.New(0)
	if err := restart.observe(dir, r, now); err != nil {
		t.Fatal(err)
	}
	if got := view(); got.Status != usage.Stale || got.ReceivedAt != nil {
		t.Fatal("restart invented a receipt")
	}
}
