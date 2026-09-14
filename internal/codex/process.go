// SPDX-License-Identifier: GPL-3.0-or-later

// Package codex reads usage data from a dedicated Codex App Server profile.
package codex

import (
	"bufio"
	"bytes"
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

const (
	maxStdoutLine  = 2 << 20
	maxStderrTail  = 4 << 10
	startupTimeout = 10 * time.Second
)

var (
	errInvalidStart    = errors.New("invalid Codex app-server configuration")
	errProcessStopped  = errors.New("Codex app-server stopped")
	errInvalidResponse = errors.New("invalid Codex app-server response")
)

type wireMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Err    error
}

type response struct {
	result     json.RawMessage
	wireError  json.RawMessage
	receivedAt time.Time
}

// Process owns one Codex app-server child and serializes its requests.
// The stdout and stderr pipes are owned here rather than by os/exec so that
// cmd.Wait can never close them underneath the reader goroutines.
type Process struct {
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout, stderr *os.File
	cancel         context.CancelFunc

	messages  chan wireMessage
	updates   chan struct{}
	logins    chan loginCompletion
	requestMu sync.Mutex
	nextID    uint64 // protected by requestMu
	closeOnce sync.Once
	closeDone chan struct{}
	waitDone  chan struct{}
	workers   sync.WaitGroup
	stderrLog tailBuffer

	expected account // set once by Start before any concurrent use
}

// Start starts executable with --stdio and an explicit, absolute CODEX_HOME.
// The shell is never involved.
func Start(ctx context.Context, executable, home string) (*Process, error) {
	p, err := startProcess(ctx, executable, home)
	if err != nil {
		return nil, err
	}

	startupCtx, startupCancel := context.WithTimeout(ctx, startupTimeout)
	defer startupCancel()
	expected, err := p.readAccount(startupCtx)
	if err != nil {
		p.Close()
		return nil, err
	}
	p.expected = expected
	return p, nil
}

func startProcess(ctx context.Context, executable, home string) (*Process, error) {
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return nil, errInvalidStart
	}
	absoluteHome := filepath.Clean(home)

	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, executable, "app-server", "--stdio")
	cmd.Env = replaceCodexHome(os.Environ(), absoluteHome)
	configureCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		cancel()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	if err := cmd.Start(); err != nil {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		cancel()
		return nil, err
	}
	stdoutW.Close()
	stderrW.Close()

	p := &Process{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdoutR,
		stderr:    stderrR,
		cancel:    cancel,
		messages:  make(chan wireMessage, 16),
		updates:   make(chan struct{}, 1),
		logins:    make(chan loginCompletion, 2),
		closeDone: make(chan struct{}),
		waitDone:  make(chan struct{}),
	}
	p.workers.Add(2)
	go p.readStdout()
	go p.readStderr()
	go func() { _ = cmd.Wait(); close(p.waitDone) }()

	startupCtx, startupCancel := context.WithTimeout(ctx, startupTimeout)
	defer startupCancel()
	if _, err := p.request(startupCtx, "initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "turzx-control",
			"title":   "TURZX Control",
			"version": "dev",
		},
	}); err != nil {
		p.Close()
		return nil, err
	}
	if err := p.notify("initialized"); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// Updates is signaled when the app-server reports a usage or account update.
func (p *Process) Updates() <-chan struct{} { return p.updates }

// Close terminates the child and waits for all I/O goroutines to finish.
func (p *Process) Close() {
	p.closeOnce.Do(func() {
		p.cancel()
		_ = p.stdin.Close()
		close(p.closeDone)
		<-p.waitDone
		// The child is gone; closing our pipe ends releases readers even if a
		// grandchild inherited the write ends. Join them before closing Updates
		// so they cannot signal a closed channel.
		_ = p.stdout.Close()
		_ = p.stderr.Close()
		p.workers.Wait()
		close(p.updates)
	})
}

// stopped describes an exited child, with its stderr tail when there is one.
func (p *Process) stopped() error {
	if tail := strings.TrimSpace(p.stderrLog.String()); tail != "" {
		return fmt.Errorf("%w: %s", errProcessStopped, tail)
	}
	return errProcessStopped
}

func (p *Process) request(ctx context.Context, method string, params any) (response, error) {
	return p.requestWithPolicy(ctx, method, params, false)
}

func (p *Process) requestAllowError(ctx context.Context, method string, params any) (response, error) {
	return p.requestWithPolicy(ctx, method, params, true)
}

