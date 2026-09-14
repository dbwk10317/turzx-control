// SPDX-License-Identifier: GPL-3.0-or-later

// Package daemon contains the long-lived product daemon loops.
package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/render"
	"github.com/dbwk10317/turzx-control/internal/turzx"
	"github.com/google/gousb"
)

const (
	displayFrameRate = 25
	landscapeWidth   = 1920
	landscapeHeight  = 462
	nativeWidth      = 462
	nativeHeight     = 1920
	maxBrightness    = 102
	queueTimeout     = 1500 * time.Millisecond
	videoRetryLimit  = 3
	videoRetryReset  = 30 * time.Minute
	fallbackMessage  = "영상 출력 오류 · 정적 화면"
	fallbackWaiting  = "PNG 정적 화면 전송 중 · 다음 영상 재시도 대기"
	fallbackHeld     = "H264 재시도 한도 초과 · PNG 정적 화면 유지"
)

// DefaultTheme is the theme used when none is configured.
const DefaultTheme = "smon-halloween"

var themeOverlays = map[string]func(func() render.Dashboard) render.Overlay{
	"azure-ribbon":   render.AzureOverlay,
	"smon-halloween": render.HalloweenOverlay,
}

// Themes lists the supported theme IDs in sorted order.
func Themes() []string {
	ids := make([]string, 0, len(themeOverlays))
	for id := range themeOverlays {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// DisplayOptions configures the product display loop.
type DisplayOptions struct {
	FFmpeg       string
	Background   string
	Theme        string
	Timeout      time.Duration
	FlushTimeout time.Duration
	ChunkWait    time.Duration
	Brightness   byte
}

// Validate checks configuration that can be checked without starting FFmpeg
// or opening the panel. FFmpeg is intentionally resolved by the video attempt;
// a missing executable enters the PNG fallback path.
func (o DisplayOptions) Validate() error {
	if strings.TrimSpace(o.Background) == "" {
		return fmt.Errorf("display background is required")
	}
	if !strings.EqualFold(filepath.Ext(o.Background), ".mp4") {
		return fmt.Errorf("display background must be an MP4 file")
	}
	info, err := os.Stat(o.Background)
	if err != nil {
		return fmt.Errorf("display background: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("display background must be a non-empty regular file")
	}
	if themeOverlays[o.Theme] == nil {
		return fmt.Errorf("display theme %q is not supported", o.Theme)
	}
	if o.FlushTimeout <= 0 || o.FlushTimeout >= o.Timeout {
		return fmt.Errorf("display requires 0 < flush timeout < I/O timeout")
	}
	if o.ChunkWait <= 0 {
		return fmt.Errorf("display H264 chunk wait limit must be positive")
	}
	if o.Brightness > maxBrightness {
		return fmt.Errorf("display brightness must be in 0..%d", maxBrightness)
	}
	return nil
}

// DisplayState is the current display transport state.
type DisplayState struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type displayUSB interface {
	Sync(context.Context) ([]byte, error)
	SendPNG(context.Context, []byte) ([]byte, error)
	SendH264Stream(context.Context, io.ReadCloser, turzx.VideoOptions, time.Duration) (turzx.VideoReport, error)
	Close() error
}

type displayDeps struct {
	open  func(context.Context, time.Duration, time.Duration) (displayUSB, error)
	start func(context.Context, render.Options) (io.ReadCloser, error)
	wait  func(context.Context, time.Duration) bool
	now   func() time.Time
}

type displayBudget struct {
	attempts      int
	progressSince time.Time
}

var productionDisplayDeps = displayDeps{
	open: func(ctx context.Context, timeout, flush time.Duration) (displayUSB, error) {
		// Return an untyped nil on failure; a nil *turzx.USB inside the
		// interface would pass a nil check and panic in Close.
		device, err := turzx.OpenUSB(ctx, timeout, flush)
		if err != nil {
			return nil, err
		}
		return device, nil
	},
	start: func(ctx context.Context, options render.Options) (io.ReadCloser, error) {
		stream, err := render.Start(ctx, options)
		if err != nil {
			return nil, err
		}
		return stream, nil
	},
	wait: waitReconnect,
	now:  time.Now,
}

// RunDisplay owns the panel until ctx is canceled. USB operations, video
// streaming and PNG fallback are deliberately kept in one serial loop.
func RunDisplay(ctx context.Context, opts DisplayOptions, dashboard func() render.Dashboard, onState func(DisplayState)) error {
	return runDisplay(ctx, opts, dashboard, onState, productionDisplayDeps)
}

func runDisplay(ctx context.Context, opts DisplayOptions, dashboard func() render.Dashboard, onState func(DisplayState), deps displayDeps) error {
	if deps.open == nil {
		deps.open = productionDisplayDeps.open
	}
	if deps.start == nil {
		deps.start = productionDisplayDeps.start
	}
	if deps.wait == nil {
		deps.wait = productionDisplayDeps.wait
	}
	if deps.now == nil {
		deps.now = productionDisplayDeps.now
	}
	if err := opts.Validate(); err != nil {
		state(onState, "error", err.Error())
		return err
	}
	if dashboard == nil {
		dashboard = func() render.Dashboard { return render.Dashboard{} }
	}
	overlay := themeOverlays[opts.Theme](dashboard)
	fallbackOverlay := render.FallbackOverlay(overlay, fallbackMessage)
	state(onState, "starting", "표시 데몬 시작")

	backoff := time.Second
	budget := displayBudget{}
	for ctx.Err() == nil {
		err := runConnection(ctx, opts, overlay, fallbackOverlay, onState, deps, &budget, func() { backoff = time.Second })
		if ctx.Err() != nil {
			// Cancellation is the normal exit; only transport failures that
			// happened alongside it are worth reporting.
			if err = stripCancellation(err); err != nil {
				state(onState, "error", err.Error())
				return err
			}
			break
		}
		message := "표시 연결 종료"
		if err != nil {
			message = err.Error()
		}
		state(onState, "disconnected", message)
		if !deps.wait(ctx, backoff) {
			break
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	state(onState, "stopped", "표시 데몬 중지")
	return nil
}

// runConnection opens the panel once and drives it until the connection is
// lost or ctx ends. Every error already carries its UI prefix.
func runConnection(ctx context.Context, opts DisplayOptions, overlay, fallbackOverlay render.Overlay, onState func(DisplayState), deps displayDeps, budget *displayBudget, markSuccess func()) error {
	device, err := deps.open(ctx, opts.Timeout, opts.FlushTimeout)
	if err != nil {
		return fmt.Errorf("USB 열기 실패: %w", err)
	}
	if _, err := device.Sync(ctx); err != nil {
		return fmt.Errorf("USB 동기화 실패: %w", errors.Join(err, device.Close()))
	}
	err = errors.Join(runConnected(ctx, opts, overlay, fallbackOverlay, device, onState, deps, budget, markSuccess), device.Close())
	if err != nil {
		return fmt.Errorf("표시 연결 끊김: %w", err)
	}
	return nil
}

// runConnected alternates video attempts and PNG fallback on a synced panel.
// It returns nil when ctx ends and an error when the panel must be reopened.
func runConnected(ctx context.Context, opts DisplayOptions, overlay, fallbackOverlay render.Overlay, device displayUSB, onState func(DisplayState), deps displayDeps, budget *displayBudget, markSuccess func()) error {
	for ctx.Err() == nil {
		if budget.attempts > videoRetryLimit {
			return runPNG(ctx, device, fallbackOverlay, onState, fallbackHeld, deps.now, markSuccess)
		}
		videoErr, err := runVideo(ctx, opts, overlay, device, onState, deps, budget, markSuccess)
		if err != nil || ctx.Err() != nil {
			return err
		}
		state(onState, "starting", fmt.Sprintf("PNG 정적 화면 준비 중 · %v", videoErr))
		if budget.attempts > videoRetryLimit {
			return runPNG(ctx, device, fallbackOverlay, onState, fallbackHeld, deps.now, markSuccess)
		}
		retryDelay := time.Duration(1<<(budget.attempts-1)) * time.Second
		if err := runPNGUntil(ctx, device, fallbackOverlay, onState, retryDelay, deps.now, deps.wait, markSuccess); err != nil {
			return err
		}
	}
	return nil
}

// runVideo runs one H264 attempt. videoErr says why the video ended; err is a
// panel failure that ends the connection. The retry budget is charged only
// after the panel answers a resync, so a vanished device counts as a
// reconnect rather than a video failure.
func runVideo(ctx context.Context, opts DisplayOptions, overlay render.Overlay, device displayUSB, onState func(DisplayState), deps displayDeps, budget *displayBudget, markSuccess func()) (videoErr, err error) {
	stream, startErr := deps.start(ctx, render.Options{
		FFmpeg: opts.FFmpeg, Background: opts.Background, FrameRate: displayFrameRate,
		Overlay: overlay, OverlayInterval: time.Second,
	})
	if startErr == nil && stream == nil {
		startErr = errors.New("H264 renderer returned a nil stream")
	}
	if startErr != nil {
		if ctx.Err() != nil {
			return nil, startErr
		}
		budget.attempts++
		return startErr, nil
	}
	if ctx.Err() != nil {
		return nil, stream.Close()
	}
	state(onState, "starting", "영상 전송 준비 중")
	reportedVideo := false
	_, sendErr := device.SendH264Stream(ctx, stream, turzx.VideoOptions{
		FrameRate: displayFrameRate, Brightness: opts.Brightness, QueueTimeout: queueTimeout,
		OnProgress: func(turzx.VideoProgress) {
			now := deps.now()
			if budget.progressSince.IsZero() {
				budget.progressSince = now
			}
			if !reportedVideo {
				reportedVideo = true
				state(onState, "video", "H264 영상 전송 중")
			}
			if now.Sub(budget.progressSince) >= videoRetryReset {
				budget.attempts = 0
				budget.progressSince = now
			}
			markSuccess()
		},
	}, opts.ChunkWait)
	// The device already joins its stop error into sendErr; only the
	// renderer's own exit needs adding here.
	videoErr = errors.Join(sendErr, stream.Close())
	budget.progressSince = time.Time{}
	if ctx.Err() != nil {
		return nil, videoErr
	}
	if videoErr == nil {
		videoErr = errors.New("H264 stream ended")
	}
	if _, syncErr := device.Sync(ctx); syncErr != nil {
		return nil, fmt.Errorf("resync after H264 failure: %w", errors.Join(videoErr, syncErr))
	}
	budget.attempts++
	return videoErr, nil
}

// runPNGUntil sends the fallback PNG every second for duration.
func runPNGUntil(ctx context.Context, device displayUSB, overlay render.Overlay, onState func(DisplayState), duration time.Duration, now func() time.Time, wait func(context.Context, time.Duration) bool, markSuccess func()) error {
	started := now()
	for counter := uint64(0); duration > 0; counter++ {
		if err := sendFallbackPNG(ctx, device, overlay, now().Sub(started), counter, markSuccess); err != nil {
			return err
		}
		if counter == 0 {
			state(onState, "fallback", fallbackWaiting)
		}
		remaining := duration - now().Sub(started)
		if remaining <= 0 || !wait(ctx, min(remaining, time.Second)) {
			return nil
		}
	}
	return nil
}

// runPNG holds the fallback PNG until ctx ends or the panel fails.
func runPNG(ctx context.Context, device displayUSB, overlay render.Overlay, onState func(DisplayState), message string, now func() time.Time, markSuccess func()) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	started := now()
	for counter := uint64(0); ; counter++ {
		if err := sendFallbackPNG(ctx, device, overlay, now().Sub(started), counter, markSuccess); err != nil {
			return err
		}
		if counter == 0 {
			state(onState, "fallback", message)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func sendFallbackPNG(ctx context.Context, device displayUSB, overlay render.Overlay, elapsed time.Duration, counter uint64, markSuccess func()) error {
	frame, err := fallbackPNG(overlay(elapsed, counter))
	if err != nil {
		return err
	}
	if _, err := device.SendPNG(ctx, frame); err != nil {
		return fmt.Errorf("send PNG fallback: %w", err)
	}
	markSuccess()
	return nil
}

func fallbackPNG(data []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, fmt.Errorf("render PNG fallback overlay: %w", err)
	}
	overlay, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode PNG fallback overlay: %w", err)
	}
	landscape := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	draw.Draw(landscape, landscape.Bounds(), image.NewUniform(color.NRGBA{R: 8, G: 8, B: 14, A: 255}), image.Point{}, draw.Src)
	draw.Draw(landscape, landscape.Bounds(), overlay, overlay.Bounds().Min, draw.Over)
	// Rotate clockwise: landscape (x, y) lands at portrait (H-1-y, x).
	portrait := image.NewNRGBA(image.Rect(0, 0, nativeWidth, nativeHeight))
	for y := 0; y < landscapeHeight; y++ {
		for x := 0; x < landscapeWidth; x++ {
			src := landscape.PixOffset(x, y)
			dst := portrait.PixOffset(landscapeHeight-1-y, x)
			copy(portrait.Pix[dst:dst+4], landscape.Pix[src:src+4])
		}
	}
	return turzx.EncodePNG(portrait)
}

func state(onState func(DisplayState), status, message string) {
	if onState != nil {
		onState(DisplayState{Status: status, Message: message})
	}
}

func waitReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// stripCancellation removes context and USB cancellation errors from err,
// keeping wrapper context around whatever transport error remains.
func stripCancellation(err error) error {
	if err == nil {
		return nil
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		kept := make([]error, 0, len(many.Unwrap()))
		for _, child := range many.Unwrap() {
			if child = stripCancellation(child); child != nil {
				kept = append(kept, child)
			}
		}
		return errors.Join(kept...)
	}
	if one, ok := err.(interface{ Unwrap() error }); ok {
		inner := one.Unwrap()
		child := stripCancellation(inner)
		switch {
		case child == nil:
			return nil
		case child == inner:
			return err
		default:
			return child
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, gousb.TransferCancelled) {
		return nil
	}
	return err
}
