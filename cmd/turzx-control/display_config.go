// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

type displaySelectionJSON struct {
	CPUTemperatureSensor         string `json:"cpu-temperature-sensor"`
	GPUUsageSensor               string `json:"gpu-usage-sensor"`
	GPUTemperatureSensor         string `json:"gpu-temperature-sensor"`
	RAMTemperatureSensor         string `json:"ram-temperature-sensor"`
	MotherboardTemperatureSensor string `json:"motherboard-temperature-sensor"`
	RAMTemperatureUnsupported    bool   `json:"ram-temperature-unsupported"`
}

type displaySettingsJSON struct {
	Background     string               `json:"background"`
	FFmpeg         string               `json:"ffmpeg"`
	Theme          string               `json:"theme"`
	Brightness     int                  `json:"brightness"`
	ChunkWait      string               `json:"chunk-wait"`
	SensorHelper   string               `json:"sensor-helper"`
	SensorSnapshot string               `json:"sensor-snapshot,omitempty"`
	Selection      displaySelectionJSON `json:"selection"`
}

func (d displaySettings) MarshalJSON() ([]byte, error) {
	s := d.Selection
	return json.Marshal(displaySettingsJSON{d.Background, d.FFmpeg, d.Theme, d.Brightness, d.ChunkWait, d.SensorHelper, d.SensorSnapshot, displaySelectionJSON{
		s.CPUTemperatureSensor, s.GPUUsageSensor, s.GPUTemperatureSensor, s.RAMTemperatureSensor,
		s.MotherboardTemperatureSensor, s.RAMTemperatureUnsupported,
	}})
}

func (d *displaySettings) UnmarshalJSON(data []byte) error {
	var v displaySettingsJSON
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("display settings contain trailing JSON")
		}
		return err
	}
	*d = displaySettings{Background: v.Background, FFmpeg: v.FFmpeg, Theme: v.Theme, Brightness: v.Brightness, ChunkWait: v.ChunkWait, SensorHelper: v.SensorHelper, SensorSnapshot: v.SensorSnapshot, Selection: metric.HardwareSensorSelection{
		CPUTemperatureSensor: v.Selection.CPUTemperatureSensor, GPUUsageSensor: v.Selection.GPUUsageSensor,
		GPUTemperatureSensor: v.Selection.GPUTemperatureSensor, RAMTemperatureSensor: v.Selection.RAMTemperatureSensor,
		MotherboardTemperatureSensor: v.Selection.MotherboardTemperatureSensor, RAMTemperatureUnsupported: v.Selection.RAMTemperatureUnsupported,
	}}
	return nil
}

func normalizeDisplaySettings(value displaySettings) (displaySettings, error) {
	value.Background = strings.TrimSpace(value.Background)
	if value.Background == "" {
		return displaySettings{}, errors.New("display background is required")
	}
	if value.Background != "" {
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
	}
	var err error
	value.FFmpeg, err = normalizeExecutablePath(strings.TrimSpace(value.FFmpeg), "FFmpeg executable")
	if err != nil {
		return displaySettings{}, err
	}
	value.Theme = strings.TrimSpace(value.Theme)
	if value.Theme != "azure-ribbon" && value.Theme != "smon-halloween" {
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
	if err := value.Selection.Validate(); err != nil {
		return displaySettings{}, err
	}
	if value.SensorHelper == "" && value.SensorSnapshot == "" && value.Selection != (metric.HardwareSensorSelection{}) {
		return displaySettings{}, errors.New("sensor selection requires sensor helper or snapshot")
	}
	return value, nil
}

func displayFlags(flags *flag.FlagSet, saved *displaySettings) func() (*displaySettings, error) {
	value := displaySettings{FFmpeg: "ffmpeg", Theme: "smon-halloween", Brightness: 32, ChunkWait: "3s"}
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
	ram := flags.String("ram-temperature-sensor", value.Selection.RAMTemperatureSensor, "RAM temperature sensor ID")
	board := flags.String("motherboard-temperature-sensor", value.Selection.MotherboardTemperatureSensor, "motherboard temperature sensor ID")
	ramUnsupported := flags.Bool("ram-temperature-unsupported", value.Selection.RAMTemperatureUnsupported, "use motherboard fallback for RAM temperature")
	return func() (*displaySettings, error) {
		result := displaySettings{Background: *background, FFmpeg: *ffmpeg, Theme: *theme, Brightness: *brightness, ChunkWait: *chunkWait, SensorHelper: *sensorHelper, SensorSnapshot: *sensorSnapshot,
			Selection: metric.HardwareSensorSelection{CPUTemperatureSensor: *cpu, GPUUsageSensor: *gpuUsage, GPUTemperatureSensor: *gpuTemp, RAMTemperatureSensor: *ram, MotherboardTemperatureSensor: *board, RAMTemperatureUnsupported: *ramUnsupported}}
		if strings.TrimSpace(result.Background) == "" {
			return nil, nil
		}
		result, err := normalizeDisplaySettings(result)
		return &result, err
	}
}

func (d displaySettings) options() (daemon.DisplayOptions, error) {
	normalized, err := normalizeDisplaySettings(d)
	if err != nil {
		return daemon.DisplayOptions{}, err
	}
	wait, err := time.ParseDuration(normalized.ChunkWait)
	if err != nil {
		return daemon.DisplayOptions{}, err
	}
	return daemon.DisplayOptions{FFmpeg: normalized.FFmpeg, Background: normalized.Background, Theme: normalized.Theme, Timeout: 2 * time.Second, FlushTimeout: 20 * time.Millisecond, ChunkWait: wait, Brightness: byte(normalized.Brightness)}, nil
}
