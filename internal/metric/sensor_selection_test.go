// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestHardwareSensorSelectionValidationRules(t *testing.T) {
	t.Run("ram_sensor_and_unsupported", func(t *testing.T) {
		selection := HardwareSensorSelection{
			RAMTemperatureSensor:         "/ram/0/temperature/0",
			RAMTemperatureUnsupported:    true,
			MotherboardTemperatureSensor: "",
		}
		if err := selection.Validate(); err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("motherboard_requires_unsupported", func(t *testing.T) {
		selection := HardwareSensorSelection{
			MotherboardTemperatureSensor: "/mb/0/temperature/0",
		}
		if err := selection.Validate(); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestHardwareSensorSelectionReadingsApplyIDsAndMetadata(t *testing.T) {
	selection := HardwareSensorSelection{
		CPUTemperatureSensor: "/cpu/0/temperature/0",
		GPUUsageSensor:       "/gpu-nvidia/0/load/0",
		GPUTemperatureSensor: "/gpu-nvidia/0/temperature/0",
		RAMTemperatureSensor: "/ram/0/temperature/0",
	}

	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors: []HelperSensor{
			{SensorID: "/cpu/0/temperature/0", Name: "CPU", Type: "Temperature", State: "ok", Value: floatPtr(42.5)},
			{SensorID: "/gpu-nvidia/0/load/0", Name: "GPU Load", Type: "Load", State: "ok", Value: floatPtr(57.0)},
			{SensorID: "/gpu-nvidia/0/temperature/0", Name: "GPU Temp", Type: "Temperature", State: "ok", Value: floatPtr(62.0)},
			{SensorID: "/ram/0/temperature/0", Name: "RAM", Type: "Temperature", State: "ok", Value: floatPtr(38.0)},
		},
	}

	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	if len(readings) != 4 {
		t.Fatalf("readings len = %d, want 4", len(readings))
	}
	cpu := readingByID(readings, "cpu.temperature")
	if cpu == nil || cpu.Value == nil || *cpu.Value != 42.5 || cpu.Label != "CPU" || cpu.SensorID != "/cpu/0/temperature/0" {
		t.Fatalf("cpu reading = %#v", cpu)
	}
	if cpu.State != "ok" {
		t.Fatalf("cpu state = %s, want ok", cpu.State)
	}
	if cpu.ReceivedAt == nil || !cpu.ReceivedAt.Equal(time.Unix(124, 0)) {
		t.Fatalf("receivedAt = %#v, want 1970-01-01 00:02:04 +0000 UTC", cpu.ReceivedAt)
	}
}

func TestHardwareSensorSelectionReadingsTypeMismatch(t *testing.T) {
	selection := HardwareSensorSelection{CPUTemperatureSensor: "/cpu/0/temperature/0"}
	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors: []HelperSensor{
			{SensorID: "/cpu/0/temperature/0", Name: "CPU", Type: "Load", State: "ok", Value: floatPtr(42.0)},
		},
	}

	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	cpu := readingByID(readings, "cpu.temperature")
	if cpu.State != "error" || cpu.Error == "" {
		t.Fatalf("cpu reading = %#v", cpu)
	}
}

func TestHardwareSensorSelectionReadingsDuplicateID(t *testing.T) {
	selection := HardwareSensorSelection{GPUUsageSensor: "/gpu-nvidia/0/load/0"}
	valueA := 42.0
	valueB := 43.0
	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors: []HelperSensor{
			{SensorID: "/gpu-nvidia/0/load/0", Name: "GPU Load A", Type: "Load", State: "ok", Value: &valueA},
			{SensorID: "/gpu-nvidia/0/load/0", Name: "GPU Load B", Type: "Load", State: "ok", Value: &valueB},
		},
	}

	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	gpu := readingByID(readings, "gpu.usage")
	if gpu.State != "error" || gpu.Error == "" {
		t.Fatalf("gpu reading = %#v", gpu)
	}
}

func TestHardwareSensorSelectionReadingsNullValue(t *testing.T) {
	selection := HardwareSensorSelection{RAMTemperatureSensor: "/ram/0/temperature/0"}
	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors: []HelperSensor{
			{SensorID: "/ram/0/temperature/0", Name: "RAM", Type: "Temperature", State: "ok", Value: nil},
		},
	}
	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	ram := readingByID(readings, "ram.temperature")
	if ram.State != "error" || ram.Error == "" {
		t.Fatalf("ram reading = %#v", ram)
	}
}

func TestHardwareSensorSelectionReadingsUnsupportedRAMWithNoBoard(t *testing.T) {
	selection := HardwareSensorSelection{
		RAMTemperatureUnsupported: true,
		CPUTemperatureSensor:      "/cpu/0/temperature/0",
	}
	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors:         []HelperSensor{{SensorID: "/cpu/0/temperature/0", Name: "CPU", Type: "Temperature", State: "ok", Value: floatPtr(44)}},
	}
	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	cpu := readingByID(readings, "cpu.temperature")
	if cpu == nil || cpu.State != "ok" || cpu.Value == nil || *cpu.Value != 44 {
		t.Fatalf("cpu reading = %#v", cpu)
	}
	ram := readingByID(readings, "ram.temperature")
	if ram == nil || ram.State != "unsupported" {
		t.Fatalf("ram reading = %#v", ram)
	}
}

