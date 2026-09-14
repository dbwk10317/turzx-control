// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
			_, err := readSensorSnapshotContents(path, now, true)
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
	if _, err := readSensorSnapshotContents(path, time.Now(), true); err == nil {
		t.Fatal("expected size error")
	}
	dir := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Only the trusted Windows open path checks the file kind; the fixture
	// path would fail later for the wrong reason.
	if runtime.GOOS == "windows" {
		if _, err := openSnapshotFileForRead(dir, false); err == nil {
			t.Fatal("expected regular file error")
		}
	}
}

// Only Windows can vouch for the snapshot; every other OS refuses the trusted path.
func TestReadSensorSnapshotUnsupportedOutsideWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("trusted path exists on Windows")
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(`{"protocol_version":1,"observed_at":"2026-09-14T00:59:55Z","elevated":true,"sensors":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSensorSnapshot(path, time.Now()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("ReadSensorSnapshot() error = %v, want ErrUnsupported", err)
	}
}
