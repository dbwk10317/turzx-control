// SPDX-License-Identifier: GPL-3.0-or-later

// turzx-metrics emits real host hardware observations without opening USB.
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
	"time"

	"github.com/dbwk10317/turzx-control/internal/metric"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("turzx-metrics", flag.ContinueOnError)
	count := flags.Int("samples", 5, "number of one-second hardware samples; 0 runs until interrupted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *count < 0 || flags.NArg() != 0 {
		return fmt.Errorf("samples must be non-negative; positional arguments are not supported")
	}
	hardware := metric.NewHardware()
	encoder := json.NewEncoder(output)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for i := 0; *count == 0 || i < *count; i++ {
		if ctx.Err() != nil {
			return nil
		}
		if err := encoder.Encode(hardware.Sample(ctx)); err != nil {
			return err
		}
		if *count > 0 && i+1 == *count {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}
