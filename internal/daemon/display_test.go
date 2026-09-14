// SPDX-License-Identifier: GPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dbwk10317/turzx-control/internal/render"
	"github.com/dbwk10317/turzx-control/internal/turzx"
	"github.com/google/gousb"
)

type fakeUSB struct {
	mu          sync.Mutex
	syncs       int
	pngs        int
	closed      int
	pngErr      error
	onPNG       func()
	onVideo     func()
	videoReport turzx.VideoReport
	videoErr    error
	syncErrs    []error
	closeErr    error
}

func (f *fakeUSB) Sync(context.Context) ([]byte, error) {
	f.mu.Lock()
	f.syncs++
	syncIndex := f.syncs - 1
	var err error
	if syncIndex < len(f.syncErrs) {
		err = f.syncErrs[syncIndex]
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return []byte{turzx.CmdSync, 0xc8}, nil
}

func (f *fakeUSB) SendPNG(context.Context, []byte) ([]byte, error) {
	f.mu.Lock()
	f.pngs++
	onPNG := f.onPNG
	err := f.pngErr
	f.mu.Unlock()
	if onPNG != nil {
		onPNG()
	}
	return []byte{turzx.CmdUploadPNG, 0xc8}, err
}

func (f *fakeUSB) SendH264Stream(ctx context.Context, stream io.ReadCloser, _ turzx.VideoOptions, _ time.Duration) (turzx.VideoReport, error) {
	if f.onVideo != nil {
		f.onVideo()
	}
	if f.videoErr != nil {
		_ = stream.Close()
		return f.videoReport, f.videoErr
	}
	<-ctx.Done()
	_ = stream.Close()
	return f.videoReport, ctx.Err()
}

func (f *fakeUSB) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return f.closeErr
}

type fakeStream struct {
	mu       sync.Mutex
	closed   int
	waited   int
	closeErr error
	waitErr  error
}

func (f *fakeStream) Read([]byte) (int, error) { return 0, errors.New("fake stream read") }
func (f *fakeStream) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return f.closeErr
}
func (f *fakeStream) Wait() error {
	f.mu.Lock()
	f.waited++
	f.mu.Unlock()
	return f.waitErr
}

func testDisplayOptions(background string) DisplayOptions {
	return DisplayOptions{
		FFmpeg:       "missing-ffmpeg",
		Background:   background,
		Theme:        "smon-halloween",
		Timeout:      2 * time.Second,
		FlushTimeout: time.Second,
		ChunkWait:    time.Second,
	}
}

