// SPDX-License-Identifier: GPL-3.0-or-later

// turzx-metrics emits real host hardware observations without opening USB.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/metric"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("turzx-metrics", flag.ContinueOnError)
	sensorHelper := flags.String("sensor-helper", "", "path to local LHM sensor helper executable")
	listSensors := flags.Bool("list-sensors", false, "print a raw helper snapshot and exit")
	cpuTemperatureSensor := flags.String("cpu-temperature-sensor", "", "helper sensor id for CPU temperature")
	gpuUsageSensor := flags.String("gpu-usage-sensor", "", "helper sensor id for GPU usage")
	gpuTemperatureSensor := flags.String("gpu-temperature-sensor", "", "helper sensor id for GPU temperature")
	ramTemperatureSensor := flags.String("ram-temperature-sensor", "", "helper sensor id for RAM temperature")
	motherboardTemperatureSensor := flags.String("motherboard-temperature-sensor", "", "helper motherboard sensor id used when RAM fallback is enabled")
	ramTemperatureUnsupported := flags.Bool("ram-temperature-unsupported", false, "disable RAM temperature and attempt fallback only when motherboard sensor is configured")
	count := flags.Int("samples", 5, "number of one-second hardware samples; 0 runs until interrupted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *count < 0 || flags.NArg() != 0 {
		return fmt.Errorf("samples must be non-negative; positional arguments are not supported")
	}

	selection := metric.HardwareSensorSelection{
		CPUTemperatureSensor:         *cpuTemperatureSensor,
		GPUUsageSensor:               *gpuUsageSensor,
		GPUTemperatureSensor:         *gpuTemperatureSensor,
		RAMTemperatureSensor:         *ramTemperatureSensor,
		MotherboardTemperatureSensor: *motherboardTemperatureSensor,
		RAMTemperatureUnsupported:    *ramTemperatureUnsupported,
	}
	if err := selection.Validate(); err != nil {
		return err
	}

	encoder := json.NewEncoder(output)
	hardware := metric.NewHardware()
	var sensorProcess *metric.SensorProcess
	if *listSensors && strings.TrimSpace(*sensorHelper) == "" {
		return errors.New("--list-sensors requires --sensor-helper")
	}
	if strings.TrimSpace(*sensorHelper) == "" && selection != (metric.HardwareSensorSelection{}) {
		return errors.New("sensor selection requires --sensor-helper")
	}
	if ctx.Err() != nil {
		return nil
	}
	if strings.TrimSpace(*sensorHelper) != "" {
		started, err := metric.StartSensors(ctx, *sensorHelper)
		if err != nil {
			return err
		}
		defer started.Close()
		sensorProcess = started
		if *listSensors {
			snapshot, err := started.Sample(ctx)
			if err != nil {
				return err
			}
			return encoder.Encode(snapshot)
		}
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for i := 0; *count == 0 || i < *count; i++ {
		if ctx.Err() != nil {
			return nil
		}

		snapshot := hardware.Sample(ctx)
		if sensorProcess != nil {
			hardwareSnapshot, helperErr := sensorProcess.Sample(ctx)
			mapped := selection.Readings(hardwareSnapshot, helperErr, time.Now())
			for _, reading := range mapped {
				for i := range snapshot.Readings {
					if snapshot.Readings[i].ID != reading.ID {
						continue
					}
					snapshot.Readings[i] = reading
				}
			}
		}
		if err := encoder.Encode(snapshot); err != nil {
			return err
		}
		if *count > 0 && i+1 == *count {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}
