// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/usage"
)

func TestDashboardFromSnapshotsStatusAndWindowFormatting(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2*time.Hour + 18*time.Minute)
	received := now.Add(-time.Minute)
	value := usage.Value{Remaining: 74, ResetsAt: reset}
	stale := usage.Value{Remaining: 61, ResetsAt: reset}
	view := usage.ScopeView{Windows: map[usage.WindowKind]usage.WindowView{
		usage.FiveHour: {Status: usage.OK, Current: &value, ReceivedAt: &received},
		usage.Weekly:   {Status: usage.Stale, Previous: &stale, ReceivedAt: &received},
	}}
	d := DashboardFromSnapshots(now, view, usage.ScopeView{Windows: map[usage.WindowKind]usage.WindowView{
		usage.FiveHour: {Status: usage.RefreshPending},
		usage.Weekly:   {Status: usage.AuthRequired},
	}}, metric.HardwareSnapshot{})
	if d.Codex.FiveHour.Value != "74%" || d.Codex.FiveHour.Fraction != .74 || d.Codex.FiveHour.Reset != "2시간 18분 뒤" {
		t.Fatalf("normal quota = %+v", d.Codex.FiveHour)
	}
	if d.Codex.FiveHour.Received != "수신 11:59" || d.Claude.FiveHour.Received != "수신 기록 없음" {
		t.Fatalf("received labels = %q %q", d.Codex.FiveHour.Received, d.Claude.FiveHour.Received)
	}
	if d.Codex.Weekly.Value != "61%" || d.Codex.Weekly.Fraction != .61 || d.Codex.Weekly.Reset != "오래됨 · 수신 11:59" {
		t.Fatalf("stale quota = %+v", d.Codex.Weekly)
	}
	if d.Claude.FiveHour.Value != "—" || d.Claude.FiveHour.Reset != "갱신 대기" || d.Claude.Weekly.Reset != "인증 필요" {
		t.Fatalf("status quota = %+v %+v", d.Claude.FiveHour, d.Claude.Weekly)
	}
}

func TestDashboardQuotaClampAndResetBoundary(t *testing.T) {
	now := time.Unix(100, 0)
	views := func(v float64, reset time.Time) usage.ScopeView {
		return usage.ScopeView{Windows: map[usage.WindowKind]usage.WindowView{usage.FiveHour: {Status: usage.OK, Current: &usage.Value{Remaining: v, ResetsAt: reset}}}}
	}
	low := DashboardFromSnapshots(now, views(-20, now.Add(-time.Minute)), usage.ScopeView{}, metric.HardwareSnapshot{}).Codex.FiveHour
	high := DashboardFromSnapshots(now, views(130, now), usage.ScopeView{}, metric.HardwareSnapshot{}).Codex.FiveHour
	if low.Value != "0%" || low.Fraction != 0 || low.Reset != "1분 이내" || high.Value != "100%" || high.Fraction != 1 || high.Reset != "1분 이내" {
		t.Fatalf("clamp/boundary = %+v %+v", low, high)
	}
}

func TestDashboardQuotaAllUnavailableStatuses(t *testing.T) {
	want := map[usage.Status]string{
		usage.Unavailable: "미제공", usage.Collecting: "수집 중", usage.Disconnected: "미연결",
		usage.AccountCheckRequired: "계정 확인 필요", usage.AuthRequired: "인증 필요", usage.Error: "수집 오류",
	}
	for status, label := range want {
		d := DashboardFromSnapshots(time.Now(), usage.ScopeView{Windows: map[usage.WindowKind]usage.WindowView{usage.FiveHour: {Status: status}}}, usage.ScopeView{}, metric.HardwareSnapshot{})
		if d.Codex.FiveHour.Value != "—" || d.Codex.FiveHour.Reset != label || d.Codex.FiveHour.Fraction != 0 {
			t.Errorf("status %q = %+v", status, d.Codex.FiveHour)
		}
	}
}

func TestDashboardHardwareNullAndFallback(t *testing.T) {
	usageValue, tempValue := 55.4, 62.2
	d := DashboardFromSnapshots(time.Now(), usage.ScopeView{}, usage.ScopeView{}, metric.HardwareSnapshot{Readings: []metric.Reading{
		{ID: "ram.usage", State: "ok", Value: &usageValue},
		{ID: "ram.temperature", State: "ok", Value: &tempValue, Label: "메인보드 온도"},
		{ID: "gpu.usage", State: "error", Value: &usageValue},
	}})
	if d.Hardware.RAM.Label != "RAM · 메인보드" || d.Hardware.RAM.Usage != "55%" || d.Hardware.RAM.Temperature != "62°C" || d.Hardware.RAM.Fraction < .5539 || d.Hardware.RAM.Fraction > .5541 {
		t.Fatalf("ram fallback = %+v", d.Hardware.RAM)
	}
	if d.Hardware.CPU.Usage != "—" || d.Hardware.CPU.Temperature != "—" || d.Hardware.GPU.Usage != "—" {
		t.Fatalf("null hardware = %+v %+v", d.Hardware.CPU, d.Hardware.GPU)
	}
}

func TestLiveOverlaysReadCallbackAndPreservePNG(t *testing.T) {
	base := Dashboard{At: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	base.Codex.FiveHour = Quota{Value: "10%", Reset: "1시간 뒤", Fraction: .1}
	base.Hardware.CPU = Metric{Label: "CPU", Usage: "10%", Temperature: "40°C", Fraction: .1}
	current := base
	azure := AzureOverlay(func() Dashboard { return current })
	a, err := azure(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	current.Codex.FiveHour.Value = "90%"
	b, err := azure(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("azure callback update did not change PNG")
	}
	img, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != landscapeWidth || img.Bounds().Dy() != landscapeHeight {
		t.Fatalf("size = %v", img.Bounds())
	}
	_, _, _, alpha := img.At(0, 0).RGBA()
	if alpha != 0 {
		t.Fatalf("background alpha = %d", alpha)
	}
	if _, err := (AzureOverlay(nil))(0, 0); err == nil {
		t.Fatal("nil azure callback did not fail")
	}
	if _, err := (HalloweenOverlay(nil))(0, 0); err == nil {
		t.Fatal("nil halloween callback did not fail")
	}
}

// A restart baseline shows a value without a reception in this run. Labelling
// that "수신 기록 없음" beside a number reads as a contradiction.
func TestRestartBaselineIsLabelledAsPastRecord(t *testing.T) {
	view := usage.WindowView{Status: usage.Stale, Previous: &usage.Value{Remaining: 42, ResetsAt: time.Now().Add(time.Hour)}}
	got := quota(time.Now(), view)
	if got.Value != "42%" {
		t.Fatalf("value = %q", got.Value)
	}
	if got.Received != "재시작 전 기록" {
		t.Fatalf("received = %q", got.Received)
	}
	none := quota(time.Now(), usage.WindowView{Status: usage.Collecting})
	if none.Received != "수신 기록 없음" {
		t.Fatalf("collecting received = %q", none.Received)
	}
}
