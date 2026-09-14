// SPDX-License-Identifier: GPL-3.0-or-later

package usage

import (
	"fmt"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
	"github.com/dbwk10317/turzx-control/internal/codex"
)

const (
	CodexStaleAfter  = 3 * time.Minute
	ClaudeStaleAfter = 5 * time.Minute
)

// CodexUpdate converts one successful App Server response without inventing
// observations for windows the provider omitted.
func CodexUpdate(scope Scope, snapshot codex.Snapshot, now time.Time) (Update, error) {
	if err := validateScope(scope, "codex"); err != nil {
		return Update{}, err
	}
	windows := make(map[WindowKind]Observation, 2)
	if snapshot.FiveHour != nil {
		windows[FiveHour] = codexObservation(*snapshot.FiveHour, snapshot.ReceivedAt)
	}
	if snapshot.Weekly != nil {
		windows[Weekly] = codexObservation(*snapshot.Weekly, snapshot.ReceivedAt)
	}
	return Update{Scope: scope, Windows: windows, Now: now, StaleAfter: CodexStaleAfter}, nil
}

func codexObservation(window codex.Window, receivedAt time.Time) Observation {
	return Observation{
		UsedPercent: float64(window.UsedPercent),
		Duration:    time.Duration(window.WindowDurationMins) * time.Minute,
		ResetsAt:    window.ResetsAt,
		ReceivedAt:  receivedAt,
	}
}

// ClaudeUpdate converts one newly received statusline envelope. Claude does
// not report when it observed the value, so receivedAt is the only timestamp.
func ClaudeUpdate(scope Scope, envelope claude.Envelope, receivedAt, now time.Time) (Update, error) {
	if err := validateScope(scope, "claude"); err != nil {
		return Update{}, err
	}
	if scope.Binding != envelope.BindingID {
		return Update{}, fmt.Errorf("Claude binding mismatch")
	}
	windows := make(map[WindowKind]Observation, 2)
	if envelope.FiveHour != nil {
		windows[FiveHour] = claudeObservation(*envelope.FiveHour, 5*time.Hour, receivedAt)
	}
	if envelope.Weekly != nil {
		windows[Weekly] = claudeObservation(*envelope.Weekly, 7*24*time.Hour, receivedAt)
	}
	return Update{Scope: scope, Windows: windows, Now: now, StaleAfter: ClaudeStaleAfter}, nil
}

// ClaudeHistoricalUpdate imports a restart baseline without claiming that the
// file discovery time was a new provider observation.
func ClaudeHistoricalUpdate(scope Scope, envelope claude.Envelope, now time.Time) (Update, error) {
	update, err := ClaudeUpdate(scope, envelope, time.Time{}, now)
	if err != nil {
		return Update{}, err
	}
	for kind, observation := range update.Windows {
		observation.Historical = true
		update.Windows[kind] = observation
	}
	return update, nil
}

func claudeObservation(window claude.Window, duration time.Duration, receivedAt time.Time) Observation {
	return Observation{
		UsedPercent: window.UsedPercentage,
		Duration:    duration,
		ResetsAt:    time.Unix(window.ResetsAt, 0).UTC(),
		ReceivedAt:  receivedAt,
	}
}

func validateScope(scope Scope, provider string) error {
	if scope.Provider != provider || scope.Binding == "" || scope.Account == "" || scope.Bucket == "" {
		return fmt.Errorf("invalid %s usage scope", provider)
	}
	return nil
}
