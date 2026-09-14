// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsImplicitProfile(t *testing.T) {
	err := run(context.Background(), nil, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--codex-home") {
		t.Fatalf("run error = %v", err)
	}
}

func TestRunValidatesArgumentsBeforeStarting(t *testing.T) {
	err := run(context.Background(), []string{"-codex-home", t.TempDir(), "-samples", "-1"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("run error = %v", err)
	}
}

func TestRunCanceledDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"-codex-home", t.TempDir(), "-codex-bin", "missing"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run error = %v", err)
	}
}
