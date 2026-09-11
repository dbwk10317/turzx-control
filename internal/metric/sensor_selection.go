// SPDX-License-Identifier: GPL-3.0-or-later
// Normalizes our helper protocol; no upstream implementation code copied.

package metric

import (
	"errors"
	"strings"
	"time"
)

// HardwareSensorSelection maps explicitly selected sensor IDs to output IDs.
type HardwareSensorSelection struct {
	CPUTemperatureSensor         string
	GPUUsageSensor               string
	GPUTemperatureSensor         string
	RAMTemperatureSensor         string
	MotherboardTemperatureSensor string
	RAMTemperatureUnsupported    bool
}

func (s HardwareSensorSelection) Validate() error {
	if s.RAMTemperatureSensor != "" && s.RAMTemperatureUnsupported {
		return errors.New("cannot select RAM temperature and mark it unsupported")
	}
	if s.MotherboardTemperatureSensor != "" && !s.RAMTemperatureUnsupported {
		return errors.New("motherboard temperature requires confirmed unsupported RAM temperature")
	}
	return nil
}

// Readings never reuses past values or infers lack of support from a read error.
func (s HardwareSensorSelection) Readings(snapshot HelperSnapshot, snapshotErr error, receivedAt time.Time) []Reading {
	ramID, ramLabel := s.RAMTemperatureSensor, "RAM 온도"
	if s.RAMTemperatureUnsupported {
		ramID, ramLabel = s.MotherboardTemperatureSensor, "메인보드 온도"
	}
	routes := []struct{ id, sensorID, kind, label string }{
		{"cpu.temperature", s.CPUTemperatureSensor, "Temperature", "CPU 온도"},
		{"gpu.usage", s.GPUUsageSensor, "Load", "GPU 사용률"},
		{"gpu.temperature", s.GPUTemperatureSensor, "Temperature", "GPU 온도"},
		{"ram.temperature", ramID, "Temperature", ramLabel},
	}
	readings := make([]Reading, 0, len(routes))
	for _, route := range routes {
		r := Reading{ID: route.id, SensorID: route.sensorID, Label: route.label,
			Unit: "celsius", Source: "librehardwaremonitor", State: "unconnected"}
		if route.kind == "Load" {
			r.Unit = "%"
		}
		if route.id == "ram.temperature" && s.RAMTemperatureUnsupported && ramID == "" {
			r.State, r.Label = "unsupported", "RAM 온도"
		} else if route.sensorID != "" {
			r.State = "error"
			switch {
			case snapshotErr != nil:
				r.Error = snapshotErr.Error()
			case (route.id == "cpu.temperature" || route.id == "ram.temperature") && (!snapshot.DriverInstalled || !snapshot.Elevated):
				r.Error = "CPU/motherboard/RAM sensors require PawnIO and elevated helper access"
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
					if !(route.id == "ram.temperature" && s.RAMTemperatureUnsupported) {
						r.Label = sensor.Name
					}
				}
			}
		}
		readings = append(readings, r)
	}
	return readings
}
