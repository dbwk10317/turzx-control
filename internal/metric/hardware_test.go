// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

func TestCPUUsageNeedsTwoSamples(t *testing.T) {
	collector := testHardware([]cpu.TimesStat{
		{User: 10, System: 5, Idle: 80, Iowait: 5, Guest: 50},
		{User: 30, System: 10, Idle: 140, Iowait: 20, Guest: 90},
	})

	first := collector.Sample(context.Background()).Readings[0]
	if first.State != "collecting" || first.Value != nil || first.ObservedAt != nil {
		t.Fatalf("first CPU reading = %#v, want collecting without a value", first)
	}

	second := collector.Sample(context.Background()).Readings[0]
	if second.State != "ok" || second.Value == nil || math.Abs(*second.Value-25) > 1e-9 {
		t.Fatalf("second CPU reading = %#v, want 25%%", second)
	}
}

func TestCPUResetDoesNotFabricateZero(t *testing.T) {
	collector := testHardware([]cpu.TimesStat{
		{User: 10, Idle: 90},
		{User: 5, Idle: 45},
		{User: 15, Idle: 85},
	})

	_ = collector.Sample(context.Background())
	reset := collector.Sample(context.Background()).Readings[0]
	if reset.State != "error" || reset.Value != nil {
		t.Fatalf("reset CPU reading = %#v, want error without a value", reset)
	}

	recovered := collector.Sample(context.Background()).Readings[0]
	if recovered.State != "ok" || recovered.Value == nil || math.Abs(*recovered.Value-20) > 1e-9 {
		t.Fatalf("recovered CPU reading = %#v, want 20%%", recovered)
	}
}

func TestCPUReadErrorInvalidatesBaseline(t *testing.T) {
	calls := 0
	collector := testHardware(nil)
	collector.cpuTimes = func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls++
		switch calls {
		case 1:
			return []cpu.TimesStat{{User: 10, Idle: 90}}, nil
		case 2:
			return nil, errors.New("temporary failure")
		default:
			return []cpu.TimesStat{{User: 20, Idle: 180}}, nil
		}
	}

	_ = collector.Sample(context.Background())
	failed := collector.Sample(context.Background()).Readings[0]
	afterFailure := collector.Sample(context.Background()).Readings[0]
	if failed.State != "error" || failed.Value != nil {
		t.Fatalf("failed CPU reading = %#v, want error without a value", failed)
	}
	if afterFailure.State != "collecting" || afterFailure.Value != nil {
		t.Fatalf("CPU reading after error = %#v, want a fresh baseline", afterFailure)
	}
}

func TestRAMUsageAndUnavailableSensors(t *testing.T) {
	collector := testHardware([]cpu.TimesStat{{User: 1, Idle: 9}})
	collector.virtualMemory = func(context.Context) (*mem.VirtualMemoryStat, error) {
		return &mem.VirtualMemoryStat{UsedPercent: 0}, nil
	}

	snapshot := collector.Sample(context.Background())
	ram := snapshot.Readings[4]
	if ram.State != "ok" || ram.Value == nil || *ram.Value != 0 || ram.ObservedAt == nil {
		t.Fatalf("RAM reading = %#v, want a valid zero", ram)
	}
	for _, index := range []int{1, 2, 3, 5} {
		reading := snapshot.Readings[index]
		if reading.State != "unconnected" || reading.Value != nil || reading.ObservedAt != nil {
			t.Fatalf("unavailable reading = %#v, want unconnected without a value", reading)
		}
	}
}

func TestRAMReadErrorDoesNotReuseValue(t *testing.T) {
	collector := testHardware([]cpu.TimesStat{{User: 1, Idle: 9}, {User: 2, Idle: 18}})
	calls := 0
	collector.virtualMemory = func(context.Context) (*mem.VirtualMemoryStat, error) {
		calls++
		if calls == 1 {
			return &mem.VirtualMemoryStat{UsedPercent: 42}, nil
		}
		return nil, errors.New("temporary failure")
	}

	first := collector.Sample(context.Background()).Readings[4]
	failed := collector.Sample(context.Background()).Readings[4]
	if first.State != "ok" || first.Value == nil || *first.Value != 42 {
		t.Fatalf("first RAM reading = %#v, want 42%%", first)
	}
	if failed.State != "error" || failed.Value != nil || failed.ObservedAt != nil {
		t.Fatalf("failed RAM reading = %#v, want error without stale value", failed)
	}
}

func TestInvalidNumbersAreErrors(t *testing.T) {
	collector := testHardware([]cpu.TimesStat{{User: math.NaN()}})
	collector.virtualMemory = func(context.Context) (*mem.VirtualMemoryStat, error) {
		return &mem.VirtualMemoryStat{UsedPercent: 101}, nil
	}

	snapshot := collector.Sample(context.Background())
	for _, index := range []int{0, 4} {
		reading := snapshot.Readings[index]
		if reading.State != "error" || reading.Value != nil || reading.ObservedAt != nil {
			t.Fatalf("invalid reading = %#v, want error without a value", reading)
		}
	}
}

func testHardware(samples []cpu.TimesStat) *Hardware {
	index := 0
	return &Hardware{
		cpuTimes: func(context.Context, bool) ([]cpu.TimesStat, error) {
			result := []cpu.TimesStat{samples[index]}
			index++
			return result, nil
		},
		virtualMemory: func(context.Context) (*mem.VirtualMemoryStat, error) {
			return &mem.VirtualMemoryStat{UsedPercent: 50}, nil
		},
		now: func() time.Time { return time.Unix(123, 0) },
	}
}
