// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadSensorSnapshotValidation(t *testing.T) {
	now := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	valid := `{"protocol_version":1,"observed_at":"2026-09-14T00:59:55Z","elevated":true,"sensors":[]}`
	for name, content := range map[string]string{
		"valid":          valid,
		"stale":          strings.Replace(valid, "00:59:55", "00:59:49", 1),
		"future":         strings.Replace(valid, "00:59:55", "01:00:03", 1),
		"not elevated":   strings.Replace(valid, `"elevated":true`, `"elevated":false`, 1),
		"wrong protocol": strings.Replace(valid, `"protocol_version":1`, `"protocol_version":2`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readSensorSnapshotFixture(path, now)
			if name == "valid" && err != nil {
				t.Fatal(err)
			}
			if name != "valid" && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestReadSensorSnapshotBoundsAndRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, make([]byte, maxResponseBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSensorSnapshotFixture(path, time.Now()); err == nil {
		t.Fatal("expected size error")
	}
	dir := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSensorSnapshotFixture(dir, time.Now()); err == nil {
		t.Fatal("expected regular file error")
	}
}
