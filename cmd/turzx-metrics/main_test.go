// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"io"
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
