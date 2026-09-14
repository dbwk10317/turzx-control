// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
)

// trayIcon encodes a small screen glyph as a PNG-backed Windows ICO.
func trayIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 3; y < 13; y++ {
		for x := 1; x < 15; x++ {
			c := color.NRGBA{R: 92, G: 55, B: 160, A: 255}
			if x > 2 && x < 13 && y > 4 && y < 11 {
				c = color.NRGBA{R: 70, G: 220, B: 200, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var data bytes.Buffer
	_ = png.Encode(&data, img)
	header := []byte{0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 32, 0, 0, 0, 0, 0, 22, 0, 0, 0}
	binary.LittleEndian.PutUint32(header[14:18], uint32(data.Len()))
	return append(header, data.Bytes()...)
}

var shell32 = windows.NewLazySystemDLL("shell32.dll")
var shellExecute = shell32.NewProc("ShellExecuteW")

func runControlSurface(ctx context.Context, settingsURL string, serve func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return runTrayLifecycle(ctx, serve, func(ready, exited func()) {
		systray.Run(func() {
			systray.SetIcon(trayIcon())
			systray.SetTooltip("TURZX Control")
			open := systray.AddMenuItem("설정 열기", "TURZX 설정 페이지 열기")
			systray.AddSeparator()
			exit := systray.AddMenuItem("종료", "TURZX Control 종료")
			ready()
			for {
				select {
				case <-open.ClickedCh:
					if err := openSettings(settingsURL); err != nil {
						log.Printf("open settings: %v", err)
					}
				case <-exit.ClickedCh:
					systray.Quit()
					return
				case <-ctx.Done():
					return
				}
			}
		}, func() { cancel(); exited() })
	}, systray.Quit)
}

func runTrayLifecycle(ctx context.Context, serve func(context.Context) error, run func(func(), func()), quit func()) error {
	if serve == nil {
		return errors.New("control surface server is nil")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	result := make(chan error, 1)
	ready := false
	run(func() {
		ready = true
		go func() {
			<-serveCtx.Done()
			quit()
		}()
		go func() {
			err := serve(serveCtx)
			result <- err
			cancel()
		}()
	}, func() {
		cancel()
	})
	cancel()
	if !ready {
		return errors.New("tray did not start; control surface never served")
	}

	select {
	case err := <-result:
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("control surface did not stop after tray exit")
	}
}

func openSettings(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	url, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	ret, _, callErr := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(url)), 0, 0, windows.SW_SHOWNORMAL)
	if ret <= 32 {
		if callErr != windows.ERROR_SUCCESS {
			return callErr
		}
		return fmt.Errorf("ShellExecuteW failed with code %d", ret)
	}
	return nil
}
