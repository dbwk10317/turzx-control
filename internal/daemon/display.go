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
)

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
	switch o.Theme {
	case "azure-ribbon", "smon-halloween":
	default:
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

type displayStream interface {
	io.ReadCloser
}

type displayDeps struct {
	open  func(context.Context, time.Duration, time.Duration) (displayUSB, error)
	start func(context.Context, render.Options) (displayStream, error)
	wait  func(context.Context, time.Duration) bool
	now   func() time.Time
}

type displayBudget struct {
	attempts       int
	progressSince  time.Time
	lastProgressAt time.Time
}

var productionDisplayDeps = displayDeps{
	open: func(ctx context.Context, timeout, flush time.Duration) (displayUSB, error) {
		return turzx.OpenUSB(ctx, timeout, flush)
	},
	start: func(ctx context.Context, options render.Options) (displayStream, error) {
		return render.Start(ctx, options)
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
	if ctx == nil {
		return fmt.Errorf("display context is nil")
	}
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
	overlay := displayOverlay(opts.Theme, dashboard)
	fallbackOverlay := render.FallbackOverlay(overlay, fallbackMessage)
	state(onState, "starting", "표시 데몬 시작")

	backoff := time.Second
	budget := displayBudget{}
	for {
		if err := ctx.Err(); err != nil {
			state(onState, "stopped", "표시 데몬 중지")
			return nil
		}
		device, err := deps.open(ctx, opts.Timeout, opts.FlushTimeout)
		if err == nil {
			_, err = device.Sync(ctx)
			if err != nil {
				closeErr := device.Close()
				if ctx.Err() != nil {
					if cleanupErr := stripCancellation(errors.Join(err, closeErr)); cleanupErr != nil {
						return cleanupErr
					}
					state(onState, "stopped", "표시 데몬 중지")
					return nil
				}
				state(onState, "disconnected", joinMessage("USB 동기화 실패", err, closeErr))
				if !deps.wait(ctx, backoff) {
					state(onState, "stopped", "표시 데몬 중지")
					return nil
				}
				backoff = nextBackoff(backoff)
				continue
			}
			err = runConnected(ctx, opts, overlay, fallbackOverlay, device, onState, deps, &budget, func() { backoff = time.Second })
			closeErr := device.Close()
			if err == nil && closeErr == nil {
				state(onState, "stopped", "표시 데몬 중지")
				return nil
			}
			if ctx.Err() != nil {
				if cleanupErr := stripCancellation(errors.Join(err, closeErr)); cleanupErr != nil {
					state(onState, "error", cleanupErr.Error())
					return cleanupErr
				}
				state(onState, "stopped", "표시 데몬 중지")
				return nil
			}
			state(onState, "disconnected", joinMessage("표시 연결 끊김", err, closeErr))
		} else {
			var closeErr error
			if device != nil {
				closeErr = device.Close()
			}
			if ctx.Err() != nil {
				if cleanupErr := stripCancellation(errors.Join(err, closeErr)); cleanupErr != nil {
					return cleanupErr
				}
				state(onState, "stopped", "표시 데몬 중지")
				return nil
			}
			state(onState, "disconnected", joinMessage("USB 열기 실패", err, closeErr))
		}
		if !deps.wait(ctx, backoff) {
			state(onState, "stopped", "표시 데몬 중지")
			return nil
		}
		backoff = nextBackoff(backoff)
	}
}

func runConnected(ctx context.Context, opts DisplayOptions, overlay, fallbackOverlay render.Overlay, device displayUSB, onState func(DisplayState), deps displayDeps, budget *displayBudget, markSuccess func()) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if budget.attempts > videoRetryLimit {
			return runPNG(ctx, device, fallbackOverlay, onState, "H264 재시도 한도 초과 · PNG 정적 화면 유지", deps.now, markSuccess)
		}
		stream, startErr := deps.start(ctx, render.Options{
			FFmpeg: opts.FFmpeg, Background: opts.Background, FrameRate: displayFrameRate,
			Overlay: overlay, OverlayInterval: time.Second,
		})
		if startErr == nil {
			if stream == nil {
				startErr = fmt.Errorf("H264 renderer returned a nil stream")
			}
		}
		if ctx.Err() != nil {
			if stream != nil {
				return stripCancellation(stream.Close())
			}
			return nil
		}
		if startErr == nil {
			state(onState, "starting", "영상 전송 준비 중")
			reportedVideo := false
			report, sendErr := device.SendH264Stream(ctx, stream, turzx.VideoOptions{
				FrameRate: displayFrameRate, Brightness: opts.Brightness, QueueTimeout: queueTimeout,
				OnProgress: func(progress turzx.VideoProgress) {
					at := progress.At
					if at.IsZero() {
						at = deps.now()
					}
					if budget.progressSince.IsZero() {
						budget.progressSince = at
					}
					if !reportedVideo {
						reportedVideo = true
						state(onState, "video", "H264 영상 전송 중")
					}
					budget.lastProgressAt = at
					if at.Sub(budget.progressSince) >= videoRetryReset {
						budget.attempts = 0
						budget.progressSince = at
					}
					markSuccess()
				},
			}, opts.ChunkWait)
			closeErr := stream.Close()
			videoErr := errors.Join(sendErr, report.StopError, closeErr)
			if ctx.Err() != nil {
				return errors.Join(stripCancellation(errors.Join(sendErr, closeErr)), report.StopError)
			}
			budget.progressSince = time.Time{}
			budget.lastProgressAt = time.Time{}
			budget.attempts++
			if videoErr == nil {
				videoErr = fmt.Errorf("H264 stream ended")
			}
			state(onState, "starting", fmt.Sprintf("PNG 정적 화면 준비 중 · %v", videoErr))
			if _, syncErr := device.Sync(ctx); syncErr != nil {
				return fmt.Errorf("resync after H264 failure: %w", syncErr)
			}
		} else {
			state(onState, "starting", fmt.Sprintf("PNG 정적 화면 준비 중 · %v", startErr))
			budget.attempts++
		}

		if budget.attempts > videoRetryLimit {
			return runPNG(ctx, device, fallbackOverlay, onState, "H264 재시도 한도 초과 · PNG 정적 화면 유지", deps.now, markSuccess)
		}
		if err := runPNGUntil(ctx, device, fallbackOverlay, onState, time.Duration(1<<(budget.attempts-1))*time.Second, deps.now, deps.wait, markSuccess); err != nil {
			if ctx.Err() != nil {
				return stripCancellation(err)
			}
			state(onState, "error", fmt.Sprintf("PNG 정적 화면 전송 실패 · %v", err))
			return err
		}
	}
}

