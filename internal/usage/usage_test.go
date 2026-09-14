// SPDX-License-Identifier: GPL-3.0-or-later
package usage

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func obs(used float64, reset, received time.Time) Observation {
	return Observation{UsedPercent: used, Duration: time.Hour, ResetsAt: reset, ReceivedAt: received}
}
func both(a, b Observation) map[WindowKind]Observation {
	return map[WindowKind]Observation{FiveHour: a, Weekly: b}
}
func scopeView(t *testing.T, v View) ScopeView {
	t.Helper()
	for _, s := range v.Scopes {
		return s
	}
	t.Fatal("missing scope")
	return ScopeView{}
}

func TestInitialMissingAndClamp(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "codex"}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: obs(150, n.Add(time.Hour), n)}}); err != nil {
		t.Fatal(err)
	}
	v := scopeView(t, m.ViewAt(n))
	if v.Windows[FiveHour].Current.Remaining != 0 || v.Windows[Weekly].Status != Unavailable {
		t.Fatalf("unexpected %#v", v)
	}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: obs(0, n.Add(time.Hour), n.Add(time.Minute))}}); err != nil {
		t.Fatal(err)
	}
	if got := scopeView(t, m.ViewAt(n.Add(time.Minute))).Windows[FiveHour].Current.Remaining; got != 100 {
		t.Fatal(got)
	}
}

func TestResetPendingRecoveryAndRegression(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "claude", Binding: "b", Account: "a", Bucket: "x"}
	old := obs(50, n.Add(time.Hour), n)
	if err := m.Update(Update{Scope: s, Now: n, Windows: both(old, obs(20, n.Add(2*time.Hour), n))}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(Update{Scope: s, Now: n.Add(2 * time.Hour), Windows: map[WindowKind]Observation{Weekly: obs(30, n.Add(3*time.Hour), n.Add(2*time.Hour))}}); err != nil {
		t.Fatal(err)
	}
	v := scopeView(t, m.ViewAt(n.Add(2*time.Hour)))
	if v.Windows[FiveHour].Status != RefreshPending || v.Windows[FiveHour].Current != nil {
		t.Fatalf("pending: %#v", v.Windows[FiveHour])
	}
	if v.Windows[FiveHour].Previous == nil {
		t.Fatal("expired current was not retained as previous")
	}
	if err := m.Update(Update{Scope: s, Now: n.Add(2 * time.Hour), Windows: map[WindowKind]Observation{FiveHour: obs(60, n.Add(90*time.Minute), n.Add(2*time.Hour))}}); err != nil {
		t.Fatal(err)
	}
	if v := scopeView(t, m.ViewAt(n.Add(2*time.Hour))).Windows[FiveHour]; v.Status != RefreshPending {
		t.Fatalf("regression accepted: %#v", v)
	}
	if err := m.Update(Update{Scope: s, Now: n.Add(2 * time.Hour), Windows: map[WindowKind]Observation{FiveHour: obs(10, n.Add(3*time.Hour), n.Add(2*time.Hour))}}); err != nil {
		t.Fatal(err)
	}
	if v := scopeView(t, m.ViewAt(n.Add(2*time.Hour))).Windows[FiveHour]; v.Status != OK || v.Current == nil {
		t.Fatalf("recovery: %#v", v)
	}
}

func TestSameValueTimestampsAndFreshness(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "p"}
	a := obs(20, n.Add(3*time.Hour), n)
	b := obs(30, n.Add(4*time.Hour), n)
	_ = m.Update(Update{Scope: s, Now: n, Windows: both(a, b)})
	first := scopeView(t, m.ViewAt(n)).Windows[FiveHour]
	updated := obs(20, n.Add(3*time.Hour), n.Add(10*time.Minute))
	_ = m.Update(Update{Scope: s, Now: n.Add(10 * time.Minute), Windows: map[WindowKind]Observation{FiveHour: updated}})
	second := scopeView(t, m.ViewAt(n.Add(10*time.Minute))).Windows[FiveHour]
	if second.LastChangedAt == nil || first.LastChangedAt == nil || !second.LastChangedAt.Equal(*first.LastChangedAt) || second.ReceivedAt == nil || !second.ReceivedAt.Equal(n.Add(10*time.Minute)) {
		t.Fatalf("timestamps: %#v %#v", first, second)
	}
	if received := scopeView(t, m.ViewAt(n.Add(10*time.Minute))).Windows[Weekly].ReceivedAt; received == nil || !received.Equal(n) {
		t.Fatal("other window freshness changed")
	}
	if scopeView(t, m.ViewAt(n.Add(2*time.Hour))).Windows[FiveHour].Status != Stale {
		t.Fatal("expected stale")
	}
}

