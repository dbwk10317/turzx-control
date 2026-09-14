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
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxStdoutLine      = 2 << 20
	maxStderrTail      = 4 << 10
	startupTimeout     = 10 * time.Second
	loginVerifyTimeout = 10 * time.Second
)

var (
	errInvalidStart     = errors.New("invalid Codex app-server configuration")
	errProcessStopped   = errors.New("Codex app-server stopped")
	errInvalidResponse  = errors.New("invalid Codex app-server response")
	errAccountIdentity  = errors.New("Codex account identity unavailable")
	errAccountChanged   = errors.New("Codex account changed during usage read")
	errRateLimitMissing = errors.New("Codex rate limits unavailable")
	errLoginFailed      = errors.New("Codex login failed")
)

// IsAccountChanged reports that the dedicated profile no longer matches the
// account identity captured when the App Server process started.
func IsAccountChanged(err error) bool { return errors.Is(err, errAccountChanged) }

// IsAuthRequired reports that the dedicated profile has no usable account
// identity and must be authenticated again.
func IsAuthRequired(err error) bool { return errors.Is(err, errAccountIdentity) }

// LoginSession owns a short-lived App Server process while the user completes
// the official ChatGPT browser login flow.
type LoginSession struct {
	process *Process
	loginID string
	authURL string
}

// WindowKind identifies the two windows understood by the first theme.
type WindowKind string

const (
	FiveHour WindowKind = "five_hour"
	Weekly   WindowKind = "weekly"
	Other    WindowKind = "other"
)

// Window is one usage limit window. UsedPercent is retained as returned by
// Codex; RemainingPercent is clamped to the displayable range.
type Window struct {
	Kind               WindowKind `json:"kind"`
	WindowDurationMins int        `json:"window_duration_mins"`
	UsedPercent        int        `json:"used_percent"`
	RemainingPercent   int        `json:"remaining_percent"`
	ResetsAt           time.Time  `json:"resets_at"`
}

