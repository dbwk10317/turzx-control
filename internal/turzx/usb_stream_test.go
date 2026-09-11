// SPDX-License-Identifier: GPL-3.0-or-later
//
// Protocol reference: https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.

package turzx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/gousb"
)

type streamUSBFixture struct {
	t             *testing.T
	u             *USB
	writes        [][]byte
	responseCmd   byte
	responseReady bool
	onWrite       func(context.Context, byte, []byte) (int, error)
}

func newStreamUSBFixture(t *testing.T) *streamUSBFixture {
	t.Helper()
	f := &streamUSBFixture{t: t}
	f.u = testUSB(func(_ context.Context, b []byte) (int, error) {
		if !f.responseReady {
			return 0, gousb.ErrorTimeout
		}
		f.responseReady = false
		response := make([]byte, 12)
		response[0], response[1] = f.responseCmd, 0xc8
		if f.responseCmd == CmdVideoChunkSize {
			binary.BigEndian.PutUint32(response[8:12], 4)
		}
		return copy(b, response), nil
	})
	f.u.out = fakeUSBWrite(func(ctx context.Context, b []byte) (int, error) {
		cmd := decryptHeader(t, b)[0]
		f.writes = append(f.writes, bytes.Clone(b))
		n, err := len(b), error(nil)
		if f.onWrite != nil {
			n, err = f.onWrite(ctx, cmd, b)
		}
		if err == nil && n == len(b) {
			f.responseCmd, f.responseReady = cmd, true
		}
		return n, err
	})
	f.u.synced = true
	return f
}

func (f *streamUSBFixture) commands() []byte {
	commands := make([]byte, len(f.writes))
	for i, packet := range f.writes {
		commands[i] = decryptHeader(f.t, packet)[0]
	}
	return commands
}

func streamOptions() VideoOptions {
	return VideoOptions{FrameRate: 25, Brightness: 32, QueueTimeout: time.Second}
}

type streamResult struct {
	report VideoReport
	err    error
}

func awaitStream(t *testing.T, result <-chan streamResult) streamResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("live H264 stream did not return")
		return streamResult{}
	}
}

func TestSendH264StreamFullChunks(t *testing.T) {
	f := newStreamUSBFixture(t)
	r, w := io.Pipe()
	result := make(chan streamResult, 1)
	go func() {
		report, err := f.u.SendH264Stream(context.Background(), r, streamOptions(), time.Second)
		result <- streamResult{report, err}
	}()
	go func() {
		_, _ = w.Write([]byte("abcdefgh"))
		_ = w.Close()
	}()
	got := awaitStream(t, result)
	if !errors.Is(got.err, io.EOF) {
		t.Fatalf("error = %v, want live EOF", got.err)
	}
	if got.report.Chunks != 2 || got.report.Bytes != 8 || got.report.StopError != nil {
		t.Fatalf("report = %+v", got.report)
	}
	var chunks [][]byte
	for _, packet := range f.writes {
		header := decryptHeader(t, packet)
		if header[0] == CmdVideoChunk {
			if header[12] != 0 || binary.BigEndian.Uint32(header[8:12]) != 4 {
				t.Fatalf("live chunk size/final = %d/%d", binary.BigEndian.Uint32(header[8:12]), header[12])
			}
			chunks = append(chunks, packet[packetLen:])
		}
	}
	if len(chunks) != 2 || string(chunks[0]) != "abcd" || string(chunks[1]) != "efgh" {
		t.Fatalf("chunks = %q", chunks)
	}
	commands := f.commands()
	if commands[len(commands)-1] != CmdVideoStop {
		t.Fatalf("last command = %d, want stop", commands[len(commands)-1])
	}
}