func TestRepeatedMissingAndSameValueRecovery(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := obs(20, n.Add(3*time.Hour), n)
	m := New(time.Hour)
	s := Scope{Provider: "claude"}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: a}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(Update{Scope: s, Now: n.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(Update{Scope: s, Now: n.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	missing := scopeView(t, m.ViewAt(n.Add(2*time.Minute))).Windows[FiveHour]
	if missing.Status != Stale || missing.Current != nil || missing.Previous == nil {
		t.Fatalf("repeated missing: %#v", missing)
	}
	recovered := obs(20, n.Add(3*time.Hour), n.Add(3*time.Minute))
	if err := m.Update(Update{Scope: s, Now: n.Add(3 * time.Minute), Windows: map[WindowKind]Observation{FiveHour: recovered}}); err != nil {
		t.Fatal(err)
	}
	got := scopeView(t, m.ViewAt(n.Add(3*time.Minute))).Windows[FiveHour]
	if got.Status != OK || got.Current == nil || got.Previous != nil || got.LastChangedAt == nil || !got.LastChangedAt.Equal(n) {
		t.Fatalf("same value recovery: %#v", got)
	}
}

// Auth and account errors show their cause but keep the connection's reset
// history, so an earlier reset time is still rejected after recovery.
func TestAuthErrorPreservesResetHistory(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "codex"}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: obs(40, n.Add(2*time.Hour), n)}}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{AuthRequired, AccountCheckRequired, Error} {
		if err := m.Update(Update{Scope: s, Status: status, Now: n.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		v := scopeView(t, m.ViewAt(n.Add(time.Minute)))
		if v.Status != status || v.Windows[FiveHour].Current != nil || v.Windows[FiveHour].Previous == nil {
			t.Fatalf("%s: %#v", status, v.Windows[FiveHour])
		}
	}
	earlier := obs(5, n.Add(90*time.Minute), n.Add(2*time.Minute))
	if err := m.Update(Update{Scope: s, Now: n.Add(2 * time.Minute), Windows: map[WindowKind]Observation{FiveHour: earlier}}); err != nil {
		t.Fatal(err)
	}
	if got := scopeView(t, m.ViewAt(n.Add(2*time.Minute))).Windows[FiveHour]; got.Current != nil {
		t.Fatalf("earlier reset accepted after auth error: %#v", got)
	}
	same := obs(40, n.Add(2*time.Hour), n.Add(3*time.Minute))
	if err := m.Update(Update{Scope: s, Now: n.Add(3 * time.Minute), Windows: map[WindowKind]Observation{FiveHour: same}}); err != nil {
		t.Fatal(err)
	}
	got := scopeView(t, m.ViewAt(n.Add(3*time.Minute))).Windows[FiveHour]
	if got.Status != OK || got.Current == nil || got.Previous != nil || got.LastChangedAt == nil || !got.LastChangedAt.Equal(n) {
		t.Fatalf("recovered value: %#v", got)
	}
}

func TestHistoricalBaselineIsNeverFresh(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "claude"}
	historical := obs(25, n.Add(time.Hour), time.Time{})
	historical.Historical = true
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: historical}}); err != nil {
		t.Fatal(err)
	}
	view := scopeView(t, m.ViewAt(n)).Windows[FiveHour]
	if view.Status != Stale || view.Current != nil || view.Previous == nil || view.ReceivedAt != nil || view.LastChangedAt != nil {
		t.Fatalf("historical baseline: %#v", view)
	}
	expired := historical
	expired.ResetsAt = n
	if err := m.Update(Update{Scope: Scope{Provider: "expired"}, Now: n, Windows: map[WindowKind]Observation{FiveHour: expired}}); err != nil {
		t.Fatal(err)
	}
	if got := m.ViewAt(n).Scopes[Scope{Provider: "expired"}].Windows[FiveHour]; got.Status != RefreshPending || got.Previous == nil {
		t.Fatalf("expired historical baseline: %#v", got)
	}
}

// A live observation whose reset already passed is still the baseline the
// next reset must move past, and is reported as refresh pending.
func TestExpiredFirstObservationBecomesBaseline(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "codex"}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: obs(80, n.Add(-time.Minute), n)}}); err != nil {
		t.Fatal(err)
	}
	got := scopeView(t, m.ViewAt(n)).Windows[FiveHour]
	if got.Status != RefreshPending || got.Current != nil || got.Previous == nil || got.ReceivedAt == nil {
		t.Fatalf("expired first observation: %#v", got)
	}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: obs(1, n.Add(-2*time.Minute), n)}}); err != nil {
		t.Fatal(err)
	}
	if got := scopeView(t, m.ViewAt(n)).Windows[FiveHour]; got.Previous.UsedPercent != 80 {
		t.Fatalf("earlier expired reset replaced baseline: %#v", got)
	}
}

