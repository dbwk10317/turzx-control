// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type settings struct {
	ListenAddress   string           `json:"listen"`
	CodexBin        string           `json:"codex-bin"`
	CodexHome       string           `json:"codex-home"`
	ClaudeBin       string           `json:"claude-bin"`
	ClaudeConfigDir string           `json:"claude-config-dir"`
	ClaudeStatusBin string           `json:"claude-status-bin"`
	ClaudeInboxDir  string           `json:"claude-inbox-dir"`
	Display         *displaySettings `json:"display,omitempty"`
}

func settingsPath(configDir string) string {
	return filepath.Join(configDir, "turzx-control", "settings.json")
}

func loadSettings(path string) (settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings{}, nil
	}
	if err != nil {
		return settings{}, fmt.Errorf("read settings: %w", err)
	}
	var value settings
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return settings{}, fmt.Errorf("decode settings: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return settings{}, errors.New("settings contain trailing JSON")
		}
		return settings{}, fmt.Errorf("decode settings trailing JSON: %w", err)
	}
	return value, nil
}

func saveSettings(path string, value settings) error {
	var err error
	value, err = normalizedSettings(value)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create settings temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := replaceSettingsFile(tmpPath, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

func normalizedSettings(value settings) (settings, error) {
	normalizeRequired := func(field, name string) (string, error) {
		return normalizeAbsolutePath(strings.TrimSpace(field), name)
	}
	normalizeExecutable := func(field, name string) (string, error) {
		return normalizeExecutablePath(strings.TrimSpace(field), name)
	}
	listen, err := normalizedListenAddress(value.ListenAddress)
	if err != nil {
		return settings{}, err
	}
	codexBin, err := normalizeExecutable(value.CodexBin, "codex executable")
	if err != nil {
		return settings{}, err
	}
	codexHome, err := normalizeRequired(value.CodexHome, "codex profile")
	if err != nil {
		return settings{}, err
	}
	claudeBin, err := normalizeExecutable(value.ClaudeBin, "claude executable")
	if err != nil {
		return settings{}, err
	}
	claudeConfigDir, err := normalizeRequired(value.ClaudeConfigDir, "claude profile")
	if err != nil {
		return settings{}, err
	}
	claudeStatusBin, err := normalizeRequired(value.ClaudeStatusBin, "statusline adapter")
	if err != nil {
		return settings{}, err
	}
	claudeInboxDir, err := normalizeRequired(value.ClaudeInboxDir, "claude inbox directory")
	if err != nil {
		return settings{}, err
	}
	var normalizedDisplay *displaySettings
	if value.Display != nil {
		normalized, err := normalizeDisplaySettings(*value.Display)
		if err != nil {
			return settings{}, err
		}
		normalizedDisplay = &normalized
	}
	return settings{
		ListenAddress:   listen,
		CodexBin:        codexBin,
		CodexHome:       codexHome,
		ClaudeBin:       claudeBin,
		ClaudeConfigDir: claudeConfigDir,
		ClaudeStatusBin: claudeStatusBin,
		ClaudeInboxDir:  claudeInboxDir,
		Display:         normalizedDisplay,
	}, nil
}

func normalizedListenAddress(value string) (string, error) {
	address := strings.TrimSpace(value)
	if address == "" {
		return "", errors.New("listen address is empty")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("listen address: %w", err)
	}
	if host != "127.0.0.1" {
		return "", errors.New("listen address must use 127.0.0.1")
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return "", fmt.Errorf("listen port: %w", err)
	}
	if n < 0 || n > 65535 {
		return "", fmt.Errorf("listen port out of range: %q", port)
	}
	return "127.0.0.1:" + strconv.Itoa(n), nil
}

func normalizeAbsolutePath(value, name string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return filepath.Clean(abs), nil
}

func normalizeExecutablePath(value, name string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	if !strings.ContainsAny(value, `/\`) {
		return value, nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return filepath.Clean(abs), nil
}
