// SPDX-License-Identifier: GPL-3.0-or-later
//
// FFmpeg invocation follows the official FFmpeg command and filter documentation;
// no FFmpeg source code is copied.

// Package render produces the live H264 stream used by the G1 diagnostic and theme preview.
package render

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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	defaultFrameRate = 25
	overlayInterval  = 2 * time.Second
	closeGracePeriod = 250 * time.Millisecond
	stderrLimit      = 64 << 10
	landscapeWidth   = 1920
	landscapeHeight  = 462
)

var errClosed = errors.New("render stream closed")

// Options configures the G1 FFmpeg producer. OnOverlay runs synchronously when
// a new overlay is selected, initially at counter zero, and must
// return promptly so it cannot hold up frame delivery or shutdown.
type Options struct {
	FFmpeg     string
	Background string
	FrameRate  int
	Overlay    Overlay
	// OverlayInterval defaults to the two-second diagnostic cadence.
	OverlayInterval time.Duration
	OnOverlay       func(time.Time, uint64)
}

// Overlay renders one frame of the transparent overlay stream.
type Overlay func(elapsed time.Duration, counter uint64) ([]byte, error)

// Stream is an FFmpeg Annex B H264 stdout stream.
type Stream struct {
	stdout *os.File
	stdin  *os.File
	stderr *os.File
	cmd    *exec.Cmd
	ctx    context.Context
	cancel context.CancelCauseFunc
	args   []string

	killed atomic.Bool
	eof    atomic.Bool

	workers sync.WaitGroup
	feedErr chan error
	tail    tailBuffer

	waitOnce  sync.Once
	waitErr   error
	closeOnce sync.Once
}

// Start validates the local inputs and starts a paced FFmpeg process.
func Start(ctx context.Context, options Options) (*Stream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("start render: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start render: %w", err)
	}
	if strings.TrimSpace(options.FFmpeg) == "" {
		return nil, fmt.Errorf("start render: FFmpeg path is required")
	}
	if _, err := exec.LookPath(options.FFmpeg); err != nil {
		return nil, fmt.Errorf("start render: find FFmpeg: %w", err)
	}
	if !strings.EqualFold(filepath.Ext(options.Background), ".mp4") {
		return nil, fmt.Errorf("start render: background must be an mp4 file")
	}
	info, err := os.Stat(options.Background)
	if err != nil {
		return nil, fmt.Errorf("start render: stat background: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("start render: background is not a regular file")
	}
	if options.FrameRate == 0 {
		options.FrameRate = defaultFrameRate
	}
	if options.FrameRate < 1 || options.FrameRate > 120 {
		return nil, fmt.Errorf("start render: frame rate must be between 1 and 120")
	}
	if options.OverlayInterval < 0 {
		return nil, fmt.Errorf("start render: overlay interval must not be negative")
	}
	if options.OverlayInterval == 0 {
		options.OverlayInterval = overlayInterval
	}
	overlay := options.Overlay
	if overlay == nil {
		overlay = diagnosticOverlay
	}

	initial, err := overlay(0, 0)
	if err != nil {
		return nil, fmt.Errorf("start render: make initial overlay: %w", err)
	}
	args := ffmpegArgs(options.Background, options.FrameRate)
	runCtx, cancel := context.WithCancelCause(ctx)
	cmd := exec.CommandContext(runCtx, options.FFmpeg, args...)
	s := &Stream{
		cmd:     cmd,
		ctx:     runCtx,
		cancel:  cancel,
		args:    append([]string(nil), cmd.Args...),
		feedErr: make(chan error, 1),
		tail:    tailBuffer{limit: stderrLimit},
	}
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		if err == nil {
			s.killed.Store(true)
		}
		return err
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("start render: create stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		cancel(err)
		return nil, fmt.Errorf("start render: create stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		cancel(err)
		return nil, fmt.Errorf("start render: create stderr pipe: %w", err)
	}

	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		cancel(err)
		return nil, fmt.Errorf("start render: start FFmpeg: %w", err)
	}
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	s.stdin, s.stdout, s.stderr = stdinW, stdoutR, stderrR

	s.workers.Add(2)
	go s.feed(initial, options.FrameRate, overlay, options.OverlayInterval, options.OnOverlay)
	go func() {
		defer s.workers.Done()
		_, _ = io.Copy(&s.tail, s.stderr)
	}()
	return s, nil
}

// Read reads encoded Annex B H264 from FFmpeg stdout.
func (s *Stream) Read(p []byte) (int, error) {
	n, err := s.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		s.eof.Store(true)
	}
	return n, err
}

// Args returns the exact executable and arguments supplied to os/exec.
func (s *Stream) Args() []string { return append([]string(nil), s.args...) }

// Wait reaps FFmpeg exactly once and returns the same result to every caller.
func (s *Stream) Wait() error {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		_ = s.stdin.Close()
		s.workers.Wait()
		_ = s.stderr.Close()

		var feedErr error
		select {
		case feedErr = <-s.feedErr:
		default:
		}
		if err != nil && s.killed.Load() {
			if cause := context.Cause(s.ctx); cause != nil {
				err = cause
			}
		} else if err == nil && feedErr != nil && !errors.Is(feedErr, os.ErrClosed) {
			err = feedErr
		}
		if err != nil && !errors.Is(err, errClosed) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			if tail := strings.TrimSpace(s.tail.String()); tail != "" {
				err = fmt.Errorf("FFmpeg: %w; stderr: %s", err, tail)
			} else {
				err = fmt.Errorf("FFmpeg: %w", err)
			}
		}
		s.waitErr = err
	})
	return s.waitErr
}

