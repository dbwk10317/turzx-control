// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("GO_WANT_SENSOR_HELPER") {
	case "1":
		runSensorHelperFixture()
		return
	}
	os.Exit(m.Run())
}

func runSensorHelperFixture() {
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		switch mode := os.Getenv("SENSOR_HELPER_MODE"); mode {
		case "timeout":
			pauseMs := os.Getenv("SENSOR_HELPER_TIMEOUT_MS")
			if pauseMs != "" {
				if dur, err := time.ParseDuration(pauseMs + "ms"); err == nil {
					time.Sleep(dur)
				}
			}
		case "exit":
			return
		case "bad-json":
			_, _ = os.Stdout.WriteString("{\n")
		case "oversize":
			_, _ = os.Stdout.WriteString(strings.Repeat("x", 2*1024*1024+1) + "\n")
		case "snapshot":
			fallthrough
		default:
			payload := os.Getenv("SENSOR_HELPER_PAYLOAD")
			if payload == "" {
				payload = `{"protocol_version":1,"observed_at":"2026-01-01T00:00:00Z","driver_installed":true,"elevated":true,"errors":[],"sensors":[]}`
			}
			_, _ = os.Stdout.WriteString(payload + "\n")
		}
	}
	_ = in.Err()
}

func TestParseHelperSnapshot(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		snapshot, err := parseHelperSnapshot(`{"protocol_version":1,"observed_at":"2026-01-01T00:00:00Z","driver_installed":false,"elevated":true,"errors":[],"sensors":[]}`, "")
		if err != nil || snapshot.ProtocolVersion != 1 || snapshot.ObservedAt == nil {
			t.Fatalf("parse = %#v, %v", snapshot, err)
		}
	})

	t.Run("missing_observed_at", func(t *testing.T) {
		if _, err := parseHelperSnapshot(`{"protocol_version":1}`, ""); err == nil {
			t.Fatal("expected missing observed_at error")
		}
	})

	t.Run("unsupported_version", func(t *testing.T) {
		if _, err := parseHelperSnapshot(`{"protocol_version":2,"observed_at":"2026-01-01T00:00:00Z"}`, ""); err == nil {
			t.Fatal("expected unsupported version error")
		}
	})
}

func TestParseHelperSnapshotOversize(t *testing.T) {
	response := strings.Repeat("x", 2*1024*1024+1)
	if _, err := parseHelperSnapshot(response, ""); err == nil {
		t.Fatal("expected oversize response error")
	}
}

func TestSensorProcessSampleTimeoutClosesProcess(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "timeout")
	t.Setenv("SENSOR_HELPER_TIMEOUT_MS", "10000")
	process, err := StartSensors(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := process.Sample(ctx); err == nil {
		t.Fatal("expected timeout error")
	}
	process.Close()
	if _, err := process.Sample(context.Background()); err == nil {
		t.Fatal("expected closed process error")
	}
}

func TestSensorProcessReturnsErrorAfterChildExit(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "exit")
	process, err := StartSensors(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	if _, err := process.Sample(context.Background()); err == nil {
		t.Fatal("expected child exit error")
	}
}

func TestSensorProcessBadJSON(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "bad-json")
	process, err := StartSensors(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()

	if _, err := process.Sample(context.Background()); err == nil || !strings.Contains(err.Error(), "parse helper response") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestSampleIsKilledByCancel(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "timeout")
	t.Setenv("SENSOR_HELPER_TIMEOUT_MS", "10000")
	ctx, cancel := context.WithCancel(context.Background())
	process, err := StartSensors(ctx, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	if _, err := process.Sample(context.Background()); err == nil {
		t.Fatal("expected sample error after cancel")
	}
}

func TestSensorProcessCloseKillsOnDemand(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "timeout")
	process, err := StartSensors(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	process.Close()
	if _, err := process.Sample(context.Background()); err == nil {
		t.Fatal("expected error after manual close")
	}
}

func TestSensorHelperProcessPathMustBeExecutable(t *testing.T) {
	if _, err := StartSensors(context.Background(), os.Args[0]+"does-not-exist"); err == nil {
		t.Fatal("expected startup error")
	}
}

func TestSensorProcessReusesChildAndJoinsOnClose(t *testing.T) {
	t.Setenv("GO_WANT_SENSOR_HELPER", "1")
	t.Setenv("SENSOR_HELPER_MODE", "snapshot")
	p, err := StartSensors(context.Background(), os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := 0; i < 3; i++ {
		if _, err := p.Sample(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	p.Close()
	select {
	case <-p.done:
	default:
		t.Fatal("child not joined")
	}
	select {
	case <-p.readerDone:
	default:
		t.Fatal("reader not joined")
	}
}

func TestSensorProcessProtocolFailureClosesAndJoins(t *testing.T) {
	for _, mode := range []string{"oversize", "bad-json", "exit"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GO_WANT_SENSOR_HELPER", "1")
			t.Setenv("SENSOR_HELPER_MODE", mode)
			p, err := StartSensors(context.Background(), os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if _, err := p.Sample(context.Background()); err == nil {
				t.Fatal("accepted invalid response")
			}
			select {
			case <-p.done:
			default:
				t.Fatal("child not joined after failure")
			}
			select {
			case <-p.readerDone:
			default:
				t.Fatal("reader not joined after failure")
			}
			if _, err := p.Sample(context.Background()); err == nil {
				t.Fatal("reused failed stream")
			}
		})
	}
}
