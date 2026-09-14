// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbwk10317/turzx-control/internal/claude"
)

func TestRunWritesOnlyAllowedFieldsAndIncrementsSequence(t *testing.T) {
	dir := t.TempDir()
	input := `{"session_id":"session-1","cwd":"C:/secret","transcript_path":"C:/secret/chat.jsonl","rate_limits":{"five_hour":{"used_percentage":25,"resets_at":2000000000}}}`
	args := []string{"-inbox-dir", dir, "-binding-id", "binding-1"}
	if err := run(context.Background(), args, strings.NewReader(input), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), args, strings.NewReader(input), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "session-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("transcript")) || bytes.Contains(raw, []byte("cwd")) {
		t.Fatalf("private input field leaked: %s", raw)
	}
	var got claude.Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 2 || got.BindingID != "binding-1" || got.SessionID != "session-1" || got.FiveHour == nil {
		t.Fatalf("unexpected envelope: %+v", got)
	}
}

func TestRunSkipsMissingRateLimits(t *testing.T) {
	dir := t.TempDir()
	err := run(context.Background(), []string{"-inbox-dir", dir, "-binding-id", "binding-1"}, strings.NewReader(`{"session_id":"session-1"}`), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "session-1.json")); !os.IsNotExist(err) {
		t.Fatalf("stat = %v, want no envelope for a statusline without rate limits", err)
	}
}

func TestRunRejectsOversizeInput(t *testing.T) {
	err := run(context.Background(), []string{"-inbox-dir", t.TempDir(), "-binding-id", "binding-1"}, strings.NewReader(strings.Repeat("x", claude.MaxJSONSize+1)), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("oversize input accepted")
	}
}

func TestRunPreservesExistingStatuslineIO(t *testing.T) {
	previous := executeForward
	t.Cleanup(func() { executeForward = previous })
	var forwarded []byte
	executeForward = func(_ context.Context, command string, input io.Reader, output, errorOutput io.Writer) error {
		if command != "existing command" {
			t.Fatalf("command = %q", command)
		}
		forwarded, _ = io.ReadAll(input)
		_, _ = io.WriteString(output, "existing output")
		_, _ = io.WriteString(errorOutput, "existing error")
		return nil
	}
	input := `{"session_id":"session-1"}`
	var output, errorOutput bytes.Buffer
	err := run(context.Background(), []string{"-inbox-dir", t.TempDir(), "-binding-id", "binding-1", "-forward-command", "existing command"}, strings.NewReader(input), &output, &errorOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwarded) != input || output.String() != "existing output" || errorOutput.String() != "existing error" {
		t.Fatalf("forwarded=%q stdout=%q stderr=%q", forwarded, output.String(), errorOutput.String())
	}
}

func TestRunForwardsInputLargerThanCollectionLimit(t *testing.T) {
	previous := executeForward
	t.Cleanup(func() { executeForward = previous })
	var forwarded int
	executeForward = func(_ context.Context, _ string, input io.Reader, _, _ io.Writer) error {
		body, err := io.ReadAll(input)
		forwarded = len(body)
		return err
	}
	input := strings.Repeat("x", claude.MaxJSONSize+1024)
	var errorOutput bytes.Buffer
	err := run(context.Background(), []string{"-inbox-dir", t.TempDir(), "-binding-id", "binding-1", "-forward-command", "existing command"}, strings.NewReader(input), io.Discard, &errorOutput)
	// The forwarded statusline succeeded, so the adapter exits cleanly and
	// only reports the inbox failure.
	if err != nil {
		t.Fatalf("run() = %v, want nil when the forward succeeded", err)
	}
	if !strings.Contains(errorOutput.String(), "write Claude inbox") {
		t.Fatalf("stderr = %q, want inbox error", errorOutput.String())
	}
	if forwarded != len(input) {
		t.Fatalf("forwarded %d of %d bytes", forwarded, len(input))
	}
}

func TestRunCollectsWhenExistingStatuslineDoesNotReadInput(t *testing.T) {
	previous := executeForward
	t.Cleanup(func() { executeForward = previous })
	executeForward = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer) error { return nil }
	dir := t.TempDir()
	input := `{"session_id":"session-1","rate_limits":{"seven_day":{"used_percentage":10,"resets_at":2000000000}}}`
	err := run(context.Background(), []string{"-inbox-dir", dir, "-binding-id", "binding-1", "-forward-command", "existing command"}, strings.NewReader(input), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "session-1.json")); err != nil {
		t.Fatal(err)
	}
}
