// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	instanceChildEnv   = "TURZX_INSTANCE_CHILD"
	instanceRootEnv    = "TURZX_INSTANCE_ROOT"
	instanceReadyEnv   = "TURZX_INSTANCE_READY"
	instanceChildValue = "acquire-lock"
)

func TestAcquireInstanceDuplicateProcess(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, "turzx-control", "instance.lock")
	readyPath := filepath.Join(t.TempDir(), "turzx-instance-ready")
	var output bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=TestAcquireInstanceChild")
	cmd.Env = append(os.Environ(),
		instanceChildEnv+"="+instanceChildValue,
		instanceRootEnv+"="+root,
		instanceReadyEnv+"="+readyPath,
	)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		if cmd.Process == nil || cmd.ProcessState != nil {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	t.Cleanup(cleanup)

	if !waitForFile(readyPath, 3*time.Second) {
		cleanup()
		t.Fatalf("child failed to start lock holder: %s", output.String())
	}
	lock, err := acquireInstance(root)
	if err == nil {
		_ = lock.Close()
		cleanup()
		t.Fatalf("acquireInstance succeeded while child holds lock: %s", output.String())
	}
	if !strings.Contains(strings.ToLower(err.Error()), "already running") {
		t.Fatalf("unexpected lock error: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("instance lock file missing: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()

	lock, err = acquireInstance(root)
	if err != nil {
		t.Fatalf("reacquire after child exit: %v", err)
	}
	_ = lock.Close()
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should persist after release: %v", err)
	}
}

func TestAcquireInstanceChild(t *testing.T) {
	if os.Getenv(instanceChildEnv) != instanceChildValue {
		return
	}
	root := os.Getenv(instanceRootEnv)
	lock, err := acquireInstance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	readyPath := os.Getenv(instanceReadyEnv)
	if readyPath == "" {
		t.Fatal("missing TURZX_INSTANCE_READY")
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Second)
}

func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
