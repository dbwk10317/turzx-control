// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ReadSensorSnapshot reads one bounded snapshot written by the elevated helper.
func ReadSensorSnapshot(path string, now time.Time) (HelperSnapshot, error) {
	if strings.TrimSpace(path) == "" {
		return HelperSnapshot{}, errors.New("sensor snapshot path is empty")
	}
	if err := validateTrustedSnapshotPath(path); err != nil {
		return HelperSnapshot{}, err
	}
	return readSensorSnapshotContents(path, now, false)
}

// readSensorSnapshotContents parses one snapshot. fixture skips the trusted
// handle checks so parser tests can use temporary files.
func readSensorSnapshotContents(path string, now time.Time, fixture bool) (HelperSnapshot, error) {
	var snapshot HelperSnapshot
	f, err := openSnapshotFileForRead(path, fixture)
	if err != nil {
		return snapshot, fmt.Errorf("open sensor snapshot: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxResponseBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return snapshot, fmt.Errorf("read sensor snapshot: %w", readErr)
	}
	if closeErr != nil {
		return snapshot, fmt.Errorf("close sensor snapshot: %w", closeErr)
	}
	if len(data) > maxResponseBytes {
		return snapshot, errors.New("sensor snapshot exceeds 2 MiB")
	}
	snapshot, err = parseHelperSnapshot(string(data), "")
	if err != nil {
		return snapshot, err
	}
	if !snapshot.Elevated {
		return HelperSnapshot{}, errors.New("sensor snapshot must be elevated")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if snapshot.ObservedAt.After(now.Add(2 * time.Second)) {
		return HelperSnapshot{}, errors.New("sensor snapshot observed_at is from the future")
	}
	if now.Sub(*snapshot.ObservedAt) > 10*time.Second {
		return HelperSnapshot{}, errors.New("sensor snapshot is stale")
	}
	return snapshot, nil
}
