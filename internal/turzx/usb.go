// SPDX-License-Identifier: GPL-3.0-or-later
//
// Protocol and response handling derived from turing-smart-screen-python 3.10.0:
// https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.

package turzx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
	"time"

	"github.com/google/gousb"
)

const (
	startupDrainReads = 128
	flushReads        = 5
	defaultVideoChunk = 202752
)

// ErrChunkWait reports that a live H264 chunk was not ready to start its USB
// transfer within the configured assembly and transfer-start wait limit.
var ErrChunkWait = errors.New("H264 chunk wait limit exceeded")

// VideoOptions are deliberately limited to the values needed by the G1 probe.
type VideoOptions struct {
	FrameRate    byte
	Brightness   byte
	QueueTimeout time.Duration
}

// VideoReport records H264 transfer values needed for G1 checks.
type VideoReport struct {
	ChunkSize           int
	Chunks              int
	Bytes               int64
	MaxQueueDepth       byte
	MaxChunkWait        time.Duration
	NegotiationResponse []byte
	LastChunkResponse   []byte
	LastStatusResponse  []byte
	StopResponse        []byte
	StopError           error
	Elapsed             time.Duration
}

type usbReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type usbWriter interface {
	WriteContext(context.Context, []byte) (int, error)
}

// USB owns one panel and serializes commands, responses, flushing and Close.
// Call Sync before SendPNG. Any transaction failure requires another Sync;
// partial transfers are never retried automatically.
type USB struct {
	mu           sync.Mutex
	usbContext   *gousb.Context
	device       *gousb.Device
	config       *gousb.Config
	intf         *gousb.Interface
	in           usbReader
	out          usbWriter
	timeout      time.Duration
	flushTimeout time.Duration
	readSize     int
	synced       bool
	closed       bool
}

// OpenUSB claims the active configuration's interface 0, alternate setting 0,
// on the supported 1cbe:0092 panel and drains stale input before returning.
// It does not reset the device, detach drivers or send a display command.
// timeout bounds each I/O and each whole drain; flushTimeout bounds one drain
// read and must be smaller than timeout. USB enumeration/claiming itself is a
// synchronous libusb operation and cannot be interrupted by ctx.
func OpenUSB(ctx context.Context, timeout, flushTimeout time.Duration) (_ *USB, err error) {
	if timeout <= 0 || flushTimeout <= 0 || flushTimeout >= timeout {
		return nil, fmt.Errorf("USB: require 0 < flush timeout < I/O timeout")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u := &USB{timeout: timeout, flushTimeout: flushTimeout}
	u.usbContext, err = newUSBContext()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, u.Close())
		}
	}()
	u.device, err = u.usbContext.OpenDeviceWithVIDPID(0x1cbe, 0x0092)
	if err != nil {
		return nil, fmt.Errorf("open USB 1cbe:0092: %w", err)
	}
	if u.device == nil {
		return nil, fmt.Errorf("USB 1cbe:0092 not found")
	}
	active, err := u.device.ActiveConfigNum()
	if err != nil {
		return nil, fmt.Errorf("read USB configuration: %w", err)
	}
	if active == 0 {
		return nil, fmt.Errorf("USB has no active configuration")
	}
	u.config, err = u.device.Config(active)
	if err != nil {
		return nil, fmt.Errorf("claim USB configuration: %w", err)
	}
	u.intf, err = u.config.Interface(0, 0)
	if err != nil {
		return nil, fmt.Errorf("claim USB interface 0: %w", err)
	}
	inDesc, outDesc, err := bulkEndpoints(u.intf.Setting)
	if err != nil {
		return nil, err
	}
	u.in, err = u.intf.InEndpoint(inDesc.Number)
	if err != nil {
		return nil, fmt.Errorf("open USB IN endpoint: %w", err)
	}
	u.out, err = u.intf.OutEndpoint(outDesc.Number)
	if err != nil {
		return nil, fmt.Errorf("open USB OUT endpoint: %w", err)
	}
	// gousb recommends buffers aligned to the endpoint's maximum packet size.
	u.readSize = ((packetLen + inDesc.MaxPacketSize - 1) / inDesc.MaxPacketSize) * inDesc.MaxPacketSize
	if err := u.drain(ctx, startupDrainReads); err != nil {
		return nil, fmt.Errorf("startup USB drain: %w", err)
	}
	return u, nil
}