func runPNGUntil(ctx context.Context, device displayUSB, overlay render.Overlay, onState func(DisplayState), duration time.Duration, now func() time.Time, wait func(context.Context, time.Duration) bool, markSuccess func()) error {
	if duration <= 0 {
		return nil
	}
	started := now()
	var counter uint64
	if err := sendFallbackPNG(ctx, device, overlay, now().Sub(started), counter, markSuccess); err != nil {
		if ctx.Err() != nil {
			return stripCancellation(err)
		}
		return err
	}
	state(onState, "fallback", fallbackWaiting)
	for {
		remaining := duration - now().Sub(started)
		if remaining <= 0 {
			return nil
		}
		step := remaining
		if step > time.Second {
			step = time.Second
		}
		if !wait(ctx, step) {
			return nil
		}
		counter++
		if err := sendFallbackPNG(ctx, device, overlay, now().Sub(started), counter, markSuccess); err != nil {
			if ctx.Err() != nil {
				return stripCancellation(err)
			}
			state(onState, "error", fmt.Sprintf("PNG 정적 화면 전송 실패 · %v", err))
			return err
		}
	}
}

func runPNG(ctx context.Context, device displayUSB, overlay render.Overlay, onState func(DisplayState), message string, now func() time.Time, markSuccess func()) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	started := now()
	var counter uint64
	reportedFallback := false
	for {
		if err := sendFallbackPNG(ctx, device, overlay, now().Sub(started), counter, markSuccess); err != nil {
			if ctx.Err() != nil {
				return stripCancellation(err)
			}
			state(onState, "error", fmt.Sprintf("PNG 정적 화면 전송 실패 · %v", err))
			return err
		}
		if !reportedFallback {
			state(onState, "fallback", message)
			reportedFallback = true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			counter++
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
	portrait := image.NewNRGBA(image.Rect(0, 0, nativeWidth, nativeHeight))
	for y := 0; y < landscapeHeight; y++ {
		for x := 0; x < landscapeWidth; x++ {
			portrait.Set(landscapeHeight-1-y, x, landscape.At(x, y))
		}
	}
	return turzx.EncodePNG(portrait)
}

func displayOverlay(theme string, dashboard func() render.Dashboard) render.Overlay {
	switch theme {
	case "azure-ribbon":
		return render.AzureOverlay(dashboard)
	case "smon-halloween":
		return render.HalloweenOverlay(dashboard)
	default:
		return func(time.Duration, uint64) ([]byte, error) { return nil, fmt.Errorf("unsupported theme %q", theme) }
	}
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

func nextBackoff(delay time.Duration) time.Duration {
	if delay >= 30*time.Second {
		return 30 * time.Second
	}
	if delay > 15*time.Second {
		return 30 * time.Second
	}
	return delay * 2
}

func joinMessage(prefix string, errs ...error) string {
	joined := errors.Join(errs...)
	if joined == nil {
		return prefix
	}
	return fmt.Sprintf("%s: %v", prefix, joined)
}

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
		child := stripCancellation(one.Unwrap())
		if child == nil {
			return nil
		}
		return child
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, gousb.TransferCancelled) {
		return nil
	}
	return err
}
