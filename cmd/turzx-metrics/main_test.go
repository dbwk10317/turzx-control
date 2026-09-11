// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestRejectArguments(t *testing.T) {
	for _, args := range [][]string{{"-samples", "-1"}, {"extra"}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestCancelledRunDoesNotSample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"-samples", "0"}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidSensorFlagCombinations(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "ram_and_unsupported", args: []string{"-ram-temperature-sensor", "/ram", "-ram-temperature-unsupported"}},
		{name: "motherboard_without_unsupported", args: []string{"-motherboard-temperature-sensor", "/mb"}},
		{name: "list_without_helper", args: []string{"-list-sensors"}},
		{name: "selection_without_helper", args: []string{"-gpu-usage-sensor", "/gpu"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(context.Background(), tt.args, io.Discard); err == nil {
				t.Fatalf("accepted invalid args: %v", tt.args)
			}
		})
	}
}

func TestListSensorsEmitsHelperSnapshot(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "snapshot")
	payload := `{"protocol_version":1,"observed_at":"2026-01-01T00:00:00Z","driver_installed":true,"elevated":true,"errors":[],"sensors":[{"sensor_id":"x","name":"CPU","type":"Temperature","state":"ok","value":42.0}]}`
	t.Setenv("SENSOR_HELPER_PAYLOAD", payload)
	helperPath := os.Args[0]

	var out bytes.Buffer
	if err := run(context.Background(), []string{"-sensor-helper", helperPath, "-list-sensors", "-samples", "3"}, &out); err != nil {
		t.Fatalf("run list-sensors = %v", err)
	}
	output := append([]byte{}, out.Bytes()...)
	if len(output) == 0 {
		t.Fatal("list-sensors output empty")
	}
	var snapshot struct {
		ProtocolVersion int `json:"protocol_version"`
	}
	if err := json.Unmarshal(output, &snapshot); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if snapshot.ProtocolVersion != 1 {
		t.Fatalf("snapshot = %#v, want protocol_version 1", snapshot)
	}
}

func TestMain(m *testing.M) {
	switch os.Getenv("GO_WANT_SENSOR_HELPER") {
	case "1":
		runMainSensorHelperFixture()
		return
	}
	os.Exit(m.Run())
}

func runMainSensorHelperFixture() {
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		payload := os.Getenv("SENSOR_HELPER_PAYLOAD")
		if payload == "" {
			payload = `{"protocol_version":1,"observed_at":"2026-01-01T00:00:00Z","driver_installed":true,"elevated":true,"errors":[],"sensors":[]}`
		}
		_, _ = os.Stdout.WriteString(payload + "\n")
	}
	_ = in.Err()
}
