// SPDX-License-Identifier: GPL-3.0-or-later
//
// Protocol and response handling derived from turing-smart-screen-python 3.10.0:
// https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.

package turzx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var errStreamFinished = errors.New("H264 stream finished")

type liveChunk struct {
	data    []byte
	started time.Time
	timer   *time.Timer
	sending chan struct{}
	err     error
}

// SendH264Stream sends full negotiated-size chunks from a live Annex B source.
// It owns and closes r on every return, and waits for its read goroutine to
// exit. Close must therefore unblock a concurrent Read; an *os.File returned
// by os.Pipe satisfies that contract. A live EOF is an error, and partial
// chunks are never sent, padded, or marked final.
func (u *USB) SendH264Stream(ctx context.Context, r io.ReadCloser, opts VideoOptions, chunkWait time.Duration) (report VideoReport, err error) {
	if r == nil {
		return report, fmt.Errorf("live H264 input is nil")
	}
	var closeOnce sync.Once
	var closeErr error
	closeInput := func() {
		closeOnce.Do(func() { closeErr = r.Close() })
	}
	defer func() {
		closeInput()
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close live H264 input: %w", closeErr))
		}
	}()
	if chunkWait <= 0 {
		return report, fmt.Errorf("H264 chunk wait limit must be positive")
	}

	return u.sendH264(ctx, opts, func(ctx context.Context, report *VideoReport) (sendErr error) {
		streamCtx, cancel := context.WithCancelCause(ctx)
		var maxWait atomic.Int64
		recordWait := func(wait time.Duration) {
			for {
				previous := maxWait.Load()
				if int64(wait) <= previous || maxWait.CompareAndSwap(previous, int64(wait)) {
					return
				}
			}
		}
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			<-streamCtx.Done()
			closeInput()
		}()

		chunks := make(chan liveChunk)
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				chunk := liveChunk{
					data:    make([]byte, report.ChunkSize),
					started: time.Now(),
					sending: make(chan struct{}),
				}
				timeoutErr := fmt.Errorf("%w after %s", ErrChunkWait, chunkWait)
				chunk.timer = time.AfterFunc(chunkWait, func() {
					recordWait(time.Since(chunk.started))
					cancel(timeoutErr)
				})
				n, readErr := io.ReadFull(r, chunk.data)
				if readErr != nil {
					chunk.timer.Stop()
					select {
					case chunks <- liveChunk{data: chunk.data[:n], started: chunk.started, timer: chunk.timer, err: readErr}:
					case <-streamCtx.Done():
					}
					return
				}
				select {
				case chunks <- chunk:
				case <-streamCtx.Done():
					chunk.timer.Stop()
					return
				}
				select {
				case <-chunk.sending:
				case <-streamCtx.Done():
					chunk.timer.Stop()
					return
				}
			}
		}()

		defer func() {
			report.MaxChunkWait = time.Duration(maxWait.Load())
			cancel(errStreamFinished)
			<-closeDone
			<-readDone
		}()

		for {
			var chunk liveChunk
			select {
			case <-streamCtx.Done():
				return context.Cause(streamCtx)
			case chunk = <-chunks:
			}
			if cause := context.Cause(streamCtx); cause != nil {
				chunk.timer.Stop()
				return cause
			}
			if chunk.err != nil {
				recordWait(time.Since(chunk.started))
				return fmt.Errorf("live H264 source ended unexpectedly after %d/%d chunk bytes: %w", len(chunk.data), report.ChunkSize, chunk.err)
			}
			packet, buildErr := VideoChunkCommand(MillisSinceMidnight(time.Now()), chunk.data, false)
			if buildErr != nil {
				chunk.timer.Stop()
				return buildErr
			}
			wait := time.Since(chunk.started)
			recordWait(wait)
			if wait >= chunkWait || !chunk.timer.Stop() {
				timeoutErr := fmt.Errorf("%w after %s", ErrChunkWait, chunkWait)
				cancel(timeoutErr)
				return timeoutErr
			}
			close(chunk.sending)
			response, transferErr := u.transact(streamCtx, CmdVideoChunk, packet)
			report.LastChunkResponse = response
			if transferErr != nil {
				return errors.Join(fmt.Errorf("send live H264 chunk %d: %w", report.Chunks+1, transferErr), context.Cause(streamCtx))
			}
			report.Chunks++
			report.Bytes += int64(len(chunk.data))
			if queueErr := u.waitVideoQueue(streamCtx, opts.QueueTimeout, report); queueErr != nil {
				return errors.Join(queueErr, context.Cause(streamCtx))
			}
		}
	})
}