// gousb exposes libusb initialization failures as panics rather than errors.
func newUSBContext() (usbContext *gousb.Context, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			err = fmt.Errorf("initialize libusb: %v", failure)
		}
	}()
	return gousb.NewContext(), nil
}

func bulkEndpoints(setting gousb.InterfaceSetting) (in, out gousb.EndpointDesc, err error) {
	if setting.Class != 0xff {
		return in, out, fmt.Errorf("USB interface 0 is not vendor-specific")
	}
	for _, ep := range setting.Endpoints {
		if ep.TransferType != gousb.TransferTypeBulk {
			continue
		}
		if ep.MaxPacketSize <= 0 {
			return in, out, fmt.Errorf("USB bulk endpoint has invalid packet size")
		}
		switch ep.Direction {
		case gousb.EndpointDirectionIn:
			if in.MaxPacketSize != 0 {
				return in, out, fmt.Errorf("USB interface has multiple bulk IN endpoints")
			}
			in = ep
		case gousb.EndpointDirectionOut:
			if out.MaxPacketSize != 0 {
				return in, out, fmt.Errorf("USB interface has multiple bulk OUT endpoints")
			}
			out = ep
		}
	}
	if in.MaxPacketSize == 0 || out.MaxPacketSize == 0 {
		return in, out, fmt.Errorf("USB interface requires bulk IN and OUT endpoints")
	}
	return in, out, nil
}

// Sync drains pending input then sends command 10. Responses are returned even
// on validation failure to allow inspection of previously unknown firmware.
// The conservative success check uses the upstream C8 marker; hardware
// compatibility still requires validating the actual reply and panel output.
func (u *USB) Sync(ctx context.Context) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.synced = false
	if err := u.ready(ctx); err != nil {
		return nil, err
	}
	if err := u.drain(ctx, startupDrainReads); err != nil {
		return nil, fmt.Errorf("sync USB drain: %w", err)
	}
	packet, err := EncryptPacket(BuildHeader(CmdSync, MillisSinceMidnight(time.Now())))
	if err != nil {
		return nil, err
	}
	response, err := u.transact(ctx, CmdSync, packet)
	u.synced = err == nil
	return response, err
}

// SendPNG validates and uploads a native RGBA PNG after a successful Sync.
// A successful response does not prove that the panel displayed the image.
func (u *USB) SendPNG(ctx context.Context, payload []byte) ([]byte, error) {
	if err := ValidatePNG(payload); err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.ready(ctx); err != nil {
		return nil, err
	}
	if !u.synced {
		return nil, fmt.Errorf("USB requires successful Sync before PNG upload")
	}
	packet, err := ImageCommand(CmdUploadPNG, MillisSinceMidnight(time.Now()), payload)
	if err != nil {
		return nil, err
	}
	response, err := u.transact(ctx, CmdUploadPNG, packet)
	if err != nil {
		u.synced = false
	}
	return response, err
}

// SendH264 sends one finite Annex B stream using the command order observed in
// turing-smart-screen-python 3.10.0. It always attempts command 123 after video
// initialization starts. The file is not replayed or retried on partial I/O.
func (u *USB) SendH264(ctx context.Context, r io.Reader, size int64, opts VideoOptions) (report VideoReport, err error) {
	if r == nil || size <= 0 {
		return report, fmt.Errorf("H264 input must have a positive known size")
	}
	return u.sendH264(ctx, opts, func(ctx context.Context, report *VideoReport) error {
		remaining := size
		buf := make([]byte, report.ChunkSize)
		for remaining > 0 {
			want := min(int64(len(buf)), remaining)
			n, readErr := io.ReadFull(r, buf[:want])
			if readErr != nil {
				return fmt.Errorf("read H264 chunk (%d/%d bytes): %w", n, want, readErr)
			}
			remaining -= int64(n)
			packet, buildErr := VideoChunkCommand(MillisSinceMidnight(time.Now()), buf[:n], remaining == 0)
			if buildErr != nil {
				return buildErr
			}
			response, transferErr := u.transact(ctx, CmdVideoChunk, packet)
			report.LastChunkResponse = response
			if transferErr != nil {
				return fmt.Errorf("send H264 chunk %d: %w", report.Chunks+1, transferErr)
			}
			report.Chunks++
			report.Bytes += int64(n)
			if queueErr := u.waitVideoQueue(ctx, opts.QueueTimeout, report); queueErr != nil {
				return queueErr
			}
		}
		return nil
	})
}

