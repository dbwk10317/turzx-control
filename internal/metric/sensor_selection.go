// SPDX-License-Identifier: GPL-3.0-or-later
// Normalizes our helper protocol; no upstream implementation code copied.

package metric

import (
	"strings"
	"time"
)

// HardwareSensorSelection maps explicitly selected sensor IDs to output IDs.
// There is deliberately no RAM temperature here: reading DIMM thermal sensors
// means driving the SMBus from ring 0, which hung the helper and the machine,
// so the panel shows memory usage only. Usage comes from the OS, not the helper.
type HardwareSensorSelection struct {
	CPUTemperatureSensor string `json:"cpu-temperature-sensor"`
	GPUUsageSensor       string `json:"gpu-usage-sensor"`
	GPUTemperatureSensor string `json:"gpu-temperature-sensor"`
}

// Readings never reuses past values or infers lack of support from a read error.
func (s HardwareSensorSelection) Readings(snapshot HelperSnapshot, snapshotErr error, receivedAt time.Time) []Reading {
	// hardware is a required LibreHardwareMonitor HardwareType prefix; it keeps
	// an ACPI thermal zone or VRM sensor from being routed as CPU or GPU.
	routes := []struct{ id, sensorID, kind, label, hardware string }{
		{"cpu.temperature", s.CPUTemperatureSensor, "Temperature", "CPU 온도", "Cpu"},
		{"gpu.usage", s.GPUUsageSensor, "Load", "GPU 사용률", "Gpu"},
		{"gpu.temperature", s.GPUTemperatureSensor, "Temperature", "GPU 온도", "Gpu"},
	}
	readings := make([]Reading, 0, len(routes))
	for _, route := range routes {
		r := Reading{ID: route.id, SensorID: route.sensorID, Label: route.label,
			Unit: "celsius", Source: "librehardwaremonitor", State: "unconnected"}
		if route.kind == "Load" {
			r.Unit = "%"
		}
		if route.sensorID != "" {
			r.State = "error"
			switch {
			case snapshotErr != nil:
				r.Error = snapshotErr.Error()
			case route.id == "cpu.temperature" && (!snapshot.DriverInstalled || !snapshot.Elevated):
				r.Error = "CPU sensors require PawnIO and elevated helper access"
				if len(snapshot.Errors) != 0 {
					r.Error += ": " + strings.Join(snapshot.Errors, "; ")
				}
			default:
				var sensor HelperSensor
				matches := 0
				for _, candidate := range snapshot.Sensors {
					if candidate.SensorID == route.sensorID {
						sensor = candidate
						matches++
					}
				}
				switch {
				case matches == 0:
					r.Error = "sensor not found"
				case matches > 1:
					r.Error = "duplicate sensor ids"
				case sensor.Type != route.kind:
					r.Error = "sensor type mismatch"
				case !strings.HasPrefix(sensor.HardwareType, route.hardware):
					r.Error = "sensor hardware type mismatch"
				case sensor.State != "ok":
					r.Error = "helper state " + sensor.State
				case sensor.Value == nil:
					r.Error = "sensor value is null"
				case !finite(*sensor.Value):
					r.Error = "sensor value is not finite"
				case route.kind == "Load" && (*sensor.Value < 0 || *sensor.Value > 100):
					r.Error = "load must be between 0 and 100"
				case route.kind == "Temperature" && *sensor.Value < -273.15:
					r.Error = "temperature is below absolute zero"
				default:
					value := *sensor.Value
					r.Value, r.State, r.ReceivedAt = &value, "ok", &receivedAt
					r.Label = sensor.Name
				}
			}
		}
		readings = append(readings, r)
	}
	return readings
}
