// SPDX-License-Identifier: GPL-3.0-or-later

// Package usage keeps per-provider rate-limit windows and applies the reset
// and freshness rules from AGENTS.md without inventing observations.
package usage

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Status is a window or scope state as shown to the user.
type Status string

const (
	Collecting           Status = "collecting"
	OK                   Status = "ok"
	Stale                Status = "stale"
	RefreshPending       Status = "refresh_pending"
	Unavailable          Status = "unavailable"
	Disconnected         Status = "disconnected"
	AccountCheckRequired Status = "account_check_required"
	AuthRequired         Status = "auth_required"
	Error                Status = "error"
)

// WindowKind names one of the two displayed rate-limit windows.
type WindowKind string

const (
	FiveHour WindowKind = "five_hour"
	Weekly   WindowKind = "weekly"
)

// Scope identifies one provider connection; a new binding is a new scope.
type Scope struct{ Provider, Binding, Account, Bucket string }

// Observation is one provider value for one window. Historical marks a
// baseline read from disk after a restart, never a fresh receipt.
type Observation struct {
	UsedPercent float64
	Duration    time.Duration
	ResetsAt    time.Time
	ReceivedAt  time.Time
	Historical  bool
}

// Update carries one provider result or status change for a scope. Windows
// missing from a successful update are recorded as missing, not as zero.
type Update struct {
	Scope      Scope
	Windows    map[WindowKind]Observation
	Status     Status
	Now        time.Time
	StaleAfter time.Duration
}

// View is a read-only snapshot of every scope.
type View struct{ Scopes map[Scope]ScopeView }

// ScopeView aggregates one scope's windows.
type ScopeView struct {
	Status  Status
	Windows map[WindowKind]WindowView
}

// WindowView is one window's display state. Previous is the last accepted
// value when Current is missing or expired.
type WindowView struct {
	Status        Status
	Current       *Value
	Previous      *Value
	ReceivedAt    *time.Time
	LastChangedAt *time.Time
}

// Value is a displayable observation; Remaining is clamp(100-used, 0, 100).
type Value struct {
	UsedPercent float64
	Remaining   float64
	Duration    time.Duration
	ResetsAt    time.Time
}

type windowState struct {
	current, previous         *Observation
	receivedAt, lastChangedAt time.Time
	status                    Status
}
type scopeState struct {
	status     Status
	staleAfter time.Duration
	windows    map[WindowKind]*windowState
}

// Model is a lock-protected state machine over scopes; it is safe for
// concurrent use.
type Model struct {
	mu         sync.RWMutex
	staleAfter time.Duration
	scopes     map[Scope]*scopeState
}

// New creates an empty model. staleAfter is the default freshness limit;
// providers override it per update.
func New(staleAfter time.Duration) *Model {
	if staleAfter < 0 {
		staleAfter = 0
	}
	return &Model{staleAfter: staleAfter, scopes: make(map[Scope]*scopeState)}
}