// Snapshot is a validated, account-independent usage observation.
// Account email and account ID are intentionally not represented here.
type Snapshot struct {
	Plan       string    `json:"plan"`
	FiveHour   *Window   `json:"five_hour"`
	Weekly     *Window   `json:"weekly"`
	Other      []Window  `json:"other,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

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
type Process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	messages  chan wireMessage
	updates   chan struct{}
	logins    chan loginCompletion
	requestMu sync.Mutex
	closeOnce sync.Once
	closeDone chan struct{}
	waitDone  chan struct{}
	workers   sync.WaitGroup

	stateMu  sync.Mutex
	readErr  error
	nextID   uint64
	stderr   tailBuffer
	expected account
}

type loginCompletion struct {
	loginID string
	success bool
	err     error
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

// StartChatGPTLogin begins the official browser login flow for an explicit
// CODEX_HOME. Credentials remain owned by Codex App Server.
func StartChatGPTLogin(ctx context.Context, executable, home string) (*LoginSession, error) {
	p, err := startProcess(ctx, executable, home)
	if err != nil {
		return nil, err
	}
	loginCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	result, err := p.request(loginCtx, "account/login/start", map[string]any{
		"type":                      "chatgpt",
		"useHostedLoginSuccessPage": true,
		"appBrand":                  "chatgpt",
	})
	if err != nil {
		p.Close()
		return nil, err
	}
	var login struct {
		Type    string `json:"type"`
		LoginID string `json:"loginId"`
		AuthURL string `json:"authUrl"`
	}
	if json.Unmarshal(result.result, &login) != nil || login.Type != "chatgpt" || strings.TrimSpace(login.LoginID) == "" {
		p.Close()
		return nil, fmt.Errorf("%w: malformed login response", errInvalidResponse)
	}
	if !validAuthURL(login.AuthURL) {
		p.Close()
		host := ""
		if parsed, err := url.Parse(login.AuthURL); err == nil {
			host = parsed.Hostname()
		}
		return nil, fmt.Errorf("%w: unsupported login URL host %q", errInvalidResponse, host)
	}
	return &LoginSession{process: p, loginID: login.LoginID, authURL: login.AuthURL}, nil
}

// Logout signs out the account in the explicit dedicated CODEX_HOME profile.
// Credentials are managed by Codex App Server; this method never reads them.
func Logout(ctx context.Context, executable, home string) error {
	p, err := startProcess(ctx, executable, home)
	if err != nil {
		return err
	}
	defer p.Close()

	logoutResult, err := p.requestAllowError(ctx, "account/logout", map[string]any{})
	if err != nil {
		return fmt.Errorf("Codex logout: %w", err)
	}
	result, err := p.requestAllowError(ctx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return fmt.Errorf("Codex logout verification: %w", err)
	}
	if loggedOut(result) {
		return nil
	}
	if len(logoutResult.wireError) != 0 {
		return fmt.Errorf("Codex logout: %w", errInvalidResponse)
	}
	return errAccountIdentity
}

// AuthURL is the official URL the user should open to continue login.
func (s *LoginSession) AuthURL() string { return s.authURL }

// Wait waits for the matching completion notification and verifies that the
// resulting profile exposes a stable ChatGPT account identity.
func (s *LoginSession) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.process.closeDone:
			return errProcessStopped
		case <-s.process.waitDone:
			return errProcessStopped
		case completed := <-s.process.logins:
			if completed.err != nil {
				return completed.err
			}
			if completed.loginID != s.loginID {
				continue
			}
			if !completed.success {
				return errLoginFailed
			}
			verifyCtx, cancel := context.WithTimeout(ctx, loginVerifyTimeout)
			_, err := s.process.readAccount(verifyCtx)
			cancel()
			return err
		}
	}
}

// Close stops the login App Server process.
func (s *LoginSession) Close() { s.process.Close() }

func startProcess(ctx context.Context, executable, home string) (*Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(executable) == "" || strings.TrimSpace(home) == "" || !filepath.IsAbs(home) {
		return nil, errInvalidStart
	}
	absoluteHome := filepath.Clean(home)

	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, executable, "app-server", "--stdio")
	cmd.Env = replaceCodeHome(os.Environ(), absoluteHome)
	configureCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	p := &Process{
		cmd:       cmd,
		stdin:     stdin,
		cancel:    cancel,
		messages:  make(chan wireMessage, 16),
		updates:   make(chan struct{}, 1),
		logins:    make(chan loginCompletion, 2),
		closeDone: make(chan struct{}),
		waitDone:  make(chan struct{}),
	}
	p.workers.Add(2)
	go p.readStdout(stdout)
	go p.readStderr(stderr)
	go func() {
		err := cmd.Wait()
		p.stateMu.Lock()
		if err != nil && p.readErr == nil {
			p.readErr = errProcessStopped
		}
		p.stateMu.Unlock()
		close(p.waitDone)
	}()

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
	if err := p.notify(startupCtx, "initialized"); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// Updates is signaled when the app-server reports a usage or account update.
func (p *Process) Updates() <-chan struct{} { return p.updates }

// Read obtains and validates one bucket's current rate limits.
func (p *Process) Read(ctx context.Context, bucket string) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(bucket) == "" {
		return Snapshot{}, errRateLimitMissing
	}

	before, err := p.readAccount(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !before.sameIdentity(p.expected) {
		return Snapshot{}, errAccountChanged
	}
	rate, err := p.request(ctx, "account/rateLimits/read", map[string]any{
		"excludeResetCreditDetails": true,
	})
	if err != nil {
		return Snapshot{}, err
	}
	after, err := p.readAccount(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !after.sameIdentity(p.expected) || !before.sameIdentity(after) {
		return Snapshot{}, errAccountChanged
	}

	snapshot, err := parseRateLimits(rate.result, bucket, after.plan, rate.receivedAt)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// Close terminates the child and waits for all I/O goroutines to finish.
func (p *Process) Close() {
	p.closeOnce.Do(func() {
		p.cancel()
		_ = p.stdin.Close()
		close(p.closeDone)
		<-p.waitDone
		// CommandContext kills the child when cancellation is observed. Join the
		// readers before closing Updates so they cannot signal a closed channel.
		p.workers.Wait()
		close(p.updates)
		p.stateMu.Lock()
		if p.readErr == nil {
			p.readErr = errProcessStopped
		}
		p.stateMu.Unlock()
	})
}

func (p *Process) request(ctx context.Context, method string, params any) (response, error) {
	return p.requestWithPolicy(ctx, method, params, false)
}

func (p *Process) requestAllowError(ctx context.Context, method string, params any) (response, error) {
	return p.requestWithPolicy(ctx, method, params, true)
}

func (p *Process) requestWithPolicy(ctx context.Context, method string, params any, allowError bool) (response, error) {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	p.stateMu.Lock()
	p.nextID++
	id := p.nextID
	p.stateMu.Unlock()

	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return response{}, err
	}
	payload = append(payload, '\n')
	stopCancel := context.AfterFunc(ctx, p.cancel)
	defer stopCancel()
	if _, err := p.stdin.Write(payload); err != nil {
		return response{}, errProcessStopped
	}
	for {
		select {
		case <-ctx.Done():
			return response{}, ctx.Err()
		case <-p.closeDone:
			return response{}, errProcessStopped
		case msg, ok := <-p.messages:
			if !ok {
				return response{}, errProcessStopped
			}
			if msg.Err != nil {
				return response{}, msg.Err
			}
			if msg.Method != "" && !hasID(msg.ID) {
				p.handleNotification(msg.Method, msg.Params)
				continue
			}
			if !hasID(msg.ID) || !sameID(msg.ID, id) {
				continue
			}
			if len(msg.Error) != 0 && string(msg.Error) != "null" {
				if allowError {
					if !stopCancel() || ctx.Err() != nil {
						return response{}, ctx.Err()
					}
					return response{wireError: msg.Error, receivedAt: time.Now()}, nil
				}
				return response{}, errInvalidResponse
			}
			if len(msg.Result) == 0 || string(msg.Result) == "null" {
				return response{}, errInvalidResponse
			}
			if !stopCancel() || ctx.Err() != nil {
				return response{}, ctx.Err()
			}
			return response{result: msg.Result, receivedAt: time.Now()}, nil
		}
	}
}

func (p *Process) notify(ctx context.Context, method string) error {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	payload, err := json.Marshal(map[string]any{"method": method, "params": map[string]any{}})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	stopCancel := context.AfterFunc(ctx, p.cancel)
	defer stopCancel()
	if _, err := p.stdin.Write(payload); err != nil {
		return errProcessStopped
	}
	if !stopCancel() || ctx.Err() != nil {
		return ctx.Err()
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

func (p *Process) readStdout(stdout io.Reader) {
	defer p.workers.Done()
	defer close(p.messages)
	r := bufio.NewReader(stdout)
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
				if err != nil {
					return
				}
				continue
			}
			p.sendMessage(msg)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.stateMu.Lock()
				p.readErr = errInvalidResponse
				p.stateMu.Unlock()
				p.sendMessage(wireMessage{Err: errInvalidResponse})
			}
			return
		}
	}
}

func validAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "chatgpt.com" || host == "auth.openai.com"
}

func (p *Process) readStderr(stderr io.Reader) {
	defer p.workers.Done()
	buf := make([]byte, 1024)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			p.stderr.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
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

func replaceCodeHome(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, value := range env {
		if key, _, found := strings.Cut(value, "="); found && strings.EqualFold(key, "CODEX_HOME") {
			continue
		}
		out = append(out, value)
	}
	return append(out, "CODEX_HOME="+home)
}

type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > maxStderrTail {
		t.b = append([]byte(nil), t.b[len(t.b)-maxStderrTail:]...)
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

type account struct {
	typ   string
	email string
	plan  string
}

func (a account) sameIdentity(other account) bool {
	return a.typ == other.typ && a.email == other.email
}

func (p *Process) readAccount(ctx context.Context) (account, error) {
	result, err := p.request(ctx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return account{}, err
	}
	return parseAccount(result.result)
}

func loggedOut(result response) bool {
	if len(result.wireError) != 0 {
		var value struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(result.wireError, &value) != nil {
			return false
		}
		code := strings.ToLower(strings.TrimSpace(value.Code))
		message := strings.ToLower(strings.TrimSpace(value.Message))
		return code == "not_authenticated" || code == "unauthenticated" || strings.Contains(message, "not logged in") || strings.Contains(message, "not authenticated")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(result.result, &root) != nil {
		return false
	}
	accountRaw, ok := root["account"]
	return ok && string(accountRaw) == "null"
}

func parseAccount(raw json.RawMessage) (account, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return account{}, errAccountIdentity
	}
	accountRaw := raw
	if nested, ok := root["account"]; ok {
		accountRaw = nested
	}
	if string(accountRaw) == "null" {
		return account{}, errAccountIdentity
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(accountRaw, &fields); err != nil {
		return account{}, errAccountIdentity
	}
	var typ, email string
	if !readString(fields["type"], &typ) || typ != "chatgpt" || !readString(fields["email"], &email) || strings.TrimSpace(email) == "" {
		return account{}, errAccountIdentity
	}
	var plan string
	if !readString(fields["planType"], &plan) {
		_ = readString(fields["plan"], &plan)
	}
	return account{typ: typ, email: email, plan: plan}, nil
}

func readString(raw json.RawMessage, dst *string) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	return json.Unmarshal(raw, dst) == nil
}

func parseRateLimits(raw json.RawMessage, bucket, plan string, receivedAt time.Time) (Snapshot, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return Snapshot{}, errInvalidResponse
	}
	selected, ok := root["rateLimits"]
	byID, hasByID := root["rateLimitsByLimitId"]
	if hasByID && string(byID) != "null" {
		var limits map[string]json.RawMessage
		if json.Unmarshal(byID, &limits) != nil {
			return Snapshot{}, errRateLimitMissing
		}
		selected, ok = limits[bucket]
		if !ok {
			return Snapshot{}, errRateLimitMissing
		}
	} else if ok {
		var single map[string]json.RawMessage
		if json.Unmarshal(selected, &single) != nil {
			return Snapshot{}, errRateLimitMissing
		}
		if rawID, exists := single["limitId"]; exists && string(rawID) != "null" {
			var limitID string
			if !readString(rawID, &limitID) || limitID != bucket {
				return Snapshot{}, errRateLimitMissing
			}
		}
	}
	if !ok || string(selected) == "null" {
		return Snapshot{}, errRateLimitMissing
	}
	windows, err := parseWindows(selected)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Plan: plan, ReceivedAt: receivedAt}
	for _, window := range windows {
		w := window
		switch w.Kind {
		case FiveHour:
			snapshot.FiveHour = &w
		case Weekly:
			snapshot.Weekly = &w
		default:
			snapshot.Other = append(snapshot.Other, w)
		}
	}
	return snapshot, nil
}

func parseWindows(raw json.RawMessage) ([]Window, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errInvalidResponse
	}
	seen := make(map[int]struct{}, 2)
	windows := make([]Window, 0, 2)
	for _, name := range []string{"primary", "secondary"} {
		value, exists := fields[name]
		if !exists || string(value) == "null" {
			continue
		}
		var item map[string]json.RawMessage
		if json.Unmarshal(value, &item) != nil {
			return nil, errInvalidResponse
		}
		mins, ok := integer(item["windowDurationMins"])
		if !ok || mins <= 0 || mins > math.MaxInt {
			return nil, errInvalidResponse
		}
		duration := int(mins)
		if _, exists := seen[duration]; exists {
			return nil, errInvalidResponse
		}
		seen[duration] = struct{}{}
		used, ok := integer(item["usedPercent"])
		if !ok || used < 0 || used > math.MaxInt {
			return nil, errInvalidResponse
		}
		reset, ok := integer(item["resetsAt"])
		if !ok || len(item["resetsAt"]) == 0 || string(item["resetsAt"]) == "null" {
			return nil, errInvalidResponse
		}
		remaining := int64(100) - used
		if remaining < 0 {
			remaining = 0
		}
		window := Window{
			WindowDurationMins: duration,
			UsedPercent:        int(used),
			RemainingPercent:   int(remaining),
			ResetsAt:           time.Unix(reset, 0).UTC(),
		}
		switch duration {
		case 300:
			window.Kind = FiveHour
		case 10080:
			window.Kind = Weekly
		default:
			window.Kind = Other
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func integer(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil
}
