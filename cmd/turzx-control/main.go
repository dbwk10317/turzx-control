// SPDX-License-Identifier: GPL-3.0-or-later

// turzx-control hosts the local control surface for the TURZX daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) (runErr error) {
	if handled, err := handleAutostart(args); handled {
		return err
	}
	ctx, cancelApp := context.WithCancel(ctx)
	defer cancelApp()
	flags := flag.NewFlagSet("turzx-control", flag.ContinueOnError)
	listenAddress := flags.String("listen", "127.0.0.1:0", "local settings UI address")
	codexBin := flags.String("codex-bin", "codex", "path to the Codex CLI executable")
	codexHome := flags.String("codex-home", "", "dedicated CODEX_HOME managed by turzx-control")
	claudeBin := flags.String("claude-bin", "claude", "path to the Claude Code executable")
	claudeConfigDir := flags.String("claude-config-dir", "", "dedicated CLAUDE_CONFIG_DIR managed by turzx-control")
	claudeStatusBin := flags.String("claude-status-bin", "", "path to the turzx-claude-status executable")
	claudeInboxDir := flags.String("claude-inbox-dir", "", "dedicated Claude statusline inbox directory")
	saveConfig := flags.Bool("save-config", false, "save noncredential settings before serving")
	configDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	loaded, err := loadSettings(settingsPath(configDir))
	if err != nil {
		return err
	}
	readDisplay := displayFlags(flags, loaded.Display)
	applySetting := func(value *string, saved string) {
		if strings.TrimSpace(saved) != "" {
			*value = saved
		}
	}
	applySetting(listenAddress, loaded.ListenAddress)
	applySetting(codexBin, loaded.CodexBin)
	applySetting(codexHome, loaded.CodexHome)
	applySetting(claudeBin, loaded.ClaudeBin)
	applySetting(claudeConfigDir, loaded.ClaudeConfigDir)
	applySetting(claudeStatusBin, loaded.ClaudeStatusBin)
	applySetting(claudeInboxDir, loaded.ClaudeInboxDir)
	if strings.TrimSpace(*codexHome) == "" {
		*codexHome = filepath.Join(configDir, "turzx-control", "codex")
	}
	if strings.TrimSpace(*claudeConfigDir) == "" {
		*claudeConfigDir = filepath.Join(configDir, "turzx-control", "claude")
	}
	if strings.TrimSpace(*claudeInboxDir) == "" {
		*claudeInboxDir = filepath.Join(configDir, "turzx-control", "inbox", "claude", runtime.GOOS, "default")
	}
	if strings.TrimSpace(*claudeStatusBin) == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		name := "turzx-claude-status"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		*claudeStatusBin = filepath.Join(filepath.Dir(executable), name)
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	display, err := readDisplay()
	if err != nil {
		return err
	}
	value, err := normalizedSettings(settings{
		ListenAddress: *listenAddress, CodexBin: *codexBin, CodexHome: *codexHome,
		ClaudeBin: *claudeBin, ClaudeConfigDir: *claudeConfigDir,
		ClaudeStatusBin: *claudeStatusBin, ClaudeInboxDir: *claudeInboxDir, Display: display,
	})
	if err != nil {
		return err
	}
	*listenAddress, *codexBin, *codexHome = value.ListenAddress, value.CodexBin, value.CodexHome
	*claudeBin, *claudeConfigDir = value.ClaudeBin, value.ClaudeConfigDir
	*claudeStatusBin, *claudeInboxDir = value.ClaudeStatusBin, value.ClaudeInboxDir
	instance, err := acquireInstance(configDir)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := os.MkdirAll(*codexHome, 0o700); err != nil {
		return fmt.Errorf("create dedicated Codex profile: %w", err)
	}
	if err := os.MkdirAll(*claudeConfigDir, 0o700); err != nil {
		return fmt.Errorf("create dedicated Claude profile: %w", err)
	}
	if err := os.MkdirAll(*claudeInboxDir, 0o700); err != nil {
		return fmt.Errorf("create dedicated Claude inbox: %w", err)
	}
	if *saveConfig {
		if err := saveSettings(settingsPath(configDir), value); err != nil {
			return err
		}
	}

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddress.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		return errors.New("settings UI must listen on 127.0.0.1")
	}
	host := listener.Addr().String()
	app, err := newApp(ctx, host, *codexBin, *codexHome, *claudeBin, *claudeConfigDir, *claudeStatusBin, *claudeInboxDir)
	if err != nil {
		return err
	}
	closeRuntime := app.startRuntime(ctx, display)
	defer func() { runErr = errors.Join(runErr, closeRuntime()) }()
	server := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
	}

	url := "http://" + host
	fmt.Printf("TURZX Control: %s\n", url)
	return runControlSurface(ctx, url, func(serveCtx context.Context) error {
		defer app.close(cancelApp)
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		select {
		case <-serveCtx.Done():
			cancelApp()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := server.Shutdown(shutdownCtx)
			if err != nil {
				_ = server.Close()
			}
			return err
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	})
}
