// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const sample = `{"session_id":"sess-1","rate_limits":{"five_hour":{"used_percentage":12.5,"resets_at":2000000000},"seven_day":{"used_percentage":99,"resets_at":2000000100}},"model":{"id":"ignored"}}`

func TestParseStatuslineAndBounds(t *testing.T) {
	s, err := ParseStatusline([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "sess-1" || s.FiveHour == nil || s.SevenDay == nil || s.FiveHour.ResetsAt != 2000000000 {
		t.Fatalf("unexpected %+v", s)
	}
	if _, err := ParseStatusline(append([]byte(sample), 'x')); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	// A shell in the statusline chain can prepend a BOM; ingestion must not
	// fail silently on it.
	if _, err := ParseStatusline(append([]byte{0xEF, 0xBB, 0xBF}, sample...)); err != nil {
		t.Fatalf("leading BOM rejected: %v", err)
	}
	bad := `{"session_id":"s","rate_limits":{"five_hour":{"used_percentage":-1,"resets_at":1}}}`
	if _, err := ParseStatusline([]byte(bad)); err == nil {
		t.Fatal("out of range accepted")
	}
	over, err := ParseStatusline([]byte(`{"session_id":"s","rate_limits":{"five_hour":{"used_percentage":101,"resets_at":1}}}`))
	if err != nil || over.FiveHour == nil || over.FiveHour.UsedPercentage != 101 {
		t.Fatalf("over-limit usage was not preserved: %+v %v", over, err)
	}
	if _, err := ParseStatusline([]byte(`{"session_id":"s","rate_limits":{"five_hour":{"used_percentage":1,"resets_at":0}}}`)); err == nil {
		t.Fatal("bad reset accepted")
	}
	s, err = ParseStatusline([]byte(`{"session_id":"s"}`))
	if err != nil || s.FiveHour != nil || s.SevenDay != nil {
		t.Fatalf("missing windows: %+v %v", s, err)
	}
	if _, err := ParseStatusline([]byte(`{"session_id":"../x"}`)); err == nil {
		t.Fatal("path traversal id accepted")
	}
	if _, err := ParseStatusline(make([]byte, MaxJSONSize+1)); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestObserverRejectsMalformedAndWrongBinding(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "sess-1.json")
	r, _ := NewReceiver(p, "binding")
	if err := os.WriteFile(p, []byte(`{"schema_version":1,"binding_id":"other","session_id":"sess-1","sequence":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, fresh := r.Observe(); fresh {
		t.Fatal("wrong binding accepted")
	}
	if err := os.WriteFile(p, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, fresh := r.Observe(); fresh {
		t.Fatal("malformed accepted")
	}
}

func TestObserverIgnoresDuplicateAndLowerSequence(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "sess-1.json")
	r, _ := NewReceiver(p, "binding")
	writeEnvelope := func(sequence uint64) {
		t.Helper()
		body, err := json.Marshal(Envelope{SchemaVersion: SchemaVersion, BindingID: "binding", SessionID: "sess-1", Sequence: sequence})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeEnvelope(3)
	if _, fresh := r.Observe(); fresh {
		t.Fatal("baseline was fresh")
	}
	for _, sequence := range []uint64{3, 2} {
		writeEnvelope(sequence)
		if _, fresh := r.Observe(); fresh {
			t.Fatalf("sequence %d was fresh", sequence)
		}
	}
	writeEnvelope(4)
	if _, fresh := r.Observe(); !fresh {
		t.Fatal("higher sequence was not fresh")
	}
}

func TestStoreRejectsExistingEnvelopeFromAnotherBinding(t *testing.T) {
	d := t.TempDir()
	status, _ := ParseStatusline([]byte(sample))
	first, _ := NewStore(d, "binding-1")
	if _, err := first.Write(status); err != nil {
		t.Fatal(err)
	}
	second, _ := NewStore(d, "binding-2")
	if _, err := second.Write(status); err == nil {
		t.Fatal("existing envelope from another binding was overwritten")
	}
}

func TestStoreLockHasShortTimeout(t *testing.T) {
	d := t.TempDir()
	lock, err := acquireLock(filepath.Join(d, "sess-1.json.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(lock)
	store, _ := NewStore(d, "binding")
	status, _ := ParseStatusline([]byte(sample))
	started := time.Now()
	if _, err := store.Write(status); err == nil {
		t.Fatal("contended lock was acquired")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock timeout took %v", elapsed)
	}
}

func TestStoreSequenceAndBaseline(t *testing.T) {
	d := t.TempDir()
	st, err := NewStore(d, "binding")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := ParseStatusline([]byte(sample))
	if e, err := st.Write(s); err != nil || e.Sequence != 1 {
		t.Fatalf("first write: %+v %v", e, err)
	}
	r, _ := NewReceiver(filepath.Join(d, "sess-1.json"), "binding")
	if _, fresh := r.Observe(); fresh {
		t.Fatal("baseline was fresh")
	}
	if _, err := st.Write(s); err != nil {
		t.Fatal(err)
	}
	if _, fresh := r.Observe(); !fresh {
		t.Fatal("higher sequence not fresh")
	}
}

func TestConcurrentWriters(t *testing.T) {
	d := t.TempDir()
	st, _ := NewStore(d, "binding")
	s, _ := ParseStatusline([]byte(sample))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.Write(s); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(d, "sess-1.json"))
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	if e.Sequence != 8 {
		t.Fatalf("sequence=%d", e.Sequence)
	}
}

func TestStoreSkipsStatuslineWithoutRateLimits(t *testing.T) {
	d := t.TempDir()
	st, _ := NewStore(d, "binding")
	full, _ := ParseStatusline([]byte(sample))
	if _, err := st.Write(full); err != nil {
		t.Fatal(err)
	}
	empty, _ := ParseStatusline([]byte(`{"session_id":"sess-1"}`))
	if _, err := st.Write(empty); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(d, "sess-1.json"))
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	if e.Sequence != 1 || e.FiveHour == nil {
		t.Fatalf("empty statusline replaced observation: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(d, "sess-2.json")); !os.IsNotExist(err) {
		t.Fatalf("stat = %v, want no file for a session without rate limits", err)
	}
}
