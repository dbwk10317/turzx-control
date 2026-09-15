// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/daemon"
	"github.com/dbwk10317/turzx-control/internal/metric"
)

type displaySettings struct {
	Background     string                         `json:"background"`
	FFmpeg         string                         `json:"ffmpeg"`
	Theme          string                         `json:"theme"`
	Brightness     int                            `json:"brightness"`
	ChunkWait      string                         `json:"chunk-wait"`
	SensorHelper   string                         `json:"sensor-helper"`
	SensorSnapshot string                         `json:"sensor-snapshot,omitempty"`
	Selection      metric.HardwareSensorSelection `json:"selection"`
}

func normalizeDisplaySettings(value displaySettings) (displaySettings, error) {
	value.Background = strings.TrimSpace(value.Background)
	if value.Background == "" {
		return displaySettings{}, errors.New("display background is required")
	}
	path, err := normalizeAbsolutePath(value.Background, "display background")
	if err != nil {
		return displaySettings{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return displaySettings{}, fmt.Errorf("display background: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || strings.ToLower(filepath.Ext(path)) != ".mp4" {
		return displaySettings{}, errors.New("display background must be an existing regular .mp4 file")
	}
	value.Background = path
	value.FFmpeg, err = normalizeExecutablePath(strings.TrimSpace(value.FFmpeg), "FFmpeg executable")
	if err != nil {
		return displaySettings{}, err
	}
	value.Theme = strings.TrimSpace(value.Theme)
	if !slices.Contains(daemon.Themes(), value.Theme) {
		return displaySettings{}, fmt.Errorf("unknown display theme %q", value.Theme)
	}
	if value.Brightness < 0 || value.Brightness > 102 {
		return displaySettings{}, errors.New("display brightness must be between 0 and 102")
	}
	value.ChunkWait = strings.TrimSpace(value.ChunkWait)
	d, err := time.ParseDuration(value.ChunkWait)
	if err != nil || d <= 0 {
		return displaySettings{}, errors.New("display chunk-wait must be a positive duration")
	}
	value.SensorHelper = strings.TrimSpace(value.SensorHelper)
	if value.SensorHelper != "" {
		value.SensorHelper, err = normalizeExecutablePath(value.SensorHelper, "sensor helper")
		if err != nil {
			return displaySettings{}, err
		}
	}
	value.SensorSnapshot = strings.TrimSpace(value.SensorSnapshot)
	if value.SensorSnapshot != "" {
		value.SensorSnapshot, err = normalizeAbsolutePath(value.SensorSnapshot, "sensor snapshot")
		if err != nil {
			return displaySettings{}, err
		}
		if value.SensorHelper != "" {
			return displaySettings{}, errors.New("sensor-helper and sensor-snapshot are mutually exclusive")
		}
	}
	if value.SensorHelper == "" && value.SensorSnapshot == "" && value.Selection != (metric.HardwareSensorSelection{}) {
		return displaySettings{}, errors.New("sensor selection requires sensor helper or snapshot")
	}
	return value, nil
}

// displayFlags registers the display flags with saved values as defaults and
// returns a reader that yields nil when no background is configured. The
// result is raw; normalizedSettings validates it once.
func displayFlags(flags *flag.FlagSet, saved *displaySettings) func() *displaySettings {
	value := displaySettings{FFmpeg: "ffmpeg", Theme: daemon.DefaultTheme, Brightness: 32, ChunkWait: "3s"}
	if saved != nil {
		value = *saved
	}
	background := flags.String("background", value.Background, "local MP4 background")
	ffmpeg := flags.String("ffmpeg", value.FFmpeg, "FFmpeg executable")
	theme := flags.String("theme", value.Theme, "display theme")
	brightness := flags.Int("brightness", value.Brightness, "display brightness (0..102)")
	chunkWait := flags.String("chunk-wait", value.ChunkWait, "chunk wait duration")
	sensorHelper := flags.String("sensor-helper", value.SensorHelper, "sensor helper executable")
	sensorSnapshot := flags.String("sensor-snapshot", value.SensorSnapshot, "sensor snapshot file")
	cpu := flags.String("cpu-temperature-sensor", value.Selection.CPUTemperatureSensor, "CPU temperature sensor ID")
	gpuUsage := flags.String("gpu-usage-sensor", value.Selection.GPUUsageSensor, "GPU usage sensor ID")
	gpuTemp := flags.String("gpu-temperature-sensor", value.Selection.GPUTemperatureSensor, "GPU temperature sensor ID")
	return func() *displaySettings {
		if strings.TrimSpace(*background) == "" {
			return nil
		}
		return &displaySettings{Background: *background, FFmpeg: *ffmpeg, Theme: *theme, Brightness: *brightness, ChunkWait: *chunkWait, SensorHelper: *sensorHelper, SensorSnapshot: *sensorSnapshot,
			Selection: metric.HardwareSensorSelection{CPUTemperatureSensor: *cpu, GPUUsageSensor: *gpuUsage, GPUTemperatureSensor: *gpuTemp}}
	}
}

// options converts already-normalized settings into daemon options.
func (d displaySettings) options() (daemon.DisplayOptions, error) {
	wait, err := time.ParseDuration(d.ChunkWait)
	if err != nil {
		return daemon.DisplayOptions{}, err
	}
	return daemon.DisplayOptions{FFmpeg: d.FFmpeg, Background: d.Background, Theme: d.Theme, Timeout: 2 * time.Second, FlushTimeout: 20 * time.Millisecond, ChunkWait: wait, Brightness: byte(d.Brightness)}, nil
}