func testBackground(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "\\background.mp4"
	if err := os.WriteFile(path, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDisplayOptionsValidate(t *testing.T) {
	background := testBackground(t)
	opts := testDisplayOptions(background)
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	for name, mutate := range map[string]func(*DisplayOptions){
		"background extension": func(o *DisplayOptions) { o.Background = background + ".txt" },
		"theme":                func(o *DisplayOptions) { o.Theme = "unknown" },
		"flush timeout":        func(o *DisplayOptions) { o.FlushTimeout = o.Timeout },
		"chunk wait":           func(o *DisplayOptions) { o.ChunkWait = 0 },
		"brightness":           func(o *DisplayOptions) { o.Brightness = maxBrightness + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := opts
			mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestFallbackPNGIsPortraitRGBA(t *testing.T) {
	landscape, err := render.HalloweenPreviewOverlay(0, 7)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fallbackPNG(landscape, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := turzx.ValidatePNG(payload); err != nil {
		t.Fatalf("fallback PNG invalid: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Bounds(); got != image.Rect(0, 0, nativeWidth, nativeHeight) {
		t.Fatalf("bounds = %v", got)
	}
}

func TestFallbackPNGRotatesPixelsClockwise(t *testing.T) {
	landscape := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	landscape.SetNRGBA(123, 45, color.NRGBA{R: 255, G: 17, B: 33, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, landscape); err != nil {
		t.Fatal(err)
	}
	payload, err := fallbackPNG(encoded.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := decoded.At(landscapeHeight-1-45, 123).RGBA()
	if r != 0xffff || g != 0x1111 || b != 0x2121 || a != 0xffff {
		t.Fatalf("rotated pixel = %#v %#v %#v %#v", r, g, b, a)
	}
}

func TestRunDisplayCancellationClosesVideoAndUSB(t *testing.T) {
	usb := &fakeUSB{}
	stream := &fakeStream{}
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (displayStream, error) { return stream, nil }
	ctx, cancel := context.WithCancel(context.Background())
	deps.start = func(context.Context, render.Options) (displayStream, error) {
		cancel()
		return stream, nil
	}
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps); err != nil {
		t.Fatalf("RunDisplay() error = %v", err)
	}
	if usb.closed != 1 {
		t.Fatalf("USB Close calls = %d, want 1", usb.closed)
	}
	if stream.closed < 1 {
		t.Fatalf("stream cleanup = closed %d, want close", stream.closed)
	}
}

func TestRunDisplayPreservesStopErrorDuringCancellation(t *testing.T) {
	stopErr := errors.New("video stop failed")
	usb := &fakeUSB{videoReport: turzx.VideoReport{StopError: stopErr}}
	stream := &fakeStream{}
	ctx, cancel := context.WithCancel(context.Background())
	usb.onVideo = cancel
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (displayStream, error) { return stream, nil }
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps); !errors.Is(err, stopErr) {
		t.Fatalf("runDisplay() error = %v, want stop error", err)
	}
}

func TestRunDisplayMissingFFmpegUsesPNGFallback(t *testing.T) {
	usb := &fakeUSB{}
	ctx, cancel := context.WithCancel(context.Background())
	usb.onPNG = func() {
		usb.mu.Lock()
		count := usb.pngs
		usb.mu.Unlock()
		if count == 1 {
			cancel()
		}
	}
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (displayStream, error) {
		return nil, errors.New("ffmpeg executable missing")
	}
	var states []DisplayState
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, func(s DisplayState) { states = append(states, s) }, deps); err != nil {
		t.Fatalf("RunDisplay() error = %v", err)
	}
	if usb.pngs < 1 {
		t.Fatalf("PNG sends = %d, want at least 1", usb.pngs)
	}
	foundFallback := false
	for _, s := range states {
		if s.Status == "fallback" {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatalf("states = %#v, want fallback", states)
	}
	startingIndex, fallbackIndex := -1, -1
	for i, s := range states {
		if s.Status == "starting" {
			startingIndex = i
		}
		if s.Status == "fallback" && fallbackIndex < 0 {
			fallbackIndex = i
		}
	}
	if startingIndex < 0 || fallbackIndex <= startingIndex {
		t.Fatalf("states = %#v, want starting before fallback", states)
	}
}

func TestRunDisplayPreservesPNGErrorWhenCanceled(t *testing.T) {
	usb := &fakeUSB{pngErr: errors.Join(context.Canceled, errors.New("PNG write failed"))}
	ctx, cancel := context.WithCancel(context.Background())
	usb.onPNG = cancel
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (displayStream, error) {
		return nil, errors.New("renderer unavailable")
	}
	err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps)
	if err == nil || err.Error() != "PNG write failed" {
		t.Fatalf("runDisplay() error = %v, want PNG write failure", err)
	}
}

func TestRunDisplayCanceledTransferIsNormal(t *testing.T) {
	usb := &fakeUSB{pngErr: errors.Join(context.Canceled, gousb.TransferCancelled)}
	ctx, cancel := context.WithCancel(context.Background())
	usb.onPNG = cancel
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (displayStream, error) {
		return nil, errors.New("renderer unavailable")
	}
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps); err != nil {
		t.Fatalf("runDisplay() error = %v, want nil for cancellation", err)
	}
}

func TestRunPNGUntilTransferErrorWithLiveContextIsRetained(t *testing.T) {
	usb := &fakeUSB{pngErr: gousb.TransferCancelled}
	states := make([]DisplayState, 0, 1)
	err := runPNGUntil(context.Background(), usb, render.HalloweenPreviewOverlay, func(s DisplayState) { states = append(states, s) }, time.Second, time.Now, func(context.Context, time.Duration) bool { return false }, func() {})
	if !errors.Is(err, gousb.TransferCancelled) {
		t.Fatalf("runPNGUntil() error = %v, want transfer cancellation", err)
	}
	for _, state := range states {
		if state.Status == "fallback" {
			t.Fatalf("states = %#v, fallback reported before PNG success", states)
		}
	}
}

func TestRunDisplayRetryBudgetSurvivesUSBReconnect(t *testing.T) {
	first := &fakeUSB{videoErr: errors.New("video failed"), pngErr: errors.New("first PNG failed"), syncErrs: []error{nil, nil}}
	second := &fakeUSB{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opens := 0
	starts := 0
	clock := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) {
		opens++
		if opens == 1 {
			return first, nil
		}
		return second, nil
	}
	deps.start = func(context.Context, render.Options) (displayStream, error) {
		starts++
		if starts == 1 {
			return &fakeStream{}, nil
		}
		return nil, errors.New("renderer unavailable")
	}
	deps.now = func() time.Time { return clock }
	deps.wait = func(ctx context.Context, delay time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		clock = clock.Add(delay)
		return true
	}
	second.onPNG = func() {
		second.mu.Lock()
		count := second.pngs
		second.mu.Unlock()
		if count >= 9 {
			cancel()
		}
	}
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps); err != nil {
		t.Fatalf("runDisplay() error = %v", err)
	}
	if opens != 2 {
		t.Fatalf("USB opens = %d, want 2", opens)
	}
	if first.closed != 1 {
		t.Fatalf("first USB closes = %d, want 1", first.closed)
	}
	if starts != videoRetryLimit+1 {
		t.Fatalf("renderer starts = %d, want %d across reconnect", starts, videoRetryLimit+1)
	}
}

func TestRunDisplayExhaustsVideoRetriesBeforeHoldingPNG(t *testing.T) {
	usb := &fakeUSB{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := 0
	var delays []time.Duration
	clock := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (displayStream, error) {
		starts++
		return nil, errors.New("renderer unavailable")
	}
	deps.now = func() time.Time { return clock }
	deps.wait = func(ctx context.Context, delay time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		delays = append(delays, delay)
		clock = clock.Add(delay)
		return true
	}
	usb.onPNG = func() {
		usb.mu.Lock()
		count := usb.pngs
		usb.mu.Unlock()
		if count >= 11 {
			cancel()
		}
	}
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps); err != nil {
		t.Fatalf("runDisplay() error = %v", err)
	}
	if starts != videoRetryLimit+1 {
		t.Fatalf("renderer starts = %d, want %d", starts, videoRetryLimit+1)
	}
	if len(delays) != 1+2+4 {
		t.Fatalf("retry delay steps = %v, want 1+2+4 one-second steps", delays)
	}
	for _, delay := range delays {
		if delay != time.Second {
			t.Fatalf("retry delay step = %s, want 1s", delay)
		}
	}
}

func TestStripCancellationKeepsJoinedTransportError(t *testing.T) {
	transportErr := errors.New("USB write failed")
	joined := errors.Join(context.Canceled, fmt.Errorf("wrapped: %w", transportErr), context.DeadlineExceeded)
	clean := stripCancellation(joined)
	if errors.Is(clean, context.Canceled) || errors.Is(clean, context.DeadlineExceeded) {
		t.Fatalf("cancellation leaked: %v", clean)
	}
	if !errors.Is(clean, transportErr) {
		t.Fatalf("transport error lost: %v", clean)
	}
}
