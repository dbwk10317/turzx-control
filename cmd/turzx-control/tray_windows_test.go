// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/png"
	"sync"
	"testing"
	"time"
)

func TestTrayLifecycle(t *testing.T) {
	want := errors.New("server failed")
	for _, earlyCancel := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if earlyCancel {
			cancel()
		}
		quitCh := make(chan struct{})
		var once sync.Once
		quit := func() { once.Do(func() { close(quitCh) }) }
		err := runTrayLifecycle(ctx, func(ctx context.Context) error {
			if earlyCancel {
				<-ctx.Done()
				return ctx.Err()
			}
			return want
		}, func(ready, exit func()) {
			ready()
			select {
			case <-quitCh:
			case <-time.After(time.Second):
				t.Error("tray did not quit")
			}
			exit()
		}, quit)
		cancel()
		if earlyCancel && err != nil {
			t.Fatal(err)
		}
		if !earlyCancel && !errors.Is(err, want) {
			t.Fatalf("lost server error: %v", err)
		}
	}
}

func TestTrayIcon(t *testing.T) {
	data := trayIcon()
	if int(binary.LittleEndian.Uint32(data[14:18])) != len(data)-22 {
		t.Fatal("invalid ICO size")
	}
	img, err := png.Decode(bytes.NewReader(data[22:]))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 16 {
		t.Fatal("invalid icon dimensions")
	}
}

func TestTrayPreservesServerContextErrors(t *testing.T) {
	for _, want := range []error{context.DeadlineExceeded, context.Canceled} {
		quitCh := make(chan struct{})
		var once sync.Once
		err := runTrayLifecycle(context.Background(), func(context.Context) error { return want },
			func(ready, exit func()) { ready(); <-quitCh; exit() },
			func() { once.Do(func() { close(quitCh) }) })
		if !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
}
