// SPDX-License-Identifier: GPL-3.0-or-later

package render

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("GO_WANT_RENDER_HELPER") {
	case "1":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("fake stderr\n"), 7000))
		_, _ = os.Stdout.Write([]byte{0, 0, 0, 1})
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "close-stdout":
		_ = os.Stdout.Close()
		time.Sleep(time.Hour)
		os.Exit(0)
	case "fail":
		_, _ = os.Stderr.Write(append(bytes.Repeat([]byte("x"), stderrLimit+100), []byte("TAIL")...))
		os.Exit(7)
	}
	os.Exit(m.Run())
}

func TestStreamCancellationAndWaitOnce(t *testing.T) {
	t.Setenv("GO_WANT_RENDER_HELPER", "1")
	background := tempMP4(t)
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan uint64, 1)
	stream, err := Start(ctx, Options{
		FFmpeg:     os.Args[0],
		Background: background,
		OnOverlay: func(_ time.Time, counter uint64) {
			select {
			case updates <- counter:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 4)
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("read fake output: %v", err)
	}
	if !bytes.Equal(got, []byte{0, 0, 0, 1}) {
		t.Fatalf("output = %v", got)
	}
	if counter := <-updates; counter != 0 {
		t.Fatalf("initial overlay counter = %d, want 0", counter)
	}

	cancel()
	if err := stream.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context cancellation", err)
	}
	if err := stream.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Wait() = %v, want same cancellation", err)
	}
	if err := stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() = %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Setenv("GO_WANT_RENDER_HELPER", "1")
	stream, err := Start(context.Background(), Options{FFmpeg: os.Args[0], Background: tempMP4(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if err := stream.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want close cancellation", err)
	}
}

func TestCloseKillsChildThatClosedStdout(t *testing.T) {
	t.Setenv("GO_WANT_RENDER_HELPER", "close-stdout")
	stream, err := Start(context.Background(), Options{FFmpeg: os.Args[0], Background: tempMP4(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = %v, want EOF", err)
	}
	started := time.Now()
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close took %s", elapsed)
	}
}

func TestClosePreservesEncoderFailureAndBoundedStderr(t *testing.T) {
	t.Setenv("GO_WANT_RENDER_HELPER", "fail")
	stream, err := Start(context.Background(), Options{FFmpeg: os.Args[0], Background: tempMP4(t)})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, stream)
	err = stream.Close()
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.HasSuffix(err.Error(), "TAIL") {
		t.Fatalf("Close() = %v, want exit status and stderr tail", err)
	}
	if len(err.Error()) > stderrLimit+100 {
		t.Fatalf("error retained more than bounded stderr: %d bytes", len(err.Error()))
	}
}

func TestDiagnosticOverlay(t *testing.T) {
	a, err := diagnosticOverlay(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := diagnosticOverlay(2*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("counter update did not change PNG")
	}
	img, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != landscapeWidth || got.Y != landscapeHeight {
		t.Fatalf("overlay size = %v", got)
	}
	if alpha := img.At(0, 0); alpha != nil {
		_, _, _, a := alpha.RGBA()
		if a != 0 {
			t.Fatalf("background alpha = %d, want 0", a)
		}
	}
}

func TestStartValidatesBeforeSpawn(t *testing.T) {
	t.Setenv("GO_WANT_RENDER_HELPER", "1")
	tests := []struct {
		name       string
		background string
		frameRate  int
		want       string
	}{
		{name: "missing", background: "missing.mp4", want: "stat background"},
		{name: "extension", background: tempFile(t, "background.mov"), want: "must be an mp4"},
		{name: "frame rate", background: tempMP4(t), frameRate: -1, want: "frame rate"},
		{name: "high frame rate", background: tempMP4(t), frameRate: 121, want: "frame rate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Start(context.Background(), Options{FFmpeg: os.Args[0], Background: test.background, FrameRate: test.frameRate})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Start() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTailBufferKeepsBoundedSuffix(t *testing.T) {
	b := tailBuffer{limit: 5}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defg"))
	if got := b.String(); got != "cdefg" {
		t.Fatalf("tail = %q", got)
	}
}

func TestFFmpegArgsKeepHostPacingAndCBR(t *testing.T) {
	args := strings.Join(ffmpegArgs("background.mp4", 25), " ")
	for _, want := range []string{
		"-stream_loop -1 -i background.mp4",
		"-framerate 25",
		"overlay=0:0:shortest=1:eof_action=endall,fps=25,transpose=clock",
		"-bf 0 -g 25",
		"-b:v 1500k -minrate 1500k -maxrate 1500k -bufsize 1500k",
		"-x264-params nal-hrd=cbr:force-cfr=1",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("FFmpeg args missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "-readrate") {
		t.Fatalf("background readrate reintroduces a measured loop-boundary stall: %s", args)
	}
}

func TestFFmpegSmoke(t *testing.T) {
	ffmpeg, background := os.Getenv("TURZX_FFMPEG"), os.Getenv("TURZX_BACKGROUND")
	if ffmpeg == "" || background == "" {
		t.Skip("set TURZX_FFMPEG and TURZX_BACKGROUND to run the real encoder")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	started := time.Now()
	stream, err := Start(ctx, Options{FFmpeg: ffmpeg, Background: background})
	if err != nil {
		t.Fatal(err)
	}
	streamStarted := time.Now()
	chunk := make([]byte, 202752)
	var chunks int
	for {
		chunkStarted := time.Now()
		n, readErr := io.ReadFull(stream, chunk)
		wait := time.Since(chunkStarted)
		if n > 0 {
			t.Logf("chunk=%d bytes=%d wait=%s elapsed=%s", chunks+1, n, wait, time.Since(streamStarted))
		}
		if chunks == 0 && n >= 4 && !bytes.Equal(chunk[:4], []byte{0, 0, 0, 1}) {
			_ = stream.Close()
			t.Fatalf("H264 prefix = %x", chunk[:4])
		}
		if readErr != nil {
			if ctx.Err() == nil {
				_ = stream.Close()
				t.Fatalf("read H264 chunk: %v", readErr)
			}
			break
		}
		chunks++
	}
	t.Logf("start=%s chunks=%d total=%s", streamStarted.Sub(started), chunks, time.Since(started))
	if err := stream.Wait(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() = %v, want deadline", err)
	}
}

func tempMP4(t *testing.T) string { return tempFile(t, "background.mp4") }

func tempFile(t *testing.T, name string) string {
	t.Helper()
	path := t.TempDir() + string(os.PathSeparator) + name
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
