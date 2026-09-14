// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"time"
)

var (
	halloweenPanel  = color.NRGBA{R: 0x10, G: 0x0B, B: 0x20, A: 0xA8}
	halloweenEdge   = color.NRGBA{R: 0xFF, G: 0xC8, B: 0x57, A: 0x92}
	halloweenInk    = color.NRGBA{R: 0xFF, G: 0xF7, B: 0xE6, A: 0xFF}
	halloweenMuted  = color.NRGBA{R: 0xD8, G: 0xC9, B: 0xE8, A: 0xFF}
	halloweenGold   = color.NRGBA{R: 0xFF, G: 0xC8, B: 0x57, A: 0xFF}
	halloweenViolet = color.NRGBA{R: 0xA7, G: 0x8B, B: 0xFA, A: 0xFF}
	halloweenCyan   = color.NRGBA{R: 0x67, G: 0xE8, B: 0xF9, A: 0xFF}
)

// HalloweenPreviewOverlay renders the compact overlay for smon-halloween.
// Values are preview data; the LIVE counter identifies refreshed overlay frames.
func HalloweenPreviewOverlay(elapsed time.Duration, counter uint64) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	for _, panel := range []image.Rectangle{
		image.Rect(24, 20, 390, 210),
		image.Rect(410, 20, 1270, 210),
		image.Rect(1290, 20, 1896, 210),
	} {
		drawRounded(img, panel, 16, halloweenEdge)
		drawRounded(img, panel.Inset(2), 14, halloweenPanel)
	}

	text(img, true, 48, 49, "10월 31일 토요일", 20, halloweenGold)
	clock := time.Date(2026, 10, 31, 20, 42, 36, 0, time.Local).Add(elapsed).Format("15:04:05")
	text(img, true, 46, 116, clock, 64, halloweenInk)
	text(img, false, 48, 145, "할로윈의 밤", 19, halloweenMuted)
	text(img, false, 48, 145, "할로윈의 밤", 19, halloweenMuted)
	text(img, false, 48, 181, fmt.Sprintf("LIVE  %s  #%06d", formatElapsed(elapsed), counter), 16, halloweenGold)

	text(img, true, 438, 49, "AI 사용량 · 미리보기", 18, halloweenMuted)
	text(img, true, 438, 78, "CODEX", 20, halloweenGold)
	text(img, true, 858, 78, "CLAUDE", 20, halloweenViolet)
	drawHalloweenQuota(img, 438, 108, "5시간", "74%", "2시간 18분 뒤", "미리보기", 0.74, halloweenGold)
	drawHalloweenQuota(img, 438, 163, "주간", "61%", "3일 6시간 뒤", "미리보기", 0.61, halloweenGold)
	drawHalloweenQuota(img, 858, 108, "5시간", "88%", "1시간 42분 뒤", "미리보기", 0.88, halloweenViolet)
	drawHalloweenQuota(img, 858, 163, "주간", "49%", "4일 11시간 뒤", "미리보기", 0.49, halloweenViolet)

	text(img, true, 1318, 49, "하드웨어 · 미리보기", 18, halloweenMuted)
	drawHalloweenMetric(img, 1318, "CPU", "34%", "52°C", 0.34, halloweenGold)
	drawHalloweenMetric(img, 1504, "GPU", "67%", "61°C", 0.67, halloweenCyan)
	drawHalloweenMetric(img, 1690, "RAM", "58%", "48°C", 0.58, halloweenViolet)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

// HalloweenOverlay renders the latest dashboard snapshot.
func HalloweenOverlay(snapshot func() Dashboard) Overlay {
	return func(elapsed time.Duration, counter uint64) ([]byte, error) {
		if snapshot == nil {
			return nil, fmt.Errorf("halloween overlay: nil dashboard snapshot")
		}
		return halloweenDashboardOverlay(snapshot(), elapsed, counter)
	}
}

