// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"path/filepath"
	"testing"
)

func TestLiveDataConfigRequiresDedicatedInputs(t *testing.T) {
	base := liveDataConfig{
		CodexBin: "codex", CodexHome: filepath.Join(t.TempDir(), "codex"),
		ClaudeInboxDir: filepath.Join(t.TempDir(), "inbox"), ClaudeBinding: "binding-1",
	}
	if err := base.validate("azure-ribbon"); err != nil {
		t.Fatal(err)
	}
	if err := base.validate(""); err == nil {
		t.Fatal("accepted live data without a theme")
	}
	for name, mutate := range map[string]func(*liveDataConfig){
		"codex home":    func(c *liveDataConfig) { c.CodexHome = "" },
		"relative home": func(c *liveDataConfig) { c.CodexHome = "codex" },
		"claude dir":    func(c *liveDataConfig) { c.ClaudeInboxDir = "" },
		"binding":       func(c *liveDataConfig) { c.ClaudeBinding = "" },
		"selection":     func(c *liveDataConfig) { c.Selection.GPUUsageSensor = "/gpu/0/load/0" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := cfg.validate("azure-ribbon"); err == nil {
				t.Fatalf("accepted invalid config %q", name)
			}
		})
	}
}
