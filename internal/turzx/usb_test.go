// SPDX-License-Identifier: GPL-3.0-or-later
//
// Protocol reference: https://github.com/mathoudebine/turing-smart-screen-python
// Copyright (C) 2021 Matthieu Houdebine (mathoudebine), GPL-3.0-or-later.

package turzx

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/des"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"
	"time"

	"github.com/google/gousb"
)

type fakeUSBRead func(context.Context, []byte) (int, error)

func (f fakeUSBRead) ReadContext(ctx context.Context, b []byte) (int, error) {
	return f(ctx, b)
}

type fakeUSBWrite func(context.Context, []byte) (int, error)

func (f fakeUSBWrite) WriteContext(ctx context.Context, b []byte) (int, error) {
	return f(ctx, b)
}

func testUSB(read fakeUSBRead) *USB {
	return &USB{
		in: read, out: fakeUSBWrite(func(_ context.Context, b []byte) (int, error) { return len(b), nil }),
		timeout: time.Second, flushTimeout: time.Millisecond, readSize: packetLen,
	}
}

func TestUSBDrain(t *testing.T) {
	for name, tc := range map[string]struct {
		n    int
		err  error
		want error
		ok   bool
	}{
		"empty timeout":              {err: gousb.ErrorTimeout, ok: true},
		"transfer timeout":           {err: gousb.TransferTimedOut, ok: true},
		"data exhaustion":            {n: 1},
		"partial timeout exhaustion": {n: 1, err: gousb.ErrorTimeout},
		"zero read exhaustion":       {},
		"disconnect":                 {err: gousb.TransferNoDevice, want: gousb.TransferNoDevice},
		"permission":                 {err: gousb.ErrorAccess, want: gousb.ErrorAccess},
		"unexpected cancellation":    {err: gousb.TransferCancelled, want: gousb.TransferCancelled},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			u := testUSB(func(context.Context, []byte) (int, error) {
				calls++
				return tc.n, tc.err
			})
			err := u.drain(context.Background(), 3)
			if (err == nil) != tc.ok || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("drain error = %v", err)
			}
			if calls > 3 {
				t.Fatalf("drain exceeded limit: %d", calls)
			}
		})
	}
	t.Run("per-read deadline is empty", func(t *testing.T) {
		u := testUSB(func(ctx context.Context, _ []byte) (int, error) {
			<-ctx.Done()
			return 0, gousb.TransferCancelled
		})
		if err := u.drain(context.Background(), 3); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("total deadline is failure", func(t *testing.T) {
		u := testUSB(func(ctx context.Context, _ []byte) (int, error) {
			<-ctx.Done()
			return 0, gousb.TransferCancelled
		})
		u.timeout = time.Millisecond
		u.flushTimeout = time.Second
		if err := u.drain(context.Background(), 3); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("caller cancellation is failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		u := testUSB(func(context.Context, []byte) (int, error) {
			cancel()
			return 0, gousb.TransferCancelled
		})
		defer cancel()
		if err := u.drain(ctx, 3); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestUSBTransaction(t *testing.T) {
	ok1 := []byte{CmdSync, 0xc8, 0, 0, 0, 0, 0, 0, 0}
	ok8 := []byte{CmdSync, 0, 0, 0, 0, 0, 0, 0, 0xc8}
	for name, tc := range map[string]struct {
		response []byte
		readErr  error
		flushErr error
		ok       bool
	}{
		"byte1":         {response: ok1, ok: true},
		"byte8":         {response: ok8, ok: true},
		"short":         {response: ok1[:2]},
		"unknown":       {response: make([]byte, 9)},
		"wrong command": {response: []byte{CmdUploadPNG, 0xc8, 0, 0, 0, 0, 0, 0, 0}},
		"read failure":  {readErr: gousb.TransferNoDevice},
		"partial read":  {response: ok1, readErr: gousb.TransferNoDevice},
		"flush failure": {response: ok1, flushErr: gousb.TransferNoDevice},
	} {
		t.Run(name, func(t *testing.T) {
			reads := 0
			u := testUSB(func(_ context.Context, b []byte) (int, error) {
				reads++
				if reads == 1 {
					return copy(b, tc.response), tc.readErr
				}
				if tc.flushErr != nil {
					return 0, tc.flushErr
				}
				return 0, gousb.ErrorTimeout
			})
			response, err := u.transact(context.Background(), CmdSync, make([]byte, packetLen))
			if (err == nil) != tc.ok || !bytes.Equal(response, tc.response) || reads != 2 {
				t.Fatalf("response %x, err %v, reads %d", response, err, reads)
			}
		})
	}
	for name, writeErr := range map[string]error{"short": nil, "failure": gousb.TransferNoDevice} {
		t.Run("write "+name, func(t *testing.T) {
			writes := 0
			u := testUSB(func(context.Context, []byte) (int, error) {
				t.Fatal("read after failed write")
				return 0, nil
			})
			u.out = fakeUSBWrite(func(context.Context, []byte) (int, error) {
				writes++
				return 1, writeErr
			})
			_, err := u.transact(context.Background(), CmdSync, make([]byte, packetLen))
			want := writeErr
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || writes != 1 {
				t.Fatalf("error %v, writes %d", err, writes)
			}
		})
	}
}

func TestUSBSyncAndPNG(t *testing.T) {
	reads, writes := 0, 0
	u := testUSB(func(_ context.Context, b []byte) (int, error) {
		reads++
		if reads == 2 || reads == 4 {
			b[0] = CmdSync
			if reads == 4 {
				b[0] = CmdUploadPNG
			}
			b[8] = 0xc8
			return 9, nil
		}
		return 0, gousb.ErrorTimeout
	})
	payload, err := EncodePNG(image.NewNRGBA(image.Rect(0, 0, nativeWidth, nativeHeight)))
	if err != nil {
		t.Fatal(err)
	}
	u.out = fakeUSBWrite(func(_ context.Context, b []byte) (int, error) {
		writes++
		if writes == 2 && !bytes.Equal(b[packetLen:], payload) {
			t.Fatal("PNG payload changed")
		}
		return len(b), nil
	})
	if _, err := u.SendPNG(context.Background(), payload); err == nil || writes != 0 {
		t.Fatal("PNG sent before sync")
	}
	if _, err := u.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.SendPNG(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if _, err := u.SendPNG(context.Background(), []byte("invalid")); err == nil || writes != 2 {
		t.Fatal("invalid PNG sent")
	}
	u.out = fakeUSBWrite(func(context.Context, []byte) (int, error) { return 0, gousb.TransferNoDevice })
	if _, err := u.SendPNG(context.Background(), payload); err == nil || u.synced {
		t.Fatal("failed upload retained synchronized state")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Sync(context.Background()); err == nil {
		t.Fatal("sync after close succeeded")
	}
}

func TestUSBSendH264(t *testing.T) {
	var writes [][]byte
	statusDepths := []byte{4, 2, 1}
	statusRead := 0
	reads := 0
	u := testUSB(func(_ context.Context, b []byte) (int, error) {
		reads++
		if reads%2 == 0 {
			return 0, gousb.ErrorTimeout
		}
		if len(writes) == 0 {
			t.Fatal("read before write")
		}
		cmd := decryptHeader(t, writes[len(writes)-1])[0]
		response := make([]byte, 12)
		response[0], response[1] = cmd, 0xc8
		switch cmd {
		case CmdVideoChunkSize:
			binary.BigEndian.PutUint32(response[8:12], 4)
		case CmdVideoStatus:
			response[8] = statusDepths[statusRead]
			statusRead++
		}
		return copy(b, response), nil
	})
	u.out = fakeUSBWrite(func(_ context.Context, b []byte) (int, error) {
		writes = append(writes, bytes.Clone(b))
		return len(b), nil
	})
	u.synced = true
	report, err := u.SendH264(context.Background(), bytes.NewReader([]byte("abcde")), 5, VideoOptions{
		FrameRate: 25, Brightness: 32, QueueTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ChunkSize != 4 || report.Chunks != 2 || report.Bytes != 5 || report.MaxQueueDepth != 4 {
		t.Fatalf("report = %+v", report)
	}
	wantCommands := []byte{111, 112, 13, 14, 41, 102, 15, 17, 121, 122, 122, 121, 122, 123}
	if len(writes) != len(wantCommands) {
		t.Fatalf("commands = %d, want %d", len(writes), len(wantCommands))
	}
	for i, want := range wantCommands {
		if got := decryptHeader(t, writes[i])[0]; got != want {
			t.Fatalf("command %d = %d, want %d", i, got, want)
		}
	}
	if header := decryptHeader(t, writes[3]); header[8] != 32 {
		t.Fatalf("brightness = %d", header[8])
	}
	if header := decryptHeader(t, writes[6]); header[8] != 25 {
		t.Fatalf("frame rate = %d", header[8])
	}
	if err := ValidatePNG(writes[5][packetLen:]); err != nil {
		t.Fatalf("clear PNG: %v", err)
	}
	for _, check := range []struct {
		index int
		data  string
		last  byte
	}{{8, "abcd", 0}, {11, "e", 1}} {
		header := decryptHeader(t, writes[check.index])
		if size := binary.BigEndian.Uint32(header[8:12]); size != uint32(len(check.data)) || header[12] != check.last || string(writes[check.index][packetLen:]) != check.data {
			t.Fatalf("chunk %d has size %d, last %d, data %q", check.index, size, header[12], writes[check.index][packetLen:])
		}
	}
}

func TestUSBSendH264StopsAfterInitResponseFailure(t *testing.T) {
	var writes [][]byte
	firstInitRead := true
	stopRead := false
	u := testUSB(func(_ context.Context, b []byte) (int, error) {
		if len(writes) == 0 {
			t.Fatal("read before write")
		}
		cmd := decryptHeader(t, writes[len(writes)-1])[0]
		if cmd == CmdVideoInit111 && firstInitRead {
			firstInitRead = false
			return 0, gousb.TransferNoDevice
		}
		if cmd == CmdVideoInit111 {
			return 0, gousb.ErrorTimeout
		}
		if cmd == CmdVideoStop && stopRead {
			return 0, gousb.ErrorTimeout
		}
		if cmd == CmdVideoStop {
			stopRead = true
		}
		response := []byte{cmd, 0xc8, 0, 0, 0, 0, 0, 0, 0}
		return copy(b, response), nil
	})
	u.out = fakeUSBWrite(func(_ context.Context, b []byte) (int, error) {
		writes = append(writes, bytes.Clone(b))
		return len(b), nil
	})
	u.synced = true
	report, err := u.SendH264(context.Background(), bytes.NewReader([]byte("x")), 1, VideoOptions{
		FrameRate: 25, Brightness: 32, QueueTimeout: time.Second,
	})
	if err == nil || !errors.Is(err, gousb.TransferNoDevice) {
		t.Fatal("expected initialization response failure")
	}
	if len(writes) != 2 || decryptHeader(t, writes[1])[0] != CmdVideoStop {
		t.Fatalf("commands = %d, want failed init followed by stop", len(writes))
	}
	if len(report.StopResponse) == 0 || u.synced {
		t.Fatalf("stop response/sync state = %x/%t", report.StopResponse, u.synced)
	}
}

func decryptHeader(t *testing.T, packet []byte) []byte {
	t.Helper()
	if len(packet) < packetLen {
		t.Fatalf("short packet: %d", len(packet))
	}
	block, err := des.NewCipher(desKey)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 504)
	cipher.NewCBCDecrypter(block, desKey).CryptBlocks(plain, packet[:504])
	return plain
}

func TestUSBTransactionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := testUSB(func(context.Context, []byte) (int, error) { return 0, gousb.ErrorTimeout })
	u.out = fakeUSBWrite(func(ctx context.Context, _ []byte) (int, error) {
		cancel()
		<-ctx.Done()
		return 0, gousb.TransferCancelled
	})
	if _, err := u.transact(ctx, CmdSync, make([]byte, packetLen)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestUSBBulkEndpoints(t *testing.T) {
	setting := gousb.InterfaceSetting{Class: 0xff, Endpoints: map[gousb.EndpointAddress]gousb.EndpointDesc{
		0x81: {Number: 1, Direction: gousb.EndpointDirectionIn, TransferType: gousb.TransferTypeBulk, MaxPacketSize: 512},
		0x02: {Number: 2, Direction: gousb.EndpointDirectionOut, TransferType: gousb.TransferTypeBulk, MaxPacketSize: 512},
	}}
	in, out, err := bulkEndpoints(setting)
	if err != nil || in.Number != 1 || out.Number != 2 {
		t.Fatalf("in %v, out %v, err %v", in, out, err)
	}
	setting.Endpoints[0x83] = gousb.EndpointDesc{Number: 3, Direction: gousb.EndpointDirectionIn, TransferType: gousb.TransferTypeBulk, MaxPacketSize: 512}
	if _, _, err := bulkEndpoints(setting); err == nil {
		t.Fatal("accepted ambiguous bulk endpoints")
	}
	delete(setting.Endpoints, 0x83)
	delete(setting.Endpoints, 0x02)
	if _, _, err := bulkEndpoints(setting); err == nil {
		t.Fatal("accepted missing bulk OUT")
	}
}