// Update applies one provider result atomically: an invalid update changes
// nothing.
func (m *Model) Update(u Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.Status == "" {
		u.Status = OK
	}
	if !validInputStatus(u.Status) {
		return fmt.Errorf("invalid status %q", u.Status)
	}
	if u.StaleAfter < 0 {
		return fmt.Errorf("invalid stale duration")
	}
	for k, o := range u.Windows {
		if !validWindowKind(k) {
			return fmt.Errorf("invalid window kind %q", k)
		}
		if err := validate(o); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	now := u.Now
	if now.IsZero() {
		now = time.Now()
	}
	s := m.scopes[u.Scope]
	if s == nil {
		s = &scopeState{status: Collecting, staleAfter: m.staleAfter, windows: make(map[WindowKind]*windowState)}
		m.scopes[u.Scope] = s
	}
	if u.StaleAfter > 0 {
		s.staleAfter = u.StaleAfter
	}
	ensureWindows(s)
	if u.Status != OK {
		if u.Status == Disconnected {
			// Only a disconnect ends the connection's history.
			s.windows = unavailableWindows(Disconnected)
		} else {
			// Auth, account and collection errors show their cause but keep
			// the reset history of this connection.
			for _, w := range s.windows {
				if w.current != nil {
					w.previous = w.current
				}
				w.current = nil
				w.status = u.Status
			}
		}
		s.status = u.Status
		return nil
	}
	for _, k := range []WindowKind{FiveHour, Weekly} {
		w := s.windows[k]
		if o, present := u.Windows[k]; present {
			m.accept(w, o, now)
		} else {
			m.missing(w, now)
		}
	}
	s.status = OK
	for _, w := range s.windows {
		if w.status != OK {
			s.status = w.status
			break
		}
	}
	return nil
}

func validate(o Observation) error {
	if math.IsNaN(o.UsedPercent) || math.IsInf(o.UsedPercent, 0) || o.UsedPercent < 0 {
		return fmt.Errorf("invalid used_percent")
	}
	if o.Duration <= 0 || o.ResetsAt.IsZero() || (!o.Historical && o.ReceivedAt.IsZero()) {
		return fmt.Errorf("invalid window metadata")
	}
	return nil
}

func validInputStatus(s Status) bool {
	switch s {
	case OK, Collecting, Disconnected, AccountCheckRequired, AuthRequired, Error:
		return true
	default:
		return false
	}
}

func validWindowKind(k WindowKind) bool { return k == FiveHour || k == Weekly }

func ensureWindows(s *scopeState) {
	if s.windows == nil {
		s.windows = make(map[WindowKind]*windowState)
	}
	for _, k := range []WindowKind{FiveHour, Weekly} {
		if s.windows[k] == nil {
			s.windows[k] = &windowState{status: s.status}
		}
	}
}

// accept applies one observation. A reset time earlier than the known
// baseline is a regression and is ignored; only a later valid reset advances.
func (m *Model) accept(w *windowState, o Observation, now time.Time) {
	baseline := w.current
	if baseline == nil {
		baseline = w.previous
	}
	if baseline != nil && (o.ResetsAt.Before(baseline.ResetsAt) ||
		(o.ResetsAt.Equal(baseline.ResetsAt) && !now.Before(baseline.ResetsAt))) {
		return
	}
	if o.Historical && baseline != nil {
		return
	}
	if !now.Before(o.ResetsAt) {
		// An already-expired window is still worth remembering as the
		// baseline the next reset must move past.
		if baseline == nil {
			w.current = nil
			w.previous = cloneObs(&o)
			w.status = RefreshPending
			if !o.Historical {
				w.receivedAt = o.ReceivedAt
			}
		}
		return
	}
	if o.Historical {
		w.current = nil
		w.previous = cloneObs(&o)
		w.receivedAt = time.Time{}
		w.lastChangedAt = time.Time{}
		w.status = Stale
		return
	}
	if baseline == nil || !same(*baseline, o) {
		w.lastChangedAt = o.ReceivedAt
	}
	w.current = cloneObs(&o)
	w.previous = nil
	w.receivedAt = o.ReceivedAt
	w.status = OK
}

// missing records that a successful response omitted this window.
func (m *Model) missing(w *windowState, now time.Time) {
	if w.current == nil {
		switch {
		case w.previous != nil && !now.Before(w.previous.ResetsAt):
			w.status = RefreshPending
		case w.previous != nil:
			w.status = Stale
		default:
			w.status = Unavailable
		}
		return
	}
	w.previous = w.current
	w.current = nil
	w.status = Stale
	if !now.Before(w.previous.ResetsAt) {
		w.status = RefreshPending
	}
}

func unavailableWindows(status Status) map[WindowKind]*windowState {
	return map[WindowKind]*windowState{
		FiveHour: {status: status},
		Weekly:   {status: status},
	}
}

func same(a, b Observation) bool {
	return a.UsedPercent == b.UsedPercent && a.Duration == b.Duration && a.ResetsAt.Equal(b.ResetsAt)
}
func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	x := *t
	return &x
}
func cloneObs(o *Observation) *Observation {
	if o == nil {
		return nil
	}
	x := *o
	return &x
}

// View evaluates every scope at the current time.
func (m *Model) View() View { return m.ViewAt(time.Now()) }

// ViewAt evaluates expiry and staleness against now.
func (m *Model) ViewAt(now time.Time) View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := View{Scopes: make(map[Scope]ScopeView, len(m.scopes))}
	for scope, s := range m.scopes {
		sv := ScopeView{Windows: make(map[WindowKind]WindowView, len(s.windows))}
		for k, w := range s.windows {
			wv := WindowView{Status: w.status, ReceivedAt: nonzeroTime(w.receivedAt), LastChangedAt: nonzeroTime(w.lastChangedAt), Current: toValue(w.current), Previous: toValue(w.previous)}
			if w.current != nil && !now.Before(w.current.ResetsAt) {
				wv.Previous = toValue(w.current)
				wv.Current = nil
				wv.Status = RefreshPending
			}
			if wv.Current != nil && s.staleAfter > 0 && now.Sub(w.receivedAt) > s.staleAfter {
				wv.Status = Stale
			}
			sv.Windows[k] = wv
		}
		sv.Status = aggregateStatus(sv.Windows)
		v.Scopes[scope] = sv
	}
	return v
}

func nonzeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return cloneTime(&t)
}

func aggregateStatus(windows map[WindowKind]WindowView) Status {
	priority := []Status{Disconnected, AuthRequired, AccountCheckRequired, Error, RefreshPending, Stale, Unavailable, Collecting, OK}
	for _, p := range priority {
		for _, w := range windows {
			if w.Status == p {
				return p
			}
		}
	}
	return Unavailable
}

func toValue(o *Observation) *Value {
	if o == nil {
		return nil
	}
	return &Value{UsedPercent: o.UsedPercent, Remaining: min(max(100-o.UsedPercent, 0), 100), Duration: o.Duration, ResetsAt: o.ResetsAt}
}
