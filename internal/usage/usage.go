// SPDX-License-Identifier: GPL-3.0-or-later
package usage

import (
	"fmt"
	"math"
	"sync"
	"time"
)

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

type WindowKind string

const (
	FiveHour WindowKind = "five_hour"
	Weekly   WindowKind = "weekly"
)

type Scope struct{ Provider, Binding, Account, Bucket string }

type Observation struct {
	UsedPercent      float64
	Duration         time.Duration
	ResetsAt         time.Time
	ReceivedAt       time.Time
	SourceObservedAt *time.Time
	Historical       bool
}

type Update struct {
	Scope       Scope
	Windows     map[WindowKind]Observation
	Unsupported map[WindowKind]bool
	Status      Status
	Now         time.Time
	StaleAfter  time.Duration
}

type View struct{ Scopes map[Scope]ScopeView }

type ScopeView struct {
	Status  Status
	Windows map[WindowKind]WindowView
}

type WindowView struct {
	Status           Status
	Current          *Value
	Previous         *Value
	ReceivedAt       *time.Time
	LastChangedAt    *time.Time
	SourceObservedAt *time.Time
}

type Value struct {
	UsedPercent float64
	Remaining   float64
	Duration    time.Duration
	ResetsAt    time.Time
}

type windowState struct {
	current, previous         *Observation
	receivedAt, lastChangedAt time.Time
	sourceObservedAt          *time.Time
	status                    Status
}
type scopeState struct {
	status     Status
	staleAfter time.Duration
	windows    map[WindowKind]*windowState
}

type Model struct {
	mu         sync.RWMutex
	staleAfter time.Duration
	scopes     map[Scope]*scopeState
}

func New(staleAfter time.Duration) *Model {
	if staleAfter < 0 {
		staleAfter = 0
	}
	return &Model{staleAfter: staleAfter, scopes: make(map[Scope]*scopeState)}
}
func NewModel(staleAfter time.Duration) *Model { return New(staleAfter) }

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
		if u.Unsupported[k] {
			return fmt.Errorf("%s is both observed and unsupported", k)
		}
		if err := validate(o); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	for k := range u.Unsupported {
		if !validWindowKind(k) {
			return fmt.Errorf("invalid window kind %q", k)
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
		if u.Status == Collecting {
			for _, w := range s.windows {
				w.status = Collecting
			}
			s.status = Collecting
			return nil
		}
		if u.Status == Unavailable || u.Status == AccountCheckRequired || u.Status == AuthRequired {
			s.windows = unavailableWindows(Unavailable)
			for _, w := range s.windows {
				w.status = u.Status
			}
		}
		for _, w := range s.windows {
			if w.current != nil {
				w.previous = w.current
			}
			w.current = nil
			w.status = u.Status
		}
		if u.Status == Disconnected {
			s.windows = unavailableWindows(Disconnected)
		}
		s.status = u.Status
		return nil
	}
	for _, k := range []WindowKind{FiveHour, Weekly} {
		w := s.windows[k]
		if w == nil {
			w = &windowState{status: Collecting}
			s.windows[k] = w
		}
		o, present := u.Windows[k]
		if u.Unsupported[k] {
			s.windows[k] = &windowState{status: Unavailable}
			continue
		}
		if present {
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
	case OK, Collecting, Unavailable, Disconnected, AccountCheckRequired, AuthRequired, Error:
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
		if o.Historical {
			w.current = nil
			w.previous = cloneObs(&o)
			w.status = RefreshPending
		}
		return
	}
	if o.Historical {
		w.current = nil
		w.previous = cloneObs(&o)
		w.receivedAt = time.Time{}
		w.lastChangedAt = time.Time{}
		w.sourceObservedAt = cloneTime(o.SourceObservedAt)
		w.status = Stale
		return
	}
	changed := baseline == nil || !same(*baseline, o)
	if changed {
		w.lastChangedAt = o.ReceivedAt
		w.sourceObservedAt = cloneTime(o.SourceObservedAt)
	}
	w.current = cloneObs(&o)
	w.previous = nil
	w.receivedAt = o.ReceivedAt
	w.status = OK
}

func (m *Model) missing(w *windowState, now time.Time) {
	if w.current == nil {
		if w.previous != nil && !now.Before(w.previous.ResetsAt) {
			w.status = RefreshPending
		} else if w.previous != nil {
			w.status = Stale
		} else {
			w.status = Unavailable
		}
		return
	}
	if !now.Before(w.current.ResetsAt) {
		w.previous = w.current
		w.current = nil
		w.status = RefreshPending
		return
	}
	w.previous = w.current
	w.current = nil
	w.status = Stale
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
	x.SourceObservedAt = cloneTime(o.SourceObservedAt)
	return &x
}

func (m *Model) View() View { return m.ViewAt(time.Now()) }
func (m *Model) ViewAt(now time.Time) View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := View{Scopes: make(map[Scope]ScopeView, len(m.scopes))}
	for scope, s := range m.scopes {
		sv := ScopeView{Windows: make(map[WindowKind]WindowView, len(s.windows))}
		for k, w := range s.windows {
			wv := WindowView{Status: w.status, ReceivedAt: nonzeroTime(w.receivedAt), LastChangedAt: nonzeroTime(w.lastChangedAt), SourceObservedAt: cloneTime(w.sourceObservedAt), Current: toValue(w.current), Previous: toValue(w.previous)}
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
	r := 100 - o.UsedPercent
	if r < 0 {
		r = 0
	}
	if r > 100 {
		r = 100
	}
	return &Value{UsedPercent: o.UsedPercent, Remaining: r, Duration: o.Duration, ResetsAt: o.ResetsAt}
}
