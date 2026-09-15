// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

// Dashboard is the immutable, display-ready snapshot consumed by an overlay.
type Dashboard struct {
	At       time.Time
	Codex    ProviderDashboard
	Claude   ProviderDashboard
	Hardware HardwareDashboard
}

type ProviderDashboard struct {
	// Account names whose usage this is, so two providers on screen are never
	// confused for one another. Empty when the account is not known yet.
	Account  string
	FiveHour Quota
	Weekly   Quota
}

type Quota struct {
	Value      string
	Reset      string
	Received   string
	Status     usage.Status
	Fraction   float64
	ReceivedAt *time.Time
}

type HardwareDashboard struct {
	CPU, GPU, RAM Metric
}

type Metric struct {
	Label, Usage, Temperature string
	Fraction                  float64
}

// DashboardFromSnapshots converts collector-owned snapshots without invoking collectors.
func DashboardFromSnapshots(at time.Time, codex, claude usage.ScopeView, hardware metric.HardwareSnapshot) Dashboard {
	return Dashboard{At: at, Codex: providerDashboard(at, codex), Claude: providerDashboard(at, claude), Hardware: hardwareDashboard(hardware)}
}

func providerDashboard(at time.Time, scope usage.ScopeView) ProviderDashboard {
	return ProviderDashboard{FiveHour: quota(at, scope.Windows[usage.FiveHour]), Weekly: quota(at, scope.Windows[usage.Weekly])}
}

func quota(at time.Time, w usage.WindowView) Quota {
	q := Quota{Status: w.Status, ReceivedAt: w.ReceivedAt, Received: receivedText(w.ReceivedAt)}
	value := w.Current
	if w.Status == usage.Stale && value == nil {
		value = w.Previous
	}
	if (w.Status == usage.OK || w.Status == usage.Stale) && value != nil {
		q.Value = fmt.Sprintf("%.0f%%", clamp(value.Remaining, 0, 100))
		q.Fraction = clamp(value.Remaining/100, 0, 1)
		if w.ReceivedAt == nil {
			// A value with no reception in this run is the restart baseline.
			// Saying "수신 기록 없음" beside a number reads as a contradiction.
			q.Received = "재시작 전 기록"
		}
		if !value.ResetsAt.IsZero() {
			q.Reset = resetText(value.ResetsAt.Sub(at))
		}
		if w.Status == usage.Stale {
			q.Reset = staleReset(w.ReceivedAt)
		}
		return q
	}
	q.Value, q.Reset = statusText(w.Status)
	return q
}

func receivedText(received *time.Time) string {
	if received == nil {
		return "수신 기록 없음"
	}
	return "수신 " + received.Format("15:04")
}

func resetText(d time.Duration) string {
	if d < time.Minute {
		return "1분 이내"
	}
	return shortDuration(d) + " 뒤"
}

func staleReset(received *time.Time) string {
	if received == nil {
		return "오래됨"
	}
	return "오래됨 · 수신 " + received.Format("15:04")
}

func statusText(status usage.Status) (string, string) {
	switch status {
	case usage.RefreshPending:
		return "—", "갱신 대기"
	case usage.Unavailable:
		return "—", "미제공"
	case usage.Collecting:
		return "—", "수집 중"
	case usage.Disconnected:
		return "—", "미연결"
	case usage.AccountCheckRequired:
		return "—", "계정 확인 필요"
	case usage.AuthRequired:
		return "—", "인증 필요"
	case usage.Error:
		return "—", "수집 오류"
	default:
		return "—", "미제공"
	}
}

func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	minutes := int(d / time.Minute)
	if minutes < 1 {
		return "1분 이내"
	}
	days, hours := minutes/(24*60), minutes/60%24
	minutes %= 60
	parts := make([]string, 0, 2)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d일", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d시간", hours))
	}
	if len(parts) < 2 && minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d분", minutes))
	}
	return strings.Join(parts, " ")
}

func hardwareDashboard(snapshot metric.HardwareSnapshot) HardwareDashboard {
	readings := make(map[string]metric.Reading, len(snapshot.Readings))
	for _, r := range snapshot.Readings {
		if _, ok := readings[r.ID]; !ok {
			readings[r.ID] = r
		}
	}
	return HardwareDashboard{CPU: hardwareMetric("CPU", readings["cpu.usage"], readings["cpu.temperature"]), GPU: hardwareMetric("GPU", readings["gpu.usage"], readings["gpu.temperature"]), RAM: hardwareMetric(ramLabel(readings["ram.temperature"]), readings["ram.usage"], readings["ram.temperature"])}
}

func ramLabel(r metric.Reading) string {
	if r.Label == "메인보드 온도" {
		return "RAM · 메인보드"
	}
	return "RAM"
}

func hardwareMetric(label string, usageReading, temp metric.Reading) Metric {
	m := Metric{Label: label, Usage: "—", Temperature: "—"}
	if usageReading.State == "stale" {
		m.Usage = "오래됨"
	}
	if temp.State == "stale" {
		m.Temperature = "오래됨"
	}
	if usageReading.State == "ok" && usageReading.Value != nil && finite(*usageReading.Value) {
		v := clamp(*usageReading.Value, 0, 100)
		m.Usage, m.Fraction = fmt.Sprintf("%.0f%%", v), v/100
	}
	if temp.State == "ok" && temp.Value != nil && finite(*temp.Value) {
		m.Temperature = fmt.Sprintf("%.0f°C", *temp.Value)
	}
	return m
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
func finite(v float64) bool           { return !math.IsNaN(v) && !math.IsInf(v, 0) }
