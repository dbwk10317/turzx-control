// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/render"
	"github.com/dbwk10317/turzx-control/internal/turzx"
)

// runLive is a bounded G1 experiment, not the product daemon or theme renderer.
func runLive(background, ffmpeg, output string, duration, timeout, flush, chunkWait time.Duration, opts turzx.VideoOptions) error {
	if !strings.EqualFold(filepath.Ext(background), ".mp4") {
		return fmt.Errorf("background must be an MP4 file")
	}
	file, err := os.Open(background)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		_ = file.Close()
		return fmt.Errorf("background must be a non-empty regular file: %v", err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	if err := errors.Join(hashErr, file.Close()); err != nil {
		return err
	}
	ffmpeg, err = exec.LookPath(ffmpeg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var device *turzx.USB
	var destination *os.File
	if output != "" {
		destination, err = os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
	} else {
		device, err = turzx.OpenUSB(ctx, timeout, flush)
		if err != nil {
			return err
		}
		if _, err := device.Sync(ctx); err != nil {
			return errors.Join(err, device.Close())
		}
	}
	liveCtx, stop := context.WithTimeout(ctx, duration)
	defer stop()
	started := time.Now()
	stream, startErr := render.Start(liveCtx, render.Options{
		FFmpeg: ffmpeg, Background: background, FrameRate: int(opts.FrameRate),
		OnOverlay: func(at time.Time, counter uint64) {
			// Diagnostic sample times, not device display acknowledgements.
			_ = json.NewEncoder(os.Stderr).Encode(struct {
				Step      string    `json:"step"`
				SampledAt time.Time `json:"sampled_at"`
				Counter   uint64    `json:"counter"`
			}{"overlay", at, counter})
		},
	})
	var encoderArgs []string
	var report turzx.VideoReport
	var runErr, closeErr error
	if startErr == nil {
		encoderArgs = stream.Args()
		if destination != nil {
			report.Bytes, runErr = io.Copy(destination, stream)
		} else {
			report, runErr = device.SendH264Stream(liveCtx, stream, opts, chunkWait)
		}
		closeErr = stream.Close()
	}
	if destination != nil {
		closeErr = errors.Join(closeErr, destination.Close())
	}
	if device != nil {
		closeErr = errors.Join(closeErr, device.Close())
	}
	err = errors.Join(startErr, runErr, closeErr)
	result := struct {
		Step             string    `json:"step"`
		StartedAt        time.Time `json:"started_at"`
		ElapsedMS        int64     `json:"elapsed_ms"`
		BackgroundSHA256 string    `json:"background_sha256"`
		FFmpeg           string    `json:"ffmpeg"`
		EncoderArgs      []string  `json:"encoder_args"`
		ChunkWaitLimitMS int64     `json:"chunk_wait_limit_ms"`
		Bytes            int64     `json:"bytes"`
		Chunks           int       `json:"chunks"`
		ChunkSize        int       `json:"chunk_size"`
		MaxQueueDepth    byte      `json:"max_queue_depth"`
		MaxChunkWaitMS   int64     `json:"max_chunk_wait_ms"`
		StopResponse     string    `json:"stop_response_hex,omitempty"`
		DurationReached  bool      `json:"duration_reached"`
		Error            string    `json:"error,omitempty"`
	}{
		Step: "live-h264", StartedAt: started, ElapsedMS: time.Since(started).Milliseconds(),
		BackgroundSHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: report.Bytes, Chunks: report.Chunks,
		FFmpeg: ffmpeg, EncoderArgs: encoderArgs, ChunkWaitLimitMS: chunkWait.Milliseconds(),
		ChunkSize: report.ChunkSize, MaxQueueDepth: report.MaxQueueDepth,
		MaxChunkWaitMS: report.MaxChunkWait.Milliseconds(), StopResponse: hex.EncodeToString(report.StopResponse),
		DurationReached: errors.Is(liveCtx.Err(), context.DeadlineExceeded),
	}
	if destination != nil {
		result.Step = "render-only"
	}
	// Retain cancellation and cleanup errors in diagnostics; reaching duration
	// alone is not evidence of a successful hardware or latency validation.
	if err != nil {
		result.Error = err.Error()
	}
	return errors.Join(err, json.NewEncoder(os.Stdout).Encode(result))
}
