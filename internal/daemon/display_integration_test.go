// SPDX-License-Identifier: GPL-3.0-or-later

package daemon

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dbwk10317/turzx-control/internal/metric"
	"github.com/dbwk10317/turzx-control/internal/turzx"
)

// Opt-in only. Close any other panel owner before running this 20-second check.
// A successful transfer is not evidence of panel readability or display latency.
func TestDisplayHardwareIntegration(t *testing.T) {
	background := os.Getenv("TURZX_INTEGRATION_BACKGROUND")
	if background == "" {
		t.Skip("set TURZX_INTEGRATION_BACKGROUND for an exclusive 20-second USB check")
	}
	ffmpeg := os.Getenv("TURZX_INTEGRATION_FFMPEG")
	if ffmpeg == "" {
		t.Fatal("TURZX_INTEGRATION_FFMPEG is required")
	}
	want := os.Getenv("TURZX_INTEGRATION_MODE")
	if want != "video" && want != "fallback" {
		t.Fatal("TURZX_INTEGRATION_MODE must be video or fallback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opts := SourceOptions{SensorHelper: os.Getenv("TURZX_INTEGRATION_SENSOR_HELPER")}
	if opts.SensorHelper != "" {
		opts.Selection = metric.HardwareSensorSelection{GPUUsageSensor: "/gpu-nvidia/0/load/0", GPUTemperatureSensor: "/gpu-nvidia/0/temperature/0", RAMTemperatureUnsupported: true}
	}
	sources := NewSources(ctx, opts, nil)
	defer sources.Close()
	seen := false
	deps := productionDisplayDeps
	var reports []*hardwareDisplay
	deps.open = func(ctx context.Context, timeout, flush time.Duration) (displayUSB, error) {
		device, err := productionDisplayDeps.open(ctx, timeout, flush)
		if err != nil {
			return device, err
		}
		report := &hardwareDisplay{displayUSB: device, t: t}
		reports = append(reports, report)
		return report, nil
	}
	err := runDisplay(ctx, DisplayOptions{Background: background, FFmpeg: ffmpeg, Theme: "smon-halloween", Brightness: 32, Timeout: 2 * time.Second, FlushTimeout: 20 * time.Millisecond, ChunkWait: 3 * time.Second}, sources.Dashboard, func(s DisplayState) {
		t.Logf("%s: %s", s.Status, s.Message)
		if s.Status == want {
			seen = true
		}
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatalf("did not reach %s", want)
	}
	var chunks, pngs int
	for _, report := range reports {
		chunks += report.chunks
		pngs += report.pngs
	}
	t.Logf("successful chunks=%d PNGs=%d", chunks, pngs)
	if want == "video" && chunks == 0 || want == "fallback" && pngs == 0 {
		t.Fatal("no successful transfer in requested mode")
	}
}

type hardwareDisplay struct {
	displayUSB
	t            *testing.T
	chunks, pngs int
}

func (d *hardwareDisplay) SendPNG(ctx context.Context, data []byte) ([]byte, error) {
	response, err := d.displayUSB.SendPNG(ctx, data)
	if err == nil {
		d.pngs++
	}
	return response, err
}

func (d *hardwareDisplay) SendH264Stream(ctx context.Context, stream io.ReadCloser, opts turzx.VideoOptions, wait time.Duration) (turzx.VideoReport, error) {
	report, err := d.displayUSB.SendH264Stream(ctx, stream, opts, wait)
	d.chunks += report.Chunks
	if report.Chunks > 0 && (report.StopError != nil || len(report.StopResponse) < 2 || report.StopResponse[0] != 123 || report.StopResponse[1] != 0xC8) {
		d.t.Errorf("H264 stop was not acknowledged: response=%X error=%v", report.StopResponse[:min(8, len(report.StopResponse))], report.StopError)
	}
	d.t.Logf("H264 chunks=%d bytes=%d max_queue=%d max_chunk_wait=%s stop_header=%X stop_error=%v error=%v", report.Chunks, report.Bytes, report.MaxQueueDepth, report.MaxChunkWait, report.StopResponse[:min(8, len(report.StopResponse))], report.StopError, err)
	return report, err
}
