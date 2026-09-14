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
	"path/filepath"
	"strings"
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
	onProgress  func(report func(turzx.VideoProgress))
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

// SendH264Stream mirrors the device: the stop error is joined into the
// returned error.
func (f *fakeUSB) SendH264Stream(ctx context.Context, stream io.ReadCloser, opts turzx.VideoOptions, _ time.Duration) (turzx.VideoReport, error) {
	if f.onVideo != nil {
		f.onVideo()
	}
	if f.onProgress != nil && opts.OnProgress != nil {
		f.onProgress(opts.OnProgress)
	}
	if f.videoErr != nil {
		_ = stream.Close()
		return f.videoReport, errors.Join(f.videoErr, f.videoReport.StopError)
	}
	<-ctx.Done()
	_ = stream.Close()
	return f.videoReport, errors.Join(ctx.Err(), f.videoReport.StopError)
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
	closeErr error
}

func (f *fakeStream) Read([]byte) (int, error) { return 0, errors.New("fake stream read") }
func (f *fakeStream) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return f.closeErr
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
	path := filepath.Join(t.TempDir(), "background.mp4")
	if err := os.WriteFile(path, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeClock advances only when the loop waits, so tests run without sleeping.
type fakeClock struct{ now time.Time }

func (c *fakeClock) deps(base displayDeps) displayDeps {
	base.now = func() time.Time { return c.now }
	base.wait = func(ctx context.Context, delay time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		c.now = c.now.Add(delay)
		return true
	}
	return base
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

func TestThemesMatchValidate(t *testing.T) {
	ids := Themes()
	if len(ids) != 2 || ids[0] != "azure-ribbon" || ids[1] != DefaultTheme {
		t.Fatalf("Themes() = %v", ids)
	}
	for _, id := range ids {
		opts := testDisplayOptions(testBackground(t))
		opts.Theme = id
		if err := opts.Validate(); err != nil {
			t.Fatalf("theme %q rejected: %v", id, err)
		}
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

// A failed USB open must produce an untyped nil interface; a typed nil would
// pass nil checks and panic in Close. Invalid timeouts fail before libusb.
func TestProductionOpenFailureReturnsUntypedNil(t *testing.T) {
	device, err := productionDisplayDeps.open(context.Background(), 0, 0)
	if err == nil {
		t.Fatal("open with invalid timeouts succeeded")
	}
	if device != nil {
		t.Fatalf("device = %#v, want untyped nil", device)
	}
}

func TestRunDisplayOpenFailureReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opens := 0
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) {
		opens++
		return nil, errors.New("panel unplugged")
	}
	var delays []time.Duration
	deps.wait = func(ctx context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		if len(delays) == 2 {
			cancel()
			return false
		}
		return true
	}
	var states []DisplayState
	err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, func(s DisplayState) { states = append(states, s) }, deps)
	if err != nil {
		t.Fatalf("runDisplay() error = %v", err)
	}
	if opens != 2 {
		t.Fatalf("opens = %d, want 2", opens)
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("backoff = %v, want 1s then 2s", delays)
	}
	if states[1].Status != "disconnected" || !strings.HasPrefix(states[1].Message, "USB 열기 실패: panel unplugged") {
		t.Fatalf("state = %+v", states[1])
	}
	if last := states[len(states)-1]; last.Status != "stopped" {
		t.Fatalf("last state = %+v, want stopped", last)
	}
}

func TestRunDisplayCancellationClosesVideoAndUSB(t *testing.T) {
	usb := &fakeUSB{}
	stream := &fakeStream{}
	ctx, cancel := context.WithCancel(context.Background())
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) {
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
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) { return stream, nil }
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
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) {
		return nil, errors.New("ffmpeg executable missing")
	}
	var states []DisplayState
	if err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, func(s DisplayState) { states = append(states, s) }, deps); err != nil {
		t.Fatalf("RunDisplay() error = %v", err)
	}
	if usb.pngs < 1 {
		t.Fatalf("PNG sends = %d, want at least 1", usb.pngs)
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
	if fallbackIndex < 0 {
		t.Fatalf("states = %#v, want fallback", states)
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
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) {
		return nil, errors.New("renderer unavailable")
	}
	err := runDisplay(ctx, testDisplayOptions(testBackground(t)), nil, nil, deps)
	if err == nil || !strings.Contains(err.Error(), "PNG write failed") || errors.Is(err, context.Canceled) {
		t.Fatalf("runDisplay() error = %v, want PNG write failure without cancellation", err)
	}
}

