// SPDX-License-Identifier: GPL-3.0-or-later

package usage

import (
	"testing"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/codex"
)

func TestCodexUpdateKeepsMissingWindowAbsent(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	scope := Scope{Provider: "codex", Binding: "b1", Account: "work", Bucket: "codex"}
	update, err := CodexUpdate(scope, codex.Snapshot{
		Weekly:     &codex.Window{Kind: codex.Weekly, WindowDurationMins: 10080, UsedPercent: 34, ResetsAt: now.Add(7 * 24 * time.Hour)},
		ReceivedAt: now,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if update.StaleAfter != CodexStaleAfter || len(update.Windows) != 1 || update.Windows[FiveHour].ReceivedAt != (time.Time{}) {
		t.Fatalf("unexpected update: %#v", update)
	}
	model := New(0)
	if err := model.Update(update); err != nil {
		t.Fatal(err)
	}
	view := model.ViewAt(now).Scopes[scope]
	if view.Windows[FiveHour].Status != Unavailable || view.Windows[Weekly].Current == nil || view.Windows[Weekly].Current.Duration != 7*24*time.Hour {
		t.Fatalf("unexpected view: %#v", view)
	}
}

func TestClaudeUpdateChecksBindingAndPreservesFraction(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	scope := Scope{Provider: "claude", Binding: "b1", Account: "personal", Bucket: "claude"}
	envelope := claude.Envelope{
		BindingID: "b1",
		FiveHour:  &claude.Window{UsedPercentage: 2.5, ResetsAt: now.Add(5 * time.Hour).Unix()},
	}
	update, err := ClaudeUpdate(scope, envelope, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if update.StaleAfter != ClaudeStaleAfter || update.Windows[FiveHour].UsedPercent != 2.5 || update.Windows[FiveHour].SourceObservedAt != nil {
		t.Fatalf("unexpected update: %#v", update)
	}
	envelope.BindingID = "old"
	if _, err := ClaudeUpdate(scope, envelope, now, now); err == nil {
		t.Fatal("accepted stale Claude binding")
	}
}

func TestClaudeHistoricalUpdateHasNoReceivedTime(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	scope := Scope{Provider: "claude", Binding: "b1", Account: "personal", Bucket: "claude"}
	update, err := ClaudeHistoricalUpdate(scope, claude.Envelope{
		BindingID: "b1",
		Weekly:    &claude.Window{UsedPercentage: 50, ResetsAt: now.Add(time.Hour).Unix()},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	window := update.Windows[Weekly]
	if !window.Historical || !window.ReceivedAt.IsZero() {
		t.Fatalf("unexpected historical update: %#v", update)
	}
}