func (u *USB) sendH264(ctx context.Context, opts VideoOptions, send func(context.Context, *VideoReport) error) (report VideoReport, err error) {
	if opts.FrameRate == 0 || opts.FrameRate > 120 {
		return report, fmt.Errorf("H264 frame rate must be in 1..120")
	}
	if opts.Brightness > 102 {
		return report, fmt.Errorf("H264 brightness must be in 0..102")
	}
	if opts.QueueTimeout <= 0 {
		return report, fmt.Errorf("H264 queue timeout must be positive")
	}
	clearPNG, err := EncodePNG(image.NewNRGBA(image.Rect(0, 0, nativeWidth, nativeHeight)))
	if err != nil {
		return report, err
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.ready(ctx); err != nil {
		return report, err
	}
	if !u.synced {
		return report, fmt.Errorf("USB requires successful Sync before H264 upload")
	}
	started := time.Now()
	videoStarted := false
	defer func() {
		if videoStarted {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), u.timeout)
			response, stopErr := u.command(stopCtx, CmdVideoStop, nil)
			cancel()
			report.StopResponse = response
			report.StopError = stopErr
			err = errors.Join(err, stopErr)
		}
		report.Elapsed = time.Since(started)
		if err != nil {
			u.synced = false
		}
	}()

	for i, cmd := range []byte{CmdVideoInit111, CmdVideoInit112, CmdVideoInit13, CmdBrightness, CmdVideoInit41} {
		if i == 0 {
			// The device may accept command 111 even if its response fails;
			// still trigger the bounded stop attempt.
			videoStarted = true
		}
		var set func([]byte)
		if cmd == CmdBrightness {
			set = func(header []byte) { header[8] = opts.Brightness }
		}
		if _, err = u.command(ctx, cmd, set); err != nil {
			return report, fmt.Errorf("initialize H264 command %d: %w", cmd, err)
		}
	}
	clearPacket, err := ImageCommand(CmdUploadPNG, MillisSinceMidnight(time.Now()), clearPNG)
	if err != nil {
		return report, err
	}
	if _, err = u.transact(ctx, CmdUploadPNG, clearPacket); err != nil {
		return report, fmt.Errorf("clear display before H264: %w", err)
	}
	if _, err = u.command(ctx, CmdFrameRate, func(header []byte) { header[8] = opts.FrameRate }); err != nil {
		return report, fmt.Errorf("set H264 frame rate: %w", err)
	}
	report.NegotiationResponse, err = u.command(ctx, CmdVideoChunkSize, nil)
	if err != nil {
		return report, fmt.Errorf("negotiate H264 chunk size: %w", err)
	}
	report.ChunkSize = defaultVideoChunk
	if len(report.NegotiationResponse) >= 12 {
		negotiated := binary.BigEndian.Uint32(report.NegotiationResponse[8:12])
		if negotiated > 0 && negotiated <= MaxPayload {
			report.ChunkSize = int(negotiated)
		}
	}

	err = send(ctx, &report)
	return report, err
}

func (u *USB) command(ctx context.Context, cmd byte, set func([]byte)) ([]byte, error) {
	header := BuildHeader(cmd, MillisSinceMidnight(time.Now()))
	if set != nil {
		set(header)
	}
	packet, err := EncryptPacket(header)
	if err != nil {
		return nil, err
	}
	return u.transact(ctx, cmd, packet)
}