// Close stops FFmpeg, closes all local pipe ends, and joins its workers.
func (s *Stream) Close() error {
	var wait <-chan error
	s.closeOnce.Do(func() {
		// If the reader already observed EOF, let Wait preserve FFmpeg's own
		// exit status during a short grace period. A child that only closes stdout
		// is still killed after the grace period so Close remains bounded.
		if s.eof.Load() {
			done := make(chan error, 1)
			wait = done
			go func() { done <- s.Wait() }()
		} else {
			s.cancel(fmt.Errorf("%w: %w", errClosed, context.Canceled))
		}
		_ = s.stdin.Close()
		_ = s.stdout.Close()
	})
	var err error
	if wait != nil {
		timer := time.NewTimer(closeGracePeriod)
		select {
		case err = <-wait:
			timer.Stop()
		case <-timer.C:
			s.cancel(fmt.Errorf("%w: %w", errClosed, context.Canceled))
			err = <-wait
		}
	} else {
		err = s.Wait()
	}
	if errors.Is(err, errClosed) {
		return nil
	}
	return err
}

func (s *Stream) feed(frame []byte, frameRate int, overlay Overlay, interval time.Duration, onOverlay func(time.Time, uint64)) {
	defer s.workers.Done()
	defer close(s.feedErr)

	started := time.Now()
	lastOverlay := started
	var counter uint64
	if onOverlay != nil {
		onOverlay(started, counter)
	}
	if err := writeAll(s.stdin, frame); err != nil {
		s.feedErr <- fmt.Errorf("feed initial overlay: %w", err)
		return
	}

	ticker := time.NewTicker(time.Second / time.Duration(frameRate))
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			if now.Sub(lastOverlay) >= interval {
				counter++
				var err error
				frame, err = overlay(now.Sub(started), counter)
				if err != nil {
					s.feedErr <- fmt.Errorf("make overlay: %w", err)
					s.cancel(err)
					return
				}
				lastOverlay = now
				if onOverlay != nil {
					onOverlay(now, counter)
				}
			}
			if err := writeAll(s.stdin, frame); err != nil {
				s.feedErr <- fmt.Errorf("feed overlay: %w", err)
				s.cancel(err)
				return
			}
		}
	}
}

func ffmpegArgs(background string, frameRate int) []string {
	filter := fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,setsar=1,setpts=PTS-STARTPTS[bg];[1:v]setpts=PTS-STARTPTS[ov];[bg][ov]overlay=0:0:shortest=1:eof_action=endall,fps=%d,transpose=clock,format=yuv420p[out]", landscapeWidth, landscapeHeight, landscapeWidth, landscapeHeight, frameRate)
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-stream_loop", "-1", "-i", background,
		"-f", "image2pipe", "-framerate", fmt.Sprint(frameRate), "-probesize", "32", "-analyzeduration", "0", "-threads", "1", "-c:v", "png", "-i", "pipe:0",
		"-filter_complex", filter, "-map", "[out]", "-an",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-bf", "0", "-g", fmt.Sprint(frameRate), "-keyint_min", fmt.Sprint(frameRate), "-sc_threshold", "0",
		"-b:v", "1500k", "-minrate", "1500k", "-maxrate", "1500k", "-bufsize", "1500k",
		"-x264-params", "nal-hrd=cbr:force-cfr=1", "-f", "h264", "pipe:1",
	}
}

func diagnosticOverlay(elapsed time.Duration, counter uint64) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, landscapeWidth, landscapeHeight))
	draw.Draw(img, image.Rect(24, 24, 720, 132), image.NewUniform(color.NRGBA{A: 176}), image.Point{}, draw.Src)
	text := fmt.Sprintf("LIVE  %s  #%06d", formatElapsed(elapsed), counter)
	drawScaledText(img, image.Pt(48, 48), text, 4)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func drawScaledText(dst draw.Image, at image.Point, text string, scale int) {
	bounds, _ := font.BoundString(basicfont.Face7x13, text)
	w := (bounds.Max.X - bounds.Min.X).Ceil()
	small := image.NewNRGBA(image.Rect(0, 0, w, basicfont.Face7x13.Height))
	d := font.Drawer{
		Dst:  small,
		Src:  image.NewUniform(color.NRGBA{R: 238, G: 244, B: 255, A: 255}),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(0, basicfont.Face7x13.Ascent),
	}
	d.DrawString(text)
	xdraw.NearestNeighbor.Scale(dst, image.Rect(at.X, at.Y, at.X+w*scale, at.Y+small.Bounds().Dy()*scale), small, small.Bounds(), draw.Over, nil)
}

func formatElapsed(d time.Duration) string {
	total := int64(d / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, total/60%60, total%60)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
		return written, nil
	}
	if extra := len(b.buf) + len(p) - b.limit; extra > 0 {
		copy(b.buf, b.buf[extra:])
		b.buf = b.buf[:len(b.buf)-extra]
	}
	b.buf = append(b.buf, p...)
	return written, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
