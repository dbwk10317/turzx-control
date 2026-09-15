// SPDX-License-Identifier: GPL-3.0-or-later
// Claude statusline field names follow the official Claude Code statusline
// schema (https://code.claude.com/docs/en/statusline); no code is copied.

package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	SchemaVersion = 1
	MaxJSONSize   = 64 << 10
	MaxIDLength   = 128
	lockTimeout   = 250 * time.Millisecond
	lockRetry     = 5 * time.Millisecond
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Window struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

type Statusline struct {
	SessionID string  `json:"session_id"`
	FiveHour  *Window `json:"five_hour,omitempty"`
	SevenDay  *Window `json:"seven_day,omitempty"`
}

type Envelope struct {
	SchemaVersion int     `json:"schema_version"`
	BindingID     string  `json:"binding_id"`
	SessionID     string  `json:"session_id"`
	Sequence      uint64  `json:"sequence"`
	FiveHour      *Window `json:"five_hour,omitempty"`
	Weekly        *Window `json:"weekly,omitempty"`
}

type rawWindow struct {
	UsedPercentage *float64        `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}
type rawStatusline struct {
	SessionID  string `json:"session_id"`
	RateLimits *struct {
		FiveHour *rawWindow `json:"five_hour"`
		SevenDay *rawWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

var errTrailingJSON = errors.New("trailing JSON")

// decodeStrict decodes exactly one JSON value; anything after it is an error.
func decodeStrict(dec *json.Decoder, v any) error {
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingJSON
	}
	return nil
}

func ParseStatusline(data []byte) (Statusline, error) {
	if len(data) == 0 || len(data) > MaxJSONSize {
		return Statusline{}, errors.New("invalid statusline size")
	}
	// A shell in the statusline chain can prepend a UTF-8 BOM. Rejecting it
	// would fail ingestion silently, because the statusline command's stderr
	// goes nowhere.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw rawStatusline
	if err := decodeStrict(dec, &raw); err != nil {
		return Statusline{}, fmt.Errorf("statusline JSON: %w", err)
	}
	if err := validID(raw.SessionID, "session_id"); err != nil {
		return Statusline{}, err
	}
	result := Statusline{SessionID: raw.SessionID}
	if raw.RateLimits == nil {
		return result, nil
	}
	var err error
	result.FiveHour, err = parseWindow(raw.RateLimits.FiveHour)
	if err != nil {
		return Statusline{}, err
	}
	result.SevenDay, err = parseWindow(raw.RateLimits.SevenDay)
	if err != nil {
		return Statusline{}, err
	}
	return result, nil
}

func ParseStatuslineReader(r io.Reader) (Statusline, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxJSONSize+1))
	if err != nil {
		return Statusline{}, err
	}
	return ParseStatusline(b)
}

func parseWindow(raw *rawWindow) (*Window, error) {
	if raw == nil {
		return nil, nil
	}
	if raw.UsedPercentage == nil || len(raw.ResetsAt) == 0 || string(raw.ResetsAt) == "null" {
		return nil, errors.New("incomplete rate limit window")
	}
	if math.IsNaN(*raw.UsedPercentage) || math.IsInf(*raw.UsedPercentage, 0) || *raw.UsedPercentage < 0 {
		return nil, errors.New("used_percentage out of range")
	}
	var n json.Number
	if err := json.Unmarshal(raw.ResetsAt, &n); err != nil {
		return nil, errors.New("invalid resets_at")
	}
	v, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || v <= 0 {
		return nil, errors.New("invalid resets_at")
	}
	return &Window{UsedPercentage: *raw.UsedPercentage, ResetsAt: v}, nil
}

func validID(value, name string) error {
	if len(value) == 0 || len(value) > MaxIDLength || !idPattern.MatchString(value) {
		return fmt.Errorf("invalid %s", name)
	}
	return nil
}

// Store writes one envelope per session. A per-session, process-independent
// lock file protects sequence allocation.
type Store struct {
	dir, binding string
}

func NewStore(dir, bindingID string) (*Store, error) {
	if err := validID(bindingID, "binding_id"); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, errors.New("empty inbox directory")
	}
	return &Store{dir: dir, binding: bindingID}, nil
}

// Write records status under the store's binding. A statusline without
// rate_limits (session start, API-key users) is not an observation and is
// skipped so it cannot mark other sessions' values stale. A session file
// written by an earlier binding is never taken over: after a reconnect only
// new Claude sessions report, as AGENTS.md requires.
func (s *Store) Write(status Statusline) (Envelope, error) {
	if err := validID(status.SessionID, "session_id"); err != nil {
		return Envelope{}, err
	}
	if status.FiveHour == nil && status.SevenDay == nil {
		return Envelope{}, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Envelope{}, err
	}
	path := filepath.Join(s.dir, status.SessionID+".json")
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		return Envelope{}, err
	}
	defer releaseLock(lock)
	sequence := uint64(0)
	if old, err := readEnvelope(path); err == nil {
		if old.BindingID != s.binding || old.SessionID != status.SessionID {
			return Envelope{}, errors.New("existing envelope identity mismatch")
		}
		sequence = old.Sequence
	} else if !os.IsNotExist(err) {
		return Envelope{}, err
	}
	e := Envelope{SchemaVersion: SchemaVersion, BindingID: s.binding, SessionID: status.SessionID, Sequence: sequence + 1, FiveHour: status.FiveHour, Weekly: status.SevenDay}
	b, err := json.Marshal(e)
	if err != nil {
		return Envelope{}, err
	}
	if err := atomicReplace(path, b); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func readEnvelope(path string) (Envelope, error) {
	f, err := os.Open(path)
	if err != nil {
		return Envelope{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxJSONSize+1))
	if err != nil {
		return Envelope{}, err
	}
	if len(b) > MaxJSONSize {
		return Envelope{}, errors.New("envelope too large")
	}
	var e Envelope
	if err := decodeStrict(json.NewDecoder(bytes.NewReader(b)), &e); err != nil {
		return Envelope{}, fmt.Errorf("envelope JSON: %w", err)
	}
	if e.SchemaVersion != SchemaVersion || validID(e.BindingID, "binding_id") != nil || validID(e.SessionID, "session_id") != nil || e.Sequence == 0 {
		return Envelope{}, errors.New("invalid envelope")
	}
	for _, w := range []*Window{e.FiveHour, e.Weekly} {
		if w != nil && (math.IsNaN(w.UsedPercentage) || math.IsInf(w.UsedPercentage, 0) || w.UsedPercentage < 0 || w.ResetsAt <= 0) {
			return Envelope{}, errors.New("invalid rate limit window")
		}
	}
	return e, nil
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if locked {
			return f, nil
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, errors.New("Claude inbox lock timeout")
		}
		time.Sleep(lockRetry)
	}
}
func releaseLock(f *os.File) { _ = unlockFile(f); _ = f.Close() }

// Receiver watches one session file and reports strictly newer sequences.
type Receiver struct {
	path, binding string
	seen          map[string]uint64
	mu            sync.Mutex
}

func NewReceiver(path, expectedBinding string) (*Receiver, error) {
	if err := validID(expectedBinding, "binding_id"); err != nil {
		return nil, err
	}
	return &Receiver{path: path, binding: expectedBinding, seen: make(map[string]uint64)}, nil
}

// Observe returns fresh only for a strictly newer sequence. The first valid
// envelope establishes a baseline and is intentionally not reported fresh.
// Unreadable, malformed, and foreign-binding files read as no envelope.
func (r *Receiver) Observe() (Envelope, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, err := readEnvelope(r.path)
	if err != nil || e.BindingID != r.binding {
		return Envelope{}, false
	}
	last, ok := r.seen[e.SessionID]
	if !ok {
		r.seen[e.SessionID] = e.Sequence
		return e, false
	}
	if e.Sequence <= last {
		return e, false
	}
	r.seen[e.SessionID] = e.Sequence
	return e, true
}