func (u *USB) waitVideoQueue(ctx context.Context, timeout time.Duration, report *VideoReport) error {
	queueCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := queueCtx.Deadline()
	for {
		response, err := u.command(queueCtx, CmdVideoStatus, nil)
		report.LastStatusResponse = response
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("H264 queue status exceeded wait limit %s: %w", timeout, err)
			}
			return fmt.Errorf("read H264 queue status: %w", err)
		}
		if len(response) < 9 || response[1] != 0xc8 {
			return fmt.Errorf("H264 queue status has no byte-1 success marker: %x", response)
		}
		depth := response[8]
		if depth > report.MaxQueueDepth {
			report.MaxQueueDepth = depth
		}
		if depth <= 3 {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("H264 queue depth %d exceeded wait limit %s", depth, timeout)
		}
		wait := 50 * time.Millisecond
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-queueCtx.Done():
			timer.Stop()
			return queueCtx.Err()
		case <-timer.C:
		}
	}
}

func (u *USB) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if u.closed || u.in == nil || u.out == nil {
		return fmt.Errorf("USB is closed or uninitialized")
	}
	return nil
}

// transact is called with mu held. Each USB request has its own bounded context.
func (u *USB) transact(ctx context.Context, cmd byte, packet []byte) ([]byte, error) {
	writeCtx, cancel := context.WithTimeout(ctx, u.timeout)
	n, err := u.out.WriteContext(writeCtx, packet)
	err = transferError(writeCtx, err)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("USB write (%d/%d bytes): %w", n, len(packet), err)
	}
	if n != len(packet) {
		return nil, fmt.Errorf("USB write (%d/%d bytes): %w", n, len(packet), io.ErrShortWrite)
	}
	response := make([]byte, u.readSize)
	readCtx, cancel := context.WithTimeout(ctx, u.timeout)
	n, err = u.in.ReadContext(readCtx, response)
	err = transferError(readCtx, err)
	cancel()
	response = response[:n]
	if err != nil {
		err = fmt.Errorf("USB response: %w", err)
	} else if n < 9 || (response[1] != 0xc8 && response[8] != 0xc8) {
		err = fmt.Errorf("USB response missing success marker (length %d, response %x)", n, response)
	} else if response[0] != cmd {
		// Sync and PNG responses on the supported panel echo the command ID.
		err = fmt.Errorf("USB response command %d, expected %d", response[0], cmd)
	}
	// Even malformed replies can leave stale input behind. Preserve both errors.
	if flushErr := u.drain(ctx, flushReads); flushErr != nil {
		err = errors.Join(err, fmt.Errorf("USB flush: %w", flushErr))
	}
	return response, err
}

func transferError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}

// drain succeeds only after an empty read times out. Reaching either limit is
// an error, not evidence that the input queue is empty.
func (u *USB) drain(ctx context.Context, maxReads int) error {
	totalCtx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()
	buf := make([]byte, u.readSize)
	for i := 0; i < maxReads; i++ {
		if err := totalCtx.Err(); err != nil {
			return err
		}
		readCtx, stop := context.WithTimeout(totalCtx, u.flushTimeout)
		n, err := u.in.ReadContext(readCtx, buf)
		readErr := readCtx.Err()
		stop()
		if totalCtx.Err() != nil {
			return transferError(totalCtx, err)
		}
		if errors.Is(err, gousb.ErrorTimeout) || errors.Is(err, gousb.TransferTimedOut) ||
			(errors.Is(readErr, context.DeadlineExceeded) && errors.Is(err, gousb.TransferCancelled)) ||
			errors.Is(err, context.DeadlineExceeded) {
			if n == 0 {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("USB drain exhausted %d reads", maxReads)
}

// Close releases the interface, configuration, device and libusb context in
// reverse order. It waits for any bounded transaction currently in progress.
func (u *USB) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.closed = true
	u.synced = false
	if u.intf != nil {
		u.intf.Close()
	}
	var err error
	if u.config != nil {
		err = errors.Join(err, u.config.Close())
	}
	if u.device != nil {
		err = errors.Join(err, u.device.Close())
	}
	if u.usbContext != nil {
		err = errors.Join(err, u.usbContext.Close())
	}
	return err
}
