// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package main

import "context"

// runControlSurface keeps non-Windows builds headless. The caller owns the
// HTTP server and supplies its complete lifecycle through serve.
func runControlSurface(ctx context.Context, _ string, serve func(context.Context) error) error {
	return serve(ctx)
}
