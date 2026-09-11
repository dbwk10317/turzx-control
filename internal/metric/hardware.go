// SPDX-License-Identifier: GPL-3.0-or-later
//
// System usage is collected with github.com/shirou/gopsutil; no upstream
// implementation code is copied.

// Package metric collects normalized host metrics.
package metric

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// Reading is one normalized metric observation.
type Reading struct {
	ID         string     `json:"id"`
	Value      *float64   `json:"value"`
	Unit       string     `json:"unit"`
	Source     string     `json:"source"`
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at"`
}

// HardwareSnapshot contains the readings produced by one collection pass.
type HardwareSnapshot struct {
	ObservedAt time.Time `json:"observed_at"`
	Readings   []Reading `json:"readings"`
}

// Hardware collects host-wide CPU and memory usage.
type Hardware struct {
	mu            sync.Mutex
	previousCPU   *cpu.TimesStat
	cpuTimes      func(context.Context, bool) ([]cpu.TimesStat, error)
	virtualMemory func(context.Context) (*mem.VirtualMemoryStat, error)
	now           func() time.Time
}

// NewHardware creates a hardware collector. CPU usage needs two successful
// samples, so its first reading has state "collecting".
func NewHardware() *Hardware {
	return &Hardware{
		cpuTimes:      cpu.TimesWithContext,
		virtualMemory: mem.VirtualMemoryWithContext,
		now:           time.Now,
	}
}

// Sample collects each hardware metric independently.
func (h *Hardware) Sample(ctx context.Context) HardwareSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := h.now()
	readings := []Reading{
		h.sampleCPU(ctx, now),
		{ID: "cpu.temperature", Unit: "celsius", Source: "librehardwaremonitor", State: "unconnected"},
		{ID: "gpu.usage", Unit: "%", Source: "librehardwaremonitor", State: "unconnected"},
		{ID: "gpu.temperature", Unit: "celsius", Source: "librehardwaremonitor", State: "unconnected"},
		h.sampleRAM(ctx, now),
		{ID: "ram.temperature", Unit: "celsius", Source: "librehardwaremonitor", State: "unconnected"},
	}
	return HardwareSnapshot{ObservedAt: now, Readings: readings}
}

func (h *Hardware) sampleCPU(ctx context.Context, now time.Time) Reading {
	reading := Reading{ID: "cpu.usage", Unit: "%", Source: "gopsutil", State: "collecting"}
	times, err := h.cpuTimes(ctx, false)
	if err != nil || len(times) != 1 || !validCPUTimes(times[0]) {
		h.previousCPU = nil
		reading.State = "error"
		return reading
	}

	current := times[0]
	previous := h.previousCPU
	h.previousCPU = &current
	if previous == nil {
		return reading
	}

	value, valid := cpuUsage(*previous, current)
	if !valid {
		reading.State = "error"
		return reading
	}

	reading.Value = &value
	reading.State = "ok"
	reading.ObservedAt = &now
	return reading
}

func (h *Hardware) sampleRAM(ctx context.Context, now time.Time) Reading {
	reading := Reading{ID: "ram.usage", Unit: "%", Source: "gopsutil", State: "error"}
	memory, err := h.virtualMemory(ctx)
	if err != nil || memory == nil || !finite(memory.UsedPercent) || memory.UsedPercent < 0 || memory.UsedPercent > 100 {
		return reading
	}

	value := memory.UsedPercent
	reading.Value = &value
	reading.State = "ok"
	reading.ObservedAt = &now
	return reading
}

func validCPUTimes(t cpu.TimesStat) bool {
	values := [...]float64{t.User, t.System, t.Idle, t.Nice, t.Iowait, t.Irq, t.Softirq, t.Steal}
	for _, value := range values {
		if value < 0 || !finite(value) {
			return false
		}
	}
	return true
}

func cpuTotals(t cpu.TimesStat) (total, busy float64) {
	total = t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	busy = total - t.Idle - t.Iowait
	return total, busy
}

func cpuUsage(previous, current cpu.TimesStat) (float64, bool) {
	previousValues := [...]float64{previous.User, previous.System, previous.Idle, previous.Nice, previous.Iowait, previous.Irq, previous.Softirq, previous.Steal}
	currentValues := [...]float64{current.User, current.System, current.Idle, current.Nice, current.Iowait, current.Irq, current.Softirq, current.Steal}
	for i := range previousValues {
		if currentValues[i] < previousValues[i] {
			return 0, false
		}
	}

	previousTotal, previousBusy := cpuTotals(previous)
	currentTotal, currentBusy := cpuTotals(current)
	totalDelta := currentTotal - previousTotal
	busyDelta := currentBusy - previousBusy
	if !finite(totalDelta) || !finite(busyDelta) || totalDelta <= 0 || busyDelta < 0 || busyDelta > totalDelta {
		return 0, false
	}
	value := busyDelta / totalDelta * 100
	return value, finite(value)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