func TestHardwareSensorSelectionReadingsSnapshotErrorAffectsOnlySelected(t *testing.T) {
	selection := HardwareSensorSelection{
		CPUTemperatureSensor: "/cpu/0/temperature/0",
	}
	readings := selection.Readings(HelperSnapshot{ProtocolVersion: 1}, errors.New("helper down"), time.Unix(124, 0))
	cpu := readingByID(readings, "cpu.temperature")
	gpu := readingByID(readings, "gpu.usage")
	if cpu == nil || cpu.State != "error" || cpu.Error != "helper down" || cpu.SensorID != "/cpu/0/temperature/0" {
		t.Fatalf("cpu reading = %#v", cpu)
	}
	if gpu == nil || gpu.State != "unconnected" {
		t.Fatalf("gpu reading = %#v", gpu)
	}
}

func TestHardwareSensorSelectionReadingsDriverUnavailableDoesNotFakeValues(t *testing.T) {
	selection := HardwareSensorSelection{
		CPUTemperatureSensor: "/cpu/0/temperature/0",
		RAMTemperatureSensor: "/ram/0/temperature/0",
	}
	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: false,
		Elevated:        false,
		Sensors:         []HelperSensor{},
	}
	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	cpu := readingByID(readings, "cpu.temperature")
	ram := readingByID(readings, "ram.temperature")
	if cpu == nil || ram == nil {
		t.Fatal("missing readings")
	}
	if cpu.State != "error" || !strings.Contains(cpu.Error, "PawnIO") {
		t.Fatalf("readings = %#v %#v", cpu, ram)
	}
	if ram.State != "error" || !strings.Contains(ram.Error, "PawnIO") {
		t.Fatalf("readings = %#v %#v", cpu, ram)
	}
}

func TestHardwareSensorSelectionReadingsMotherboardFallbackUsesFixedLabel(t *testing.T) {
	selection := HardwareSensorSelection{
		RAMTemperatureUnsupported:    true,
		MotherboardTemperatureSensor: "/mb/0/temperature/0",
	}
	readings := selection.Readings(HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors: []HelperSensor{
			{SensorID: "/mb/0/temperature/0", Name: "Real MB Label", Type: "Temperature", State: "ok", Value: floatPtr(37)},
		},
	}, nil, time.Unix(124, 0))
	ram := readingByID(readings, "ram.temperature")
	if ram.State != "ok" || ram.Label != "메인보드 온도" || ram.SensorID != "/mb/0/temperature/0" {
		t.Fatalf("ram reading = %#v", ram)
	}
}

func TestHardwareSensorSelectionReadingsIgnoreUnselectedDuplicateIDs(t *testing.T) {
	selection := HardwareSensorSelection{
		GPUUsageSensor: "/gpu-nvidia/0/load/3",
	}
	unselectedA := 40.0
	selected := 42.0
	snapshot := HelperSnapshot{
		ProtocolVersion: 1,
		ObservedAt:      timePtr(time.Unix(123, 0)),
		DriverInstalled: true,
		Elevated:        true,
		Sensors: []HelperSensor{
			{SensorID: "/gpu-nvidia/0/load/0", Name: "GPU Load A", Type: "Load", State: "ok", Value: &unselectedA},
			{SensorID: "/gpu-nvidia/0/load/0", Name: "GPU Load B", Type: "Load", State: "ok", Value: &unselectedA},
			{SensorID: "/gpu-nvidia/0/load/3", Name: "GPU Load Selected", Type: "Load", State: "ok", Value: &selected},
		},
	}
	readings := selection.Readings(snapshot, nil, time.Unix(124, 0))
	gpu := readingByID(readings, "gpu.usage")
	if gpu.State != "ok" || gpu.Value == nil || *gpu.Value != selected {
		t.Fatalf("gpu usage = %#v", gpu)
	}
}

func floatPtr(value float64) *float64 { return &value }

func timePtr(value time.Time) *time.Time { return &value }

func readingByID(readings []Reading, id string) *Reading {
	for i := range readings {
		if readings[i].ID == id {
			return &readings[i]
		}
	}
	return nil
}

func TestSensorValueBoundariesAndRecovery(t *testing.T) {
	s := HardwareSensorSelection{GPUUsageSensor: "load", RAMTemperatureUnsupported: true}
	for _, value := range []float64{0, -1, 101, math.NaN(), 25} {
		snapshot := HelperSnapshot{Sensors: []HelperSensor{{SensorID: "load", Type: "Load", State: "ok", Value: &value}}}
		readings := s.Readings(snapshot, nil, time.Unix(100, 0))
		gpu := readingByID(readings, "gpu.usage")
		valid := finite(value) && value >= 0 && value <= 100
		if valid != (gpu.State == "ok") || valid != (gpu.Value != nil) || valid != (gpu.ReceivedAt != nil) {
			t.Fatalf("value %v: %#v", value, gpu)
		}
		if gpu.ObservedAt != nil {
			t.Fatal("invented original sensor timestamp")
		}
		if readingByID(readings, "ram.temperature").State != "unsupported" {
			t.Fatal("lost confirmed unsupported state")
		}
	}
	failed := s.Readings(HelperSnapshot{}, errors.New("disconnected"), time.Now())
	if readingByID(failed, "gpu.usage").Value != nil || readingByID(failed, "ram.temperature").State != "unsupported" {
		t.Fatal("failed sample reused value or lost unsupported state")
	}
}
