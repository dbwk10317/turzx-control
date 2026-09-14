// SPDX-License-Identifier: GPL-3.0-or-later
// Claude statusline field names follow the official Claude Code statusline
// schema (https://code.claude.com/docs/en/statusline); no code is copied.

// turzx-claude-status forwards allowed Claude usage fields to the TURZX inbox.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/dbwk10317/turzx-control/internal/claude"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var executeForward = forwardCommand

// run forwards stdin to the user's existing statusline first and only then
// records the allowed fields. When a forward command exists its result is the
// exit status; an inbox failure is reported on stderr so the user's statusline
// output is never discarded because of our bookkeeping.
func run(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("turzx-claude-status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inboxDir := flags.String("inbox-dir", "", "dedicated Claude inbox directory")
	bindingID := flags.String("binding-id", "", "current Claude connection generation")
	forward := flags.String("forward-command", "", "existing statusline command to preserve")
	forwardTimeout := flags.Duration("forward-timeout", 2*time.Second, "maximum existing statusline runtime")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	if *inboxDir == "" {
		return errors.New("-inbox-dir is required")
	}
	if *bindingID == "" {
		return errors.New("-binding-id is required")
	}

	var raw []byte
	var forwardErr error
	if strings.TrimSpace(*forward) != "" {
		if *forwardTimeout <= 0 {
			return errors.New("-forward-timeout must be positive")
		}
		capture := &claude.HeadBuffer{Max: claude.MaxJSONSize + 1}
		forwardInput := io.TeeReader(input, capture)
		forwardCtx, cancel := context.WithTimeout(ctx, *forwardTimeout)
		forwardErr = executeForward(forwardCtx, *forward, forwardInput, output, errorOutput)
		cancel()
		if remaining := int64(capture.Max - capture.Len()); remaining > 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(forwardInput, remaining))
		}
		raw = capture.Bytes()
	} else {
		var err error
		raw, err = io.ReadAll(io.LimitReader(input, claude.MaxJSONSize+1))
		if err != nil {
			return fmt.Errorf("read statusline input: %w", err)
		}
	}
	var ingestErr error
	status, err := claude.ParseStatusline(raw)
	if err == nil {
		var store *claude.Store
		store, err = claude.NewStore(*inboxDir, *bindingID)
		if err == nil {
			_, err = store.Write(status)
		}
	}
	if err != nil {
		ingestErr = fmt.Errorf("write Claude inbox: %w", err)
	}
	if strings.TrimSpace(*forward) == "" {
		return ingestErr
	}
	if ingestErr != nil {
		fmt.Fprintln(errorOutput, ingestErr)
	}
	return forwardErr
}

func forwardCommand(ctx context.Context, command string, input io.Reader, output, errorOutput io.Writer) error {
	var child *exec.Cmd
	if runtime.GOOS == "windows" {
		child = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", command)
	} else {
		child = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	child.Stdin = input
	child.Stdout = output
	child.Stderr = errorOutput
	if err := child.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("existing statusline timed out: %w", ctx.Err())
		}
		return fmt.Errorf("existing statusline: %w", err)
	}
	return nil
}
