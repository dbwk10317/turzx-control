// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbwk10317/turzx-control/internal/metric"
)

func TestDisplayFlagsOverrideAndDisable(t *testing.T) {
	background := filepath.Join(t.TempDir(), "background.mp4")
	if err := os.WriteFile(background, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	read := displayFlags(flags, &displaySettings{Background: background, FFmpeg: "ffmpeg", Theme: "smon-halloween", Brightness: 32, ChunkWait: "3s"})
	if err := flags.Parse([]string{"-brightness=50", "-theme=azure-ribbon", "-chunk-wait=1500ms"}); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got == nil || got.Brightness != 50 || got.Theme != "azure-ribbon" || got.ChunkWait != "1500ms" {
		t.Fatalf("display override = %#v", got)
	}
	flags = flag.NewFlagSet("test", flag.ContinueOnError)
	read = displayFlags(flags, got)
	if err := flags.Parse([]string{"-background="}); err != nil {
		t.Fatal(err)
	}
	if got = read(); got != nil {
		t.Fatalf("display disable = %#v", got)
	}
}

func TestNormalizeDisplaySettingsRejectsInvalidAndNormalizesPaths(t *testing.T) {
	base := t.TempDir()
	background := filepath.Join(base, "bg.mp4")
	if err := os.WriteFile(background, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	got, err := normalizeDisplaySettings(displaySettings{Background: "bg.mp4", FFmpeg: "tools/ffmpeg", Theme: "smon-halloween", Brightness: 32, ChunkWait: "3s"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.Background) || !filepath.IsAbs(got.FFmpeg) {
		t.Fatalf("paths not normalized: %#v", got)
	}
	for _, invalid := range []displaySettings{
		{Background: background, FFmpeg: "ffmpeg", Theme: "nope", Brightness: 32, ChunkWait: "3s"},
		{Background: background, FFmpeg: "ffmpeg", Theme: "smon-halloween", Brightness: 103, ChunkWait: "3s"},
		{Background: background, FFmpeg: "ffmpeg", Theme: "smon-halloween", Brightness: 32, ChunkWait: "0s"},
	} {
		if _, err := normalizeDisplaySettings(invalid); err == nil {
			t.Fatalf("invalid display accepted: %#v", invalid)
		}
	}
}

func TestDisplaySettingsJSONUsesKebabCaseSelection(t *testing.T) {
	data, err := json.Marshal(displaySettings{Background: "bg.mp4", FFmpeg: "ffmpeg", Theme: "smon-halloween", Brightness: 32, ChunkWait: "3s", Selection: metric.HardwareSensorSelection{GPUUsageSensor: "gpu/load"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"gpu-usage-sensor":"gpu/load"`) || strings.Contains(string(data), `"GPUUsageSensor"`) {
		t.Fatalf("selection JSON = %s", data)
	}
}

func TestSnapshotSensorSelectionSurvivesSettingsRoundTrip(t *testing.T) {
	background := filepath.Join(t.TempDir(), "background.mp4")
	if err := os.WriteFile(background, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := displaySettings{Background: background, FFmpeg: "ffmpeg", Theme: "smon-halloween", Brightness: 32, ChunkWait: "3s", SensorSnapshot: filepath.Join(t.TempDir(), "snapshot.json"), Selection: metric.HardwareSensorSelection{CPUTemperatureSensor: "/intelcpu/0/temperature/14", GPUUsageSensor: "/gpu-nvidia/0/load/0"}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var saved displaySettings
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	got, err := normalizeDisplaySettings(saved)
	if err != nil || got.SensorSnapshot != input.SensorSnapshot || got.Selection != input.Selection {
		t.Fatalf("snapshot settings=%+v error=%v", got, err)
	}
	saved.SensorHelper = "helper.exe"
	if _, err := normalizeDisplaySettings(saved); err == nil {
		t.Fatal("accepted two sensor providers")
	}
	saved.SensorHelper = ""
	saved.SensorSnapshot = ""
	if _, err := normalizeDisplaySettings(saved); err == nil {
		t.Fatal("accepted selected sensors without a provider")
	}
}