func TestSendH264StreamAssemblyStalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "first byte", data: ""},
		{name: "partial chunk", data: "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStreamUSBFixture(t)
			r, w := io.Pipe()
			defer w.Close()
			if tc.data != "" {
				go func() { _, _ = w.Write([]byte(tc.data)) }()
			}
			const wait = 40 * time.Millisecond
			result := make(chan streamResult, 1)
			go func() {
				report, err := f.u.SendH264Stream(context.Background(), r, streamOptions(), wait)
				result <- streamResult{report, err}
			}()
			got := awaitStream(t, result)
			if !errors.Is(got.err, ErrChunkWait) || got.report.MaxChunkWait < wait || got.report.Chunks != 0 {
				t.Fatalf("result = %+v, error = %v", got.report, got.err)
			}
			for _, cmd := range f.commands() {
				if cmd == CmdVideoChunk {
					t.Fatal("stalled partial chunk was sent")
				}
			}
			if commands := f.commands(); commands[len(commands)-1] != CmdVideoStop {
				t.Fatal("stop was not attempted")
			}
		})
	}
}

type blockingReadCloser struct {
	started chan struct{}
	closed  chan struct{}
	done    chan struct{}
	once    sync.Once
	closes  int
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{started: make(chan struct{}), closed: make(chan struct{}), done: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	close(r.started)
	<-r.closed
	close(r.done)
	return 0, errors.New("read unblocked by close")
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() {
		r.closes++
		close(r.closed)
	})
	return nil
}

func TestSendH264StreamCancellationClosesAndJoinsReader(t *testing.T) {
	f := newStreamUSBFixture(t)
	r := newBlockingReadCloser()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan streamResult, 1)
	go func() {
		report, err := f.u.SendH264Stream(ctx, r, streamOptions(), time.Second)
		result <- streamResult{report, err}
	}()
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}
	cancel()
	got := awaitStream(t, result)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v", got.err)
	}
	select {
	case <-r.done:
	default:
		t.Fatal("SendH264Stream returned before read goroutine exited")
	}
	if r.closes != 1 {
		t.Fatalf("Close calls = %d", r.closes)
	}
}

func TestSendH264StreamPendingChunkExpiresDuringUSB(t *testing.T) {
	f := newStreamUSBFixture(t)
	firstChunk := true
	f.onWrite = func(ctx context.Context, cmd byte, b []byte) (int, error) {
		if cmd == CmdVideoChunk && firstChunk {
			firstChunk = false
			<-ctx.Done()
			return 0, gousb.TransferCancelled
		}
		return len(b), nil
	}
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte("abcdefgh"))
		_ = w.Close()
	}()
	const wait = 40 * time.Millisecond
	report, err := f.u.SendH264Stream(context.Background(), r, streamOptions(), wait)
	if !errors.Is(err, ErrChunkWait) || report.MaxChunkWait < wait {
		t.Fatalf("report = %+v, error = %v", report, err)
	}
	chunkWrites := 0
	for _, cmd := range f.commands() {
		if cmd == CmdVideoChunk {
			chunkWrites++
		}
	}
	if chunkWrites != 1 || report.Chunks != 0 {
		t.Fatalf("chunk writes/completions = %d/%d", chunkWrites, report.Chunks)
	}
	if commands := f.commands(); commands[len(commands)-1] != CmdVideoStop {
		t.Fatal("stop was not attempted after USB cancellation")
	}
}

type errorReadCloser struct {
	err    error
	closed bool
}

func (r *errorReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (r *errorReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestSendH264StreamPreservesInputAndStopErrors(t *testing.T) {
	readErr := errors.New("encoder failed")
	stopErr := errors.New("stop failed")
	f := newStreamUSBFixture(t)
	f.onWrite = func(_ context.Context, cmd byte, b []byte) (int, error) {
		if cmd == CmdVideoStop {
			return 0, stopErr
		}
		return len(b), nil
	}
	r := &errorReadCloser{err: readErr}
	report, err := f.u.SendH264Stream(context.Background(), r, streamOptions(), time.Second)
	if !errors.Is(err, readErr) || !errors.Is(err, stopErr) {
		t.Fatalf("error = %v", err)
	}
	if !errors.Is(report.StopError, stopErr) || !r.closed {
		t.Fatalf("report/reader = %+v/%+v", report, r)
	}
	if commands := f.commands(); commands[len(commands)-1] != CmdVideoStop {
		t.Fatal("stop was not attempted")
	}
}