func TestRunDisplayCanceledTransferIsNormal(t *testing.T) {
	usb := &fakeUSB{pngErr: errors.Join(context.Canceled, gousb.TransferCancelled)}
	ctx, cancel := context.WithCancel(context.Background())
	usb.onPNG = cancel
	deps := productionDisplayDeps
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) {
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
	clock := &fakeClock{now: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)}
	deps := clock.deps(productionDisplayDeps)
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) {
		opens++
		if opens == 1 {
			return first, nil
		}
		return second, nil
	}
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) {
		starts++
		if starts == 1 {
			return &fakeStream{}, nil
		}
		return nil, errors.New("renderer unavailable")
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

// A device that vanishes mid-video fails the resync; that is a reconnect,
// not a video failure, so the retry budget is left alone.
func TestRunConnectedDeviceLossDoesNotChargeVideoBudget(t *testing.T) {
	usb := &fakeUSB{videoErr: errors.New("transfer: no device"), syncErrs: []error{errors.New("sync: no device")}}
	deps := productionDisplayDeps
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) { return &fakeStream{}, nil }
	budget := displayBudget{}
	overlay := render.HalloweenPreviewOverlay
	err := runConnected(context.Background(), testDisplayOptions(testBackground(t)), overlay, overlay, usb, nil, deps, &budget, func() {})
	if err == nil || !strings.Contains(err.Error(), "resync after H264 failure") {
		t.Fatalf("runConnected() error = %v, want resync failure", err)
	}
	if budget.attempts != 0 {
		t.Fatalf("attempts = %d, want 0 after device loss", budget.attempts)
	}
}

func TestRunConnectedResetsVideoBudgetAfterSustainedProgress(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)}
	usb := &fakeUSB{videoErr: errors.New("video failed")}
	usb.onProgress = func(report func(turzx.VideoProgress)) {
		report(turzx.VideoProgress{})
		clock.now = clock.now.Add(videoRetryReset)
		report(turzx.VideoProgress{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := clock.deps(productionDisplayDeps)
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) { return &fakeStream{}, nil }
	deps.wait = func(context.Context, time.Duration) bool { cancel(); return false }
	budget := displayBudget{attempts: videoRetryLimit}
	var states []DisplayState
	overlay := render.HalloweenPreviewOverlay
	err := runConnected(ctx, testDisplayOptions(testBackground(t)), overlay, overlay, usb, func(s DisplayState) { states = append(states, s) }, deps, &budget, func() {})
	if err != nil {
		t.Fatalf("runConnected() error = %v", err)
	}
	// 30 minutes of progress zeroes the budget; the failure that followed charges one.
	if budget.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", budget.attempts)
	}
	sawVideo := false
	for _, s := range states {
		sawVideo = sawVideo || s.Status == "video"
	}
	if !sawVideo {
		t.Fatalf("states = %+v, want video", states)
	}
}

func TestRunDisplayExhaustsVideoRetriesBeforeHoldingPNG(t *testing.T) {
	usb := &fakeUSB{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := 0
	var delays []time.Duration
	clock := &fakeClock{now: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)}
	deps := clock.deps(productionDisplayDeps)
	deps.open = func(context.Context, time.Duration, time.Duration) (displayUSB, error) { return usb, nil }
	deps.start = func(context.Context, render.Options) (io.ReadCloser, error) {
		starts++
		return nil, errors.New("renderer unavailable")
	}
	wait := deps.wait
	deps.wait = func(ctx context.Context, delay time.Duration) bool {
		if !wait(ctx, delay) {
			return false
		}
		delays = append(delays, delay)
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

func TestStripCancellationKeepsWrapperWhenNothingStripped(t *testing.T) {
	err := fmt.Errorf("send PNG fallback: %w", errors.New("USB write failed"))
	if clean := stripCancellation(err); clean != err {
		t.Fatalf("stripCancellation() = %v, want the original wrapper", clean)
	}
	if clean := stripCancellation(fmt.Errorf("resync: %w", context.Canceled)); clean != nil {
		t.Fatalf("stripCancellation() = %v, want nil", clean)
	}
}
