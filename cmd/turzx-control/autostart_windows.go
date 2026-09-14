// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const autostartValueName = "TURZXControl"

func handleAutostart(args []string) (bool, error) {
	index := -1
	for i, arg := range args {
		if arg == "-autostart" || strings.HasPrefix(arg, "-autostart=") {
			index = i
			break
		}
	}
	if index < 0 {
		return false, nil
	}
	if index != 0 || len(args) != 2 || args[0] != "-autostart" {
		return true, errors.New("autostart syntax: -autostart enable|disable|status")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return true, err
	}
	switch args[1] {
	case "enable":
		return true, enableAutostart(configDir)
	case "disable":
		return true, disableAutostart(defaultAutostartRoot, "")
	case "status":
		return true, statusAutostart(defaultAutostartRoot)
	default:
		return true, errors.New("autostart syntax: -autostart enable|disable|status")
	}
}

const defaultAutostartRoot = "Software\\Microsoft\\Windows\\CurrentVersion\\Run"

func enableAutostart(configDir string) error {
	path := settingsPath(configDir)
	loaded, err := loadSettings(path)
	if err != nil {
		return fmt.Errorf("validate settings for autostart: %w", err)
	}
	// A fresh install has no settings.json; the daemon then runs on compiled
	// defaults, so only a saved file needs to validate.
	if loaded != (settings{}) {
		if _, err := normalizedSettings(loaded); err != nil {
			return fmt.Errorf("validate settings for autostart: %w", err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	if err := validateAutostartExecutable(executable); err != nil {
		return err
	}
	return enableAutostartAt(configDir, defaultAutostartRoot, executable)
}

func enableAutostartAt(configDir, root, executable string) error {
	command := syscall.EscapeArg(executable)
	if len(command) > 260 {
		return errors.New("autostart command exceeds Windows 260-character limit")
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, root, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open user Run key: %w", err)
	}
	defer key.Close()
	if current, _, err := key.GetStringValue(autostartValueName); err == nil {
		if current != "" && current != command {
			return fmt.Errorf("autostart value is owned by another executable; disable it before enabling this one")
		}
	} else if !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("read existing autostart value: %w", err)
	}
	if err := key.SetStringValue(autostartValueName, command); err != nil {
		return fmt.Errorf("set autostart value: %w", err)
	}
	fmt.Println("autostart enabled")
	return nil
}

func disableAutostart(root, executable string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, root, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		fmt.Println("autostart disabled")
		return nil
	}
	if err != nil {
		return fmt.Errorf("open user Run key: %w", err)
	}
	defer key.Close()
	current, _, err := key.GetStringValue(autostartValueName)
	if errors.Is(err, registry.ErrNotExist) {
		fmt.Println("autostart disabled")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read autostart value: %w", err)
	}
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return err
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return err
		}
		executable = syscall.EscapeArg(executable)
	}
	if current != executable {
		return errors.New("autostart value belongs to another executable; refusing to remove it")
	}
	if err := key.DeleteValue(autostartValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("remove autostart value: %w", err)
	}
	fmt.Println("autostart disabled")
	return nil
}

func statusAutostart(root string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, root, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		fmt.Println("autostart disabled")
		return nil
	}
	if err != nil {
		return fmt.Errorf("open user Run key: %w", err)
	}
	defer key.Close()
	value, _, err := key.GetStringValue(autostartValueName)
	if errors.Is(err, registry.ErrNotExist) {
		fmt.Println("autostart disabled")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read autostart value: %w", err)
	}
	fmt.Printf("autostart enabled: %s\n", value)
	return nil
}

func validateAutostartExecutable(path string) error {
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(temp, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("refusing temporary executable for autostart")
	}
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if strings.HasPrefix(strings.ToLower(part), "go-build") {
			return errors.New("refusing go-build executable for autostart")
		}
	}
	return nil
}