func TestInvalidUpdateIsAtomic(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "codex"}
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: obs(10, n.Add(time.Hour), n)}}); err != nil {
		t.Fatal(err)
	}
	before := m.ViewAt(n)
	invalid := []Update{
		{Scope: s, Status: Status("bad")},
		{Scope: s, Status: Unavailable},
		{Scope: s, Windows: map[WindowKind]Observation{WindowKind("seven_day"): obs(1, n.Add(time.Hour), n)}},
		{Scope: s, StaleAfter: -time.Second},
	}
	for _, update := range invalid {
		if err := m.Update(update); err == nil {
			t.Fatalf("accepted invalid update: %#v", update)
		}
		if after := m.ViewAt(n); !reflect.DeepEqual(after, before) {
			t.Fatalf("invalid update changed state: before=%#v after=%#v", before, after)
		}
	}
	empty := New(time.Hour)
	if err := empty.Update(Update{Scope: Scope{Provider: "new"}, Status: Status("bad")}); err == nil {
		t.Fatal("accepted invalid new scope")
	}
	if len(empty.ViewAt(n).Scopes) != 0 {
		t.Fatal("invalid update created a scope")
	}
}

func TestScopeFreshnessPolicies(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	codexScope := Scope{Provider: "codex"}
	claudeScope := Scope{Provider: "claude"}
	window := obs(10, n.Add(time.Hour), n)
	if err := m.Update(Update{Scope: codexScope, Now: n, StaleAfter: 3 * time.Minute, Windows: map[WindowKind]Observation{FiveHour: window}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(Update{Scope: claudeScope, Now: n, StaleAfter: 5 * time.Minute, Windows: map[WindowKind]Observation{FiveHour: window}}); err != nil {
		t.Fatal(err)
	}
	view := m.ViewAt(n.Add(4 * time.Minute))
	if view.Scopes[codexScope].Windows[FiveHour].Status != Stale || view.Scopes[claudeScope].Windows[FiveHour].Status != OK {
		t.Fatalf("provider freshness: %#v", view)
	}
}

func TestDisconnectClearsHistoryAndInvalidValuesAreRejected(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "p"}
	a := obs(20, n.Add(time.Hour), n)
	_ = m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: a}})
	_ = m.Update(Update{Scope: s, Status: Disconnected, Now: n})
	if v := scopeView(t, m.ViewAt(n)); len(v.Windows) != 2 || v.Windows[FiveHour].Previous != nil || v.Status != Disconnected {
		t.Fatalf("disconnect retained history: %#v", v)
	}
	if err := m.Update(Update{Scope: s, Now: n, Windows: nil}); err != nil {
		t.Fatal(err)
	}
	if v := scopeView(t, m.ViewAt(n)); v.Windows[FiveHour].Status != Unavailable {
		t.Fatalf("error recovery without new data: %#v", v)
	}
	bad := a
	bad.UsedPercent = math.NaN()
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: bad}}); err == nil {
		t.Fatal("NaN accepted")
	}
}

func TestPreResetMissingRejectsEarlierFutureReset(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(time.Hour)
	s := Scope{Provider: "p"}
	old := obs(10, n.Add(2*time.Hour), n)
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{FiveHour: old}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(Update{Scope: s, Now: n.Add(time.Minute), Windows: nil}); err != nil {
		t.Fatal(err)
	}
	earlier := obs(20, n.Add(90*time.Minute), n.Add(time.Minute))
	if err := m.Update(Update{Scope: s, Now: n.Add(time.Minute), Windows: map[WindowKind]Observation{FiveHour: earlier}}); err != nil {
		t.Fatal(err)
	}
	if v := scopeView(t, m.ViewAt(n.Add(time.Minute))).Windows[FiveHour]; v.Current != nil || v.Previous == nil || v.Status != Stale {
		t.Fatalf("earlier reset accepted: %#v", v)
	}
}

func TestCollectingThenPartialWindows(t *testing.T) {
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := New(-time.Hour)
	s := Scope{Provider: "p"}
	if err := m.Update(Update{Scope: s, Status: Collecting, Now: n}); err != nil {
		t.Fatal(err)
	}
	if v := scopeView(t, m.ViewAt(n)); len(v.Windows) != 2 || v.Windows[FiveHour].Status != Collecting || v.Windows[Weekly].Status != Collecting {
		t.Fatalf("collecting windows: %#v", v)
	}
	weekly := obs(25, n.Add(time.Hour), n)
	if err := m.Update(Update{Scope: s, Now: n, Windows: map[WindowKind]Observation{Weekly: weekly}}); err != nil {
		t.Fatal(err)
	}
	v := scopeView(t, m.ViewAt(n))
	if v.Windows[FiveHour].Status != Unavailable || v.Windows[FiveHour].Current != nil || v.Windows[Weekly].Status != OK || v.Windows[Weekly].Current == nil {
		t.Fatalf("never-provided window: %#v", v)
	}
}