// requestWithPolicy sends one request and waits for its reply. A ctx deadline
// abandons only this request: the child keeps running and a late reply is
// discarded by the id check on the next request.
func (p *Process) requestWithPolicy(ctx context.Context, method string, params any, allowError bool) (response, error) {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	p.nextID++
	id := p.nextID

	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return response{}, err
	}
	payload = append(payload, '\n')
	if _, err := p.stdin.Write(payload); err != nil {
		return response{}, p.stopped()
	}
	for {
		select {
		case <-ctx.Done():
			return response{}, ctx.Err()
		case <-p.closeDone:
			return response{}, errProcessStopped
		case msg, ok := <-p.messages:
			if !ok {
				return response{}, p.stopped()
			}
			if msg.Err != nil {
				return response{}, msg.Err
			}
			if !sameID(msg.ID, id) {
				continue
			}
			if len(msg.Error) != 0 && string(msg.Error) != "null" {
				if allowError {
					return response{wireError: msg.Error, receivedAt: time.Now()}, nil
				}
				return response{}, errInvalidResponse
			}
			if len(msg.Result) == 0 || string(msg.Result) == "null" {
				return response{}, errInvalidResponse
			}
			return response{result: msg.Result, receivedAt: time.Now()}, nil
		}
	}
}

func (p *Process) notify(method string) error {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	payload, err := json.Marshal(map[string]any{"method": method, "params": map[string]any{}})
	if err != nil {
		return err
	}
	if _, err := p.stdin.Write(append(payload, '\n')); err != nil {
		return p.stopped()
	}
	return nil
}

func (p *Process) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "account/rateLimits/updated", "account/updated":
		select {
		case p.updates <- struct{}{}:
		default:
		}
	case "account/login/completed":
		var value struct {
			LoginID string          `json:"loginId"`
			Success bool            `json:"success"`
			Error   json.RawMessage `json:"error"`
		}
		completed := loginCompletion{}
		if json.Unmarshal(params, &value) != nil || strings.TrimSpace(value.LoginID) == "" {
			completed.err = errInvalidResponse
		} else {
			completed.loginID = value.LoginID
			completed.success = value.Success
		}
		select {
		case p.logins <- completed:
		default:
		}
	}
}

// readStdout routes notifications directly and queues replies for the
// request in flight. Notifications never enter the reply queue.
func (p *Process) readStdout() {
	defer p.workers.Done()
	defer close(p.messages)
	r := bufio.NewReader(p.stdout)
	for {
		line, err := readLineLimit(r, maxStdoutLine)
		if len(line) != 0 {
			var msg wireMessage
			if decodeErr := json.Unmarshal(bytes.TrimSpace(line), &msg); decodeErr != nil {
				p.sendMessage(wireMessage{Err: errInvalidResponse})
				return
			}
			if msg.Method != "" && !hasID(msg.ID) {
				p.handleNotification(msg.Method, msg.Params)
			} else {
				p.sendMessage(msg)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				p.sendMessage(wireMessage{Err: errInvalidResponse})
			}
			return
		}
	}
}

func (p *Process) readStderr() {
	defer p.workers.Done()
	_, _ = io.Copy(&p.stderrLog, p.stderr)
}

func (p *Process) sendMessage(msg wireMessage) {
	select {
	case p.messages <- msg:
	case <-p.closeDone:
	}
}

func readLineLimit(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line) > limit-len(part) {
			return nil, errInvalidResponse
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil || len(part) == 0 || part[len(part)-1] == '\n' {
			return line, err
		}
	}
}

func hasID(id json.RawMessage) bool { return len(id) != 0 && string(id) != "null" }

func sameID(raw json.RawMessage, id uint64) bool {
	var n uint64
	if err := json.Unmarshal(raw, &n); err != nil {
		return false
	}
	return n == id
}

func replaceCodexHome(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, value := range env {
		if key, _, found := strings.Cut(value, "="); found && strings.EqualFold(key, "CODEX_HOME") {
			continue
		}
		out = append(out, value)
	}
	return append(out, "CODEX_HOME="+home)
}

// tailBuffer keeps the last maxStderrTail bytes written.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > maxStderrTail {
		t.b = append([]byte(nil), t.b[len(t.b)-maxStderrTail:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

func readString(raw json.RawMessage, dst *string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	return json.Unmarshal(raw, dst) == nil
}
