// SPDX-License-Identifier: GPL-3.0-or-later
// TURZX protocol reference: https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.

// turzx-probe performs bounded sync and optional static PNG or finite H264 checks.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/dbwk10317/turzx-control/internal/turzx"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("turzx-probe", flag.ContinueOnError)
	pngPath := flags.String("png", "", "optional native 462x1920 RGBA PNG to display")
	h264Path := flags.String("h264", "", "optional finite H264 Annex B stream to display")
	background := flags.String("background", "", "local MP4 background for the live diagnostic overlay")
	ffmpeg := flags.String("ffmpeg", "ffmpeg", "FFmpeg executable for the live diagnostic")
	duration := flags.Duration("duration", 30*time.Second, "live diagnostic duration")
	chunkWait := flags.Duration("chunk-wait", 1500*time.Millisecond, "live chunk assembly and queue wait limit")
	renderOnly := flags.String("render-only", "", "save live encoder output to a new H264 file without opening USB")
	pattern := flags.Bool("test-pattern", false, "display static diagnostic color bands (not a theme)")
	timeout := flags.Duration("timeout", 2*time.Second, "per-transfer I/O timeout")
	flush := flags.Duration("flush-timeout", 20*time.Millisecond, "idle IN drain timeout; requires device tuning")
	frameRate := flags.Int("frame-rate", 25, "H264 frame rate sent to the panel")
	brightness := flags.Int("brightness", 32, "H264 initialization brightness (0..102)")
	queueTimeout := flags.Duration("queue-timeout", 1500*time.Millisecond, "maximum H264 queue drain wait")
	if err := flags.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, selected := range []bool{*pngPath != "", *h264Path != "", *pattern, *background != ""} {
		if selected {
			modes++
		}
	}
	if flags.NArg() != 0 || modes > 1 {
		return fmt.Errorf("use at most one of -png, -h264, -background, or -test-pattern, without positional arguments")
	}
	if *timeout <= 0 || *flush <= 0 || *flush >= *timeout {
		return fmt.Errorf("require 0 < flush-timeout < timeout")
	}
	if *frameRate < 1 || *frameRate > 120 || *brightness < 0 || *brightness > 102 || *queueTimeout <= 0 {
		return fmt.Errorf("require frame-rate 1..120, brightness 0..102, and positive queue-timeout")
	}
	if *duration <= 0 || *chunkWait <= 0 || (*renderOnly != "" && *background == "") {
		return fmt.Errorf("require positive duration/chunk-wait and -background for -render-only")
	}
	if *background != "" {
		return runLive(*background, *ffmpeg, *renderOnly, *duration, *timeout, *flush, *chunkWait,
			turzx.VideoOptions{FrameRate: byte(*frameRate), Brightness: byte(*brightness), QueueTimeout: *queueTimeout})
	}
	var payload []byte
	var h264 *os.File
	var h264Size int64
	if *pngPath != "" {
		file, err := os.Open(*pngPath)
		if err != nil {
			return err
		}
		payload, err = io.ReadAll(io.LimitReader(file, turzx.MaxPayload+1))
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := turzx.ValidatePNG(payload); err != nil {
			return err
		}
	} else if *h264Path != "" {
		var err error
		h264, err = os.Open(*h264Path)
		if err != nil {
			return err
		}
		info, statErr := h264.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			_ = h264.Close()
			if statErr != nil {
				return statErr
			}
			return fmt.Errorf("H264 input must be a non-empty regular file")
		}
		h264Size = info.Size()
	} else if *pattern {
		// Four horizontal bands in native portrait coordinates; no theme layout.
		img := image.NewNRGBA(image.Rect(0, 0, 462, 1920))
		colors := []color.NRGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}, {R: 255, G: 255, B: 255, A: 255}}
		for i, c := range colors {
			draw.Draw(img, image.Rect(0, i*480, 462, (i+1)*480), image.NewUniform(c), image.Point{}, draw.Src)
		}
		var err error
		payload, err = turzx.EncodePNG(img)
		if err != nil {
			return err
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	device, err := turzx.OpenUSB(ctx, *timeout, *flush)
	if err != nil {
		if h264 != nil {
			_ = h264.Close()
		}
		return err
	}
	probeErr := func() error {
		for _, step := range []string{"sync", "png"} {
			if step == "png" && payload == nil {
				break
			}
			start := time.Now()
			var response []byte
			var err error
			if step == "sync" {
				response, err = device.Sync(ctx)
			} else {
				response, err = device.SendPNG(ctx, payload)
			}
			result := struct {
				Step      string `json:"step"`
				ElapsedMS int64  `json:"elapsed_ms"`
				Response  string `json:"response_hex"`
				Error     string `json:"error,omitempty"`
			}{Step: step, ElapsedMS: time.Since(start).Milliseconds(), Response: hex.EncodeToString(response)}
			if err != nil {
				result.Error = err.Error()
			}
			if outputErr := json.NewEncoder(os.Stdout).Encode(result); outputErr != nil {
				return outputErr
			}
			if err != nil {
				return err
			}
		}
		if h264 != nil {
			report, err := device.SendH264(ctx, h264, h264Size, turzx.VideoOptions{
				FrameRate: byte(*frameRate), Brightness: byte(*brightness), QueueTimeout: *queueTimeout,
			})
			result := struct {
				Step                string `json:"step"`
				ElapsedMS           int64  `json:"elapsed_ms"`
				Bytes               int64  `json:"bytes"`
				Chunks              int    `json:"chunks"`
				ChunkSize           int    `json:"chunk_size"`
				MaxQueueDepth       byte   `json:"max_queue_depth"`
				NegotiationResponse string `json:"negotiation_response_hex"`
				LastChunkResponse   string `json:"last_chunk_response_hex"`
				LastStatusResponse  string `json:"last_status_response_hex"`
				StopResponse        string `json:"stop_response_hex"`
				Error               string `json:"error,omitempty"`
			}{
				Step: "h264", ElapsedMS: report.Elapsed.Milliseconds(), Bytes: report.Bytes,
				Chunks: report.Chunks, ChunkSize: report.ChunkSize, MaxQueueDepth: report.MaxQueueDepth,
				NegotiationResponse: hex.EncodeToString(report.NegotiationResponse),
				LastChunkResponse:   hex.EncodeToString(report.LastChunkResponse),
				LastStatusResponse:  hex.EncodeToString(report.LastStatusResponse),
				StopResponse:        hex.EncodeToString(report.StopResponse),
			}
			if err != nil {
				result.Error = err.Error()
			}
			if outputErr := json.NewEncoder(os.Stdout).Encode(result); outputErr != nil {
				return outputErr
			}
			return err
		}
		return nil
	}()
	var fileErr error
	if h264 != nil {
		fileErr = h264.Close()
	}
	return errors.Join(probeErr, fileErr, device.Close())
}
