// SPDX-License-Identifier: GPL-3.0-or-later

// turzx-codex reads Codex usage from an explicitly selected dedicated profile.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/codex"
)

const queryTimeout = 15 * time.Second

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("turzx-codex", flag.ContinueOnError)
	bin := flags.String("codex-bin", "codex", "path to the Codex CLI executable")
	home := flags.String("codex-home", "", "dedicated CODEX_HOME managed by turzx-control")
	bucket := flags.String("bucket", "codex", "exact Codex rate-limit bucket ID")
	samples := flags.Int("samples", 1, "number of observations; 0 runs until interrupted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *samples < 0 {
		return errors.New("samples must be non-negative; positional arguments are not supported")
	}
	if strings.TrimSpace(*home) == "" {
		return errors.New("--codex-home is required; the default Codex profile is not used")
	}
	if strings.TrimSpace(*bin) == "" || strings.TrimSpace(*bucket) == "" {
		return errors.New("--codex-bin and --bucket must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	process, err := codex.Start(ctx, *bin, *home)
	if err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}
	defer process.Close()
	encoder := json.NewEncoder(output)

	for i := 0; *samples == 0 || i < *samples; i++ {
		readCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		snapshot, err := process.Read(readCtx, *bucket)
		cancel()
		if err != nil {
			return fmt.Errorf("read Codex usage: %w", err)
		}
		if err := encoder.Encode(snapshot); err != nil {
			return err
		}
		if *samples > 0 && i+1 == *samples {
			return nil
		}

		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case _, open := <-process.Updates():
			timer.Stop()
			if !open {
				return errors.New("Codex app-server stopped")
			}
		case <-timer.C:
		}
	}
	return nil
}