func halloweenDashboardOverlay(d Dashboard, elapsed time.Duration, counter uint64) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	for _, panel := range []image.Rectangle{image.Rect(24, 20, 390, 210), image.Rect(410, 20, 1270, 210), image.Rect(1290, 20, 1896, 210)} {
		drawRounded(img, panel, 16, halloweenEdge)
		drawRounded(img, panel.Inset(2), 14, halloweenPanel)
	}
	base := d.At
	if base.IsZero() {
		base = time.Now()
	}
	weekdays := [...]string{"일요일", "월요일", "화요일", "수요일", "목요일", "금요일", "토요일"}
	text(img, true, 48, 49, fmt.Sprintf("%s %s", base.Format("2006년 01월 02일"), weekdays[base.Weekday()]), 20, halloweenGold)
	text(img, true, 46, 116, base.Format("15:04:05"), 64, halloweenInk)
	text(img, false, 48, 145, "할로윈의 밤", 19, halloweenMuted)
	text(img, false, 48, 181, fmt.Sprintf("LIVE  %s  #%06d", formatElapsed(elapsed), counter), 16, halloweenGold)
	text(img, true, 438, 49, "AI 사용량", 18, halloweenMuted)
	text(img, true, 438, 78, "CODEX", 20, halloweenGold)
	text(img, true, 858, 78, "CLAUDE", 20, halloweenViolet)
	drawHalloweenQuota(img, 438, 108, "5시간", d.Codex.FiveHour.Value, d.Codex.FiveHour.Reset, d.Codex.FiveHour.Received, d.Codex.FiveHour.Fraction, halloweenGold)
	drawHalloweenQuota(img, 438, 163, "주간", d.Codex.Weekly.Value, d.Codex.Weekly.Reset, d.Codex.Weekly.Received, d.Codex.Weekly.Fraction, halloweenGold)
	drawHalloweenQuota(img, 858, 108, "5시간", d.Claude.FiveHour.Value, d.Claude.FiveHour.Reset, d.Claude.FiveHour.Received, d.Claude.FiveHour.Fraction, halloweenViolet)
	drawHalloweenQuota(img, 858, 163, "주간", d.Claude.Weekly.Value, d.Claude.Weekly.Reset, d.Claude.Weekly.Received, d.Claude.Weekly.Fraction, halloweenViolet)
	text(img, true, 1318, 49, "하드웨어", 18, halloweenMuted)
	drawHalloweenMetric(img, 1318, d.Hardware.CPU.Label, d.Hardware.CPU.Usage, d.Hardware.CPU.Temperature, d.Hardware.CPU.Fraction, halloweenGold)
	drawHalloweenMetric(img, 1504, d.Hardware.GPU.Label, d.Hardware.GPU.Usage, d.Hardware.GPU.Temperature, d.Hardware.GPU.Fraction, halloweenCyan)
	drawHalloweenMetric(img, 1690, d.Hardware.RAM.Label, d.Hardware.RAM.Usage, d.Hardware.RAM.Temperature, d.Hardware.RAM.Fraction, halloweenViolet)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func drawHalloweenQuota(img draw.Image, x, y int, period, value, reset, received string, fraction float64, accent color.NRGBA) {
	text(img, false, x, y, period, 17, halloweenMuted)
	text(img, true, x+48, y, value, 28, halloweenInk)
	text(img, false, x+126, y, reset, 14, halloweenMuted)
	text(img, false, x+245, y, received, 11, halloweenMuted)
	drawRail(img, x, y+17, 326, fraction, accent)
}

func drawHalloweenMetric(img draw.Image, x int, label, usage, temp string, fraction float64, accent color.NRGBA) {
	text(img, false, x, 83, label, 16, halloweenMuted)
	text(img, true, x, 119, usage, 30, halloweenInk)
	text(img, false, x, 147, temp, 20, accent)
	drawRail(img, x, 166, 158, fraction, accent)
}
