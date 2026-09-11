// SPDX-License-Identifier: GPL-3.0-or-later
// Communicates with our LibreHardwareMonitorLib helper; no upstream code copied.

package metric

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxResponseBytes = 2 * 1024 * 1024

// HelperSnapshot records a helper update, not the hardware's original timestamp.
type HelperSnapshot struct {
	ProtocolVersion int            `json:"protocol_version"`
	ObservedAt      *time.Time     `json:"observed_at"`
	DriverInstalled bool           `json:"driver_installed"`
	Elevated        bool           `json:"elevated"`
	Errors          []string       `json:"errors"`
	Sensors         []HelperSensor `json:"sensors"`
}

type HelperSensor struct {
	SensorID     string   `json:"sensor_id"`
	HardwareID   string   `json:"hardware_id"`
	HardwareType string   `json:"hardware_type"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Value        *float64 `json:"value"`
	State        string   `json:"state"`
	ObservedAt   string   `json:"observed_at"`
}

// SensorProcess owns one child and serializes its request/response stream.
type SensorProcess struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *os.File
	responses   chan helperResponse
	done        chan struct{}
	readerDone  chan struct{}
	closed      chan struct{}
	request     chan struct{}
	closeOnce   sync.Once
	ctx         context.Context
	cancel      context.CancelFunc
	stderrLog   *tailBuffer
	firstSample bool // protected by request
}

type helperResponse struct {
	line string
	err  error
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	if len(p) >= b.limit {
		b.buf = append(b.buf[:0], p[len(p)-b.limit:]...)
	} else {
		if keep := b.limit - len(p); len(b.buf) > keep {
			b.buf = append(b.buf[:0], b.buf[len(b.buf)-keep:]...)
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}

func (b *tailBuffer) StringTrimmed() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// StartSensors starts the explicitly selected local executable, without a shell.
func StartSensors(ctx context.Context, helperPath string) (*SensorProcess, error) {
	if strings.TrimSpace(helperPath) == "" {
		return nil, errors.New("missing helper path")
	}
	helperPath, err := filepath.Abs(helperPath)
	if err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, helperPath, "--stdio")
	configureSensorCommand(cmd)
	cmd.WaitDelay = 1500 * time.Millisecond
	log := &tailBuffer{limit: 4096}
	cmd.Stderr = log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	// Own this pipe so cmd.Wait cannot close it before the reader drains stdout.
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		stdin.Close()
		cancel()
		return nil, err
	}
	cmd.Stdout = childStdout
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		childStdout.Close()
		cancel()
		return nil, fmt.Errorf("start sensor helper: %w", err)
	}
	childStdout.Close()
	p := &SensorProcess{
		cmd: cmd, stdin: stdin, stdout: stdout, stderrLog: log,
		ctx: processCtx, cancel: cancel, firstSample: true,
		responses: make(chan helperResponse, 1), done: make(chan struct{}),
		readerDone: make(chan struct{}), closed: make(chan struct{}), request: make(chan struct{}, 1),
	}
	go p.readStdout()
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *SensorProcess) readStdout() {
	defer close(p.readerDone)
	defer close(p.responses)
	scanner := bufio.NewScanner(p.stdout)
	scanner.Buffer(make([]byte, 4096), maxResponseBytes+1)
	for scanner.Scan() {
		select {
		case p.responses <- helperResponse{line: scanner.Text()}:
		case <-p.ctx.Done():
			return
		case <-p.closed:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case p.responses <- helperResponse{err: err}:
		case <-p.ctx.Done():
		case <-p.closed:
		}
	}
}

func (p *SensorProcess) abort() {
	p.cancel()
	p.stdin.Close()
	p.stdout.Close()
}

func (p *SensorProcess) Sample(ctx context.Context) (snapshot HelperSnapshot, err error) {
	select {
	case p.request <- struct{}{}:
		defer func() { <-p.request }()
	case <-ctx.Done():
		return snapshot, ctx.Err()
	case <-p.ctx.Done():
		return snapshot, p.ctx.Err()
	case <-p.closed:
		return snapshot, errors.New("sensor helper closed")
	}
	if p.ctx.Err() != nil {
		p.Close()
		return snapshot, p.ctx.Err()
	}
	select {
	case <-p.closed:
		return snapshot, errors.New("sensor helper closed")
	default:
	}
	timeout := 1500 * time.Millisecond
	if p.firstSample {
		timeout = 10 * time.Second
		p.firstSample = false
	}
	sampleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Also interrupt a blocked pipe write if the child stops consuming requests.
	stopAbort := context.AfterFunc(sampleCtx, p.abort)
	defer stopAbort()
	defer func() {
		if err != nil {
			p.abort()
			p.Close()
		}
	}()
	if sampleCtx.Err() != nil {
		return snapshot, sampleCtx.Err()
	}
	if _, err = io.WriteString(p.stdin, "sample\n"); err != nil {
		return snapshot, fmt.Errorf("write sample request: %w", err)
	}
	select {
	case <-sampleCtx.Done():
		return snapshot, sampleCtx.Err()
	case <-p.ctx.Done():
		return snapshot, p.ctx.Err()
	case <-p.closed:
		return snapshot, errors.New("sensor helper closed")
	case response, open := <-p.responses:
		if sampleCtx.Err() != nil {
			return snapshot, sampleCtx.Err()
		}
		if !open {
			return snapshot, fmt.Errorf("sensor helper exited: %s", p.stderrLog.StringTrimmed())
		}
		if response.err != nil {
			return snapshot, fmt.Errorf("read sensor helper: %w", response.err)
		}
		snapshot, err = parseHelperSnapshot(response.line, p.stderrLog.StringTrimmed())
		if err != nil {
			return snapshot, err
		}
		if !stopAbort() || sampleCtx.Err() != nil {
			return HelperSnapshot{}, sampleCtx.Err()
		}
		return snapshot, nil
	}
}

func parseHelperSnapshot(line, stderr string) (HelperSnapshot, error) {
	var snapshot HelperSnapshot
	if len(line) > maxResponseBytes {
		return snapshot, errors.New("sensor helper response exceeds 2 MiB")
	}
	if err := json.Unmarshal([]byte(line), &snapshot); err != nil {
		return snapshot, fmt.Errorf("parse helper response: %w", err)
	}
	if snapshot.ProtocolVersion != 1 {
		return snapshot, fmt.Errorf("unsupported helper protocol version %d", snapshot.ProtocolVersion)
	}
	if snapshot.ObservedAt == nil || snapshot.ObservedAt.IsZero() {
		return snapshot, errors.New("missing or invalid helper observed_at")
	}
	if snapshot.Sensors == nil {
		return snapshot, errors.New("missing helper sensors array")
	}
	if stderr != "" {
		snapshot.Errors = append(snapshot.Errors, stderr)
	}
	return snapshot, nil
}

// Close gives the helper a short EOF grace period, then kills and joins it.
func (p *SensorProcess) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.stdin.Close()
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
		}
		p.abort()
		<-p.done
		<-p.readerDone
	})
}
