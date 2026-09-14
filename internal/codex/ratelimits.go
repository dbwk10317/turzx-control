// SPDX-License-Identifier: GPL-3.0-or-later

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var errRateLimitMissing = errors.New("Codex rate limits unavailable")

// WindowKind identifies the two windows understood by the first theme.
type WindowKind string

const (
	FiveHour WindowKind = "five_hour"
	Weekly   WindowKind = "weekly"
	Other    WindowKind = "other"
)

// Window is one usage limit window. UsedPercent is retained as returned by
// Codex; RemainingPercent is clamped to the displayable range.
type Window struct {
	Kind               WindowKind `json:"kind"`
	WindowDurationMins int        `json:"window_duration_mins"`
	UsedPercent        int        `json:"used_percent"`
	RemainingPercent   int        `json:"remaining_percent"`
	ResetsAt           time.Time  `json:"resets_at"`
}

// Snapshot is a validated, account-independent usage observation.
// Account email and account ID are intentionally not represented here.
type Snapshot struct {
	Plan       string    `json:"plan"`
	FiveHour   *Window   `json:"five_hour"`
	Weekly     *Window   `json:"weekly"`
	Other      []Window  `json:"other,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

// Read obtains and validates one bucket's current rate limits. The account is
// read before and after so a profile switch mid-read is rejected.
func (p *Process) Read(ctx context.Context, bucket string) (Snapshot, error) {
	if strings.TrimSpace(bucket) == "" {
		return Snapshot{}, errRateLimitMissing
	}

	before, err := p.readAccount(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !before.sameIdentity(p.expected) {
		return Snapshot{}, errAccountChanged
	}
	rate, err := p.request(ctx, "account/rateLimits/read", map[string]any{
		"excludeResetCreditDetails": true,
	})
	if err != nil {
		return Snapshot{}, err
	}
	after, err := p.readAccount(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !after.sameIdentity(p.expected) || !before.sameIdentity(after) {
		return Snapshot{}, errAccountChanged
	}
	return parseRateLimits(rate.result, bucket, after.plan, rate.receivedAt)
}

func parseRateLimits(raw json.RawMessage, bucket, plan string, receivedAt time.Time) (Snapshot, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return Snapshot{}, errInvalidResponse
	}
	selected, ok := root["rateLimits"]
	byID, hasByID := root["rateLimitsByLimitId"]
	if hasByID && string(byID) != "null" {
		var limits map[string]json.RawMessage
		if json.Unmarshal(byID, &limits) != nil {
			return Snapshot{}, errRateLimitMissing
		}
		selected, ok = limits[bucket]
		if !ok {
			return Snapshot{}, errRateLimitMissing
		}
	} else if ok {
		var single map[string]json.RawMessage
		if json.Unmarshal(selected, &single) != nil {
			return Snapshot{}, errRateLimitMissing
		}
		if rawID, exists := single["limitId"]; exists && string(rawID) != "null" {
			var limitID string
			if !readString(rawID, &limitID) || limitID != bucket {
				return Snapshot{}, errRateLimitMissing
			}
		}
	}
	if !ok || string(selected) == "null" {
		return Snapshot{}, errRateLimitMissing
	}
	windows, err := parseWindows(selected)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Plan: plan, ReceivedAt: receivedAt}
	for _, window := range windows {
		w := window
		switch w.Kind {
		case FiveHour:
			snapshot.FiveHour = &w
		case Weekly:
			snapshot.Weekly = &w
		default:
			snapshot.Other = append(snapshot.Other, w)
		}
	}
	return snapshot, nil
}

func parseWindows(raw json.RawMessage) ([]Window, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errInvalidResponse
	}
	seen := make(map[int]struct{}, 2)
	windows := make([]Window, 0, 2)
	for _, name := range []string{"primary", "secondary"} {
		value, exists := fields[name]
		if !exists || string(value) == "null" {
			continue
		}
		var item map[string]json.RawMessage
		if json.Unmarshal(value, &item) != nil {
			return nil, errInvalidResponse
		}
		mins, ok := integer(item["windowDurationMins"])
		if !ok || mins <= 0 {
			return nil, errInvalidResponse
		}
		duration := int(mins)
		if _, exists := seen[duration]; exists {
			return nil, errInvalidResponse
		}
		seen[duration] = struct{}{}
		used, ok := integer(item["usedPercent"])
		if !ok || used < 0 {
			return nil, errInvalidResponse
		}
		// A zero or negative epoch is not a reset time; it would silently read
		// as an already-expired window.
		reset, ok := integer(item["resetsAt"])
		if !ok || reset <= 0 {
			return nil, errInvalidResponse
		}
		window := Window{
			WindowDurationMins: duration,
			UsedPercent:        int(used),
			RemainingPercent:   int(max(100-used, 0)),
			ResetsAt:           time.Unix(reset, 0).UTC(),
		}
		switch duration {
		case 300:
			window.Kind = FiveHour
		case 10080:
			window.Kind = Weekly
		default:
			window.Kind = Other
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func integer(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil
}
