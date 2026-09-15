// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
)

const (
	profileJSONLimit   = 64 << 10
	profileOutputLimit = 16 << 10
	profileSidecar     = ".turzx-statusline.json"
)

// LoginSession owns the short-lived Claude CLI login process.
type LoginSession struct {
	cmd        *exec.Cmd
	executable string
	configDir  string
	cancel     context.CancelFunc
	done       chan struct{}
	procMu     sync.Mutex
	procErr    error
	waitOnce   sync.Once
	waitErr    error
}

// StartLogin starts an interactive Claude login in the supplied dedicated profile.
func StartLogin(ctx context.Context, executable, configDir string) (*LoginSession, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if strings.TrimSpace(executable) == "" {
		return nil, errors.New("empty Claude executable")
	}
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	childCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(childCtx, executable, "auth", "login", "--claudeai")
	cmd.Env = replaceConfigDir(os.Environ(), dir)
	configureProfileProcess(cmd)
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
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start Claude login: %w", err)
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	s := &LoginSession{cmd: cmd, executable: executable, configDir: dir, cancel: cancel, done: make(chan struct{})}
	go func() { s.procMu.Lock(); s.procErr = cmd.Wait(); s.procMu.Unlock(); close(s.done) }()
	return s, nil
}

// Wait waits for login and confirms the dedicated profile is authenticated.
func (s *LoginSession) Wait(ctx context.Context) error {
	if s == nil {
		return errors.New("nil login session")
	}
	if ctx == nil {
		return errors.New("nil context")
	}
	s.waitOnce.Do(func() {
		select {
		case <-s.done:
			s.procMu.Lock()
			err := s.procErr
			s.procMu.Unlock()
			if err != nil {
				s.waitErr = fmt.Errorf("Claude login: %w", err)
				return
			}
		case <-ctx.Done():
			s.cancel()
			<-s.done
			s.waitErr = ctx.Err()
			return
		}
		s.waitErr = verifyLogin(ctx, s.executable, s.configDir)
	})
	return s.waitErr
}

// Close cancels the login and reaps its child process.
func (s *LoginSession) Close() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

// Account identifies the Claude account a profile is signed into. Claude Code's
// statusline payload carries no account identity, so asking the official CLI is
// the only way to notice that the account behind the usage numbers changed.
type Account struct {
	LoggedIn         bool   `json:"loggedIn"`
	Email            string `json:"email"`
	OrgID            string `json:"orgId"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
	ConfigDirectory  string `json:"configDirectory"`
}

// SameAccount reports whether both describe the same signed-in account. A
// logged-out profile never matches, so losing the session ends the generation.
func (a Account) SameAccount(other Account) bool {
	return a.LoggedIn && other.LoggedIn && a.Email == other.Email && a.OrgID == other.OrgID
}

// Label names the account for display without inventing one when the CLI is terse.
func (a Account) Label() string {
	switch {
	case a.Email != "" && a.OrgName != "":
		return a.Email + " · " + a.OrgName
	case a.Email != "":
		return a.Email
	default:
		return ""
	}
}

// Status reports the account a profile is signed into, using the official CLI.
func Status(ctx context.Context, executable, configDir string) (Account, error) {
	if ctx == nil {
		return Account{}, errors.New("nil context")
	}
	if strings.TrimSpace(executable) == "" {
		return Account{}, errors.New("empty Claude executable")
	}
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return Account{}, err
	}
	return authStatus(ctx, executable, dir)
}

func verifyLogin(ctx context.Context, executable, configDir string) error {
	account, err := authStatus(ctx, executable, configDir)
	if err == nil && !account.LoggedIn {
		err = errors.New("Claude auth status: loggedIn is false")
	}
	return err
}

func verifyLoggedOut(ctx context.Context, executable, configDir string) error {
	account, err := authStatus(ctx, executable, configDir)
	if err == nil && account.LoggedIn {
		err = errors.New("Claude auth status: loggedIn is true")
	}
	return err
}

// authStatus asks the official CLI which account the profile is signed into.
func authStatus(ctx context.Context, executable, configDir string) (Account, error) {
	cmd := exec.CommandContext(ctx, executable, "auth", "status", "--json")
	cmd.Env = replaceConfigDir(os.Environ(), configDir)
	configureProfileProcess(cmd)
	out, errOut := &HeadBuffer{Max: profileOutputLimit}, &HeadBuffer{Max: profileOutputLimit}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Run(); err != nil {
		return Account{}, fmt.Errorf("Claude auth status: %w", err)
	}
	var account Account
	if err := decodeStrict(json.NewDecoder(bytes.NewReader(out.Bytes())), &account); err != nil {
		return Account{}, fmt.Errorf("Claude auth status JSON: %w", err)
	}
	return account, nil
}

// Logout revokes the dedicated Claude profile and verifies it is logged out.
func Logout(ctx context.Context, executable, configDir string) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if strings.TrimSpace(executable) == "" {
		return errors.New("empty Claude executable")
	}
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "auth", "logout")
	cmd.Env = replaceConfigDir(os.Environ(), dir)
	configureProfileProcess(cmd)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	runErr := cmd.Run()
	if err := verifyLoggedOut(ctx, executable, dir); err == nil {
		return nil
	} else if runErr == nil {
		return err
	}
	return fmt.Errorf("Claude logout: %w", runErr)
}

// HeadBuffer keeps the first Max bytes written and reports the rest as
// written, so a chatty child process never blocks or grows memory.
type HeadBuffer struct {
	Max int
	buf bytes.Buffer
}

func (b *HeadBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if keep := b.Max - b.buf.Len(); keep > 0 {
		if len(p) > keep {
			p = p[:keep]
		}
		b.buf.Write(p)
	}
	return n, nil
}
func (b *HeadBuffer) Bytes() []byte { return b.buf.Bytes() }
func (b *HeadBuffer) Len() int      { return b.buf.Len() }

func absoluteDir(value, name string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("empty %s", name)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be absolute", name)
	}
	return filepath.Clean(value), nil
}

func replaceConfigDir(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	for _, value := range env {
		if key, _, ok := strings.Cut(value, "="); ok && strings.EqualFold(key, "CLAUDE_CONFIG_DIR") {
			continue
		}
		out = append(out, value)
	}
	return append(out, "CLAUDE_CONFIG_DIR="+dir)
}

type managedStatusline struct {
	ManagedCommand     string          `json:"managed_command"`
	HadStatusline      bool            `json:"had_statusline"`
	OriginalStatusline json.RawMessage `json:"original_statusline,omitempty"`
	BindingID          string          `json:"binding_id,omitempty"`
	InboxDir           string          `json:"inbox_dir,omitempty"`
	Confirmed          bool            `json:"confirmed,omitempty"`
	// Account the binding was issued for, so a restart can tell whether the
	// profile has since been signed into a different one.
	AccountEmail string `json:"account_email,omitempty"`
	AccountOrgID string `json:"account_org_id,omitempty"`
}

// InstallStatusline installs the dedicated adapter command while preserving settings.
func InstallStatusline(configDir, adapterPath, inboxDir, bindingID string) error {
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return err
	}
	adapter, err := absoluteDir(adapterPath, "adapter path")
	if err != nil {
		return err
	}
	info, err := os.Stat(adapter)
	if err != nil {
		return fmt.Errorf("statusline adapter: %w", err)
	}
	if info.IsDir() {
		return errors.New("statusline adapter is a directory")
	}
	inbox, err := absoluteDir(inboxDir, "inbox directory")
	if err != nil {
		return err
	}
	if err := validID(bindingID, "binding_id"); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readSettings(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	managed := filepath.Join(dir, profileSidecar)
	var side managedStatusline
	if existing, e := readManagedStatuslineFile(managed); e == nil {
		side = existing
	} else if !os.IsNotExist(e) {
		return e
	}
	var original json.RawMessage
	lineObject := map[string]json.RawMessage{}
	if raw, ok := settings["statusLine"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			original = append(json.RawMessage(nil), raw...)
		} else {
			if err := json.Unmarshal(raw, &lineObject); err != nil {
				return fmt.Errorf("statusLine: %w", err)
			}
			var lineType, lineCommand string
			_ = json.Unmarshal(lineObject["type"], &lineType)
			_ = json.Unmarshal(lineObject["command"], &lineCommand)
			if lineCommand == side.ManagedCommand && side.ManagedCommand != "" {
				original = append(json.RawMessage(nil), side.OriginalStatusline...)
			} else {
				original = append(json.RawMessage(nil), raw...)
			}
			if lineType != "" && lineType != "command" {
				return fmt.Errorf("unsupported statusLine type %q", lineType)
			}
		}
	}
	forward := ""
	if len(original) != 0 {
		var originalObject map[string]json.RawMessage
		if err := json.Unmarshal(original, &originalObject); err != nil {
			return fmt.Errorf("original statusLine: %w", err)
		}
		_ = json.Unmarshal(originalObject["command"], &forward)
	}
	command := buildAdapterCommand(adapter, inbox, bindingID, forward)
	lineObject["type"], _ = json.Marshal("command")
	lineObject["command"], _ = json.Marshal(command)
	line, _ := json.Marshal(lineObject)
	settings["statusLine"] = line
	data, _ := json.MarshalIndent(settings, "", "  ")
	data = append(data, '\n')
	sideData, _ := json.Marshal(managedStatusline{
		ManagedCommand:     command,
		HadStatusline:      len(original) != 0,
		OriginalStatusline: original,
		BindingID:          bindingID,
		InboxDir:           inbox,
		Confirmed:          false,
	})
	if err := atomicReplace(managed, sideData); err != nil {
		return err
	}
	return atomicReplace(settingsPath, data)
}

// UninstallStatusline restores the command previously replaced by InstallStatusline.
// It is intentionally a no-op when the current command is no longer ours.
func UninstallStatusline(configDir string) error {
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return err
	}
	sideData, err := readManagedStatuslineFile(filepath.Join(dir, profileSidecar))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	side := sideData
	if side.ManagedCommand == "" {
		return errors.New("invalid statusline sidecar")
	}
	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readSettings(settingsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, ok := settings["statusLine"]
	if !ok {
		return nil
	}
	line := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &line); err != nil {
		return nil
	}
	var current string
	_ = json.Unmarshal(line["command"], &current)
	if current != side.ManagedCommand {
		return nil
	}
	if !side.HadStatusline {
		delete(settings, "statusLine")
	} else {
		if !json.Valid(side.OriginalStatusline) {
			return errors.New("invalid original statusLine")
		}
		settings["statusLine"] = append(json.RawMessage(nil), side.OriginalStatusline...)
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	b = append(b, '\n')
	return atomicReplace(settingsPath, b)
}

func readManagedStatuslineFile(path string) (managedStatusline, error) {
	f, err := os.Open(path)
	if err != nil {
		return managedStatusline{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, profileOutputLimit+1))
	if err != nil {
		return managedStatusline{}, err
	}
	if len(b) > profileOutputLimit {
		return managedStatusline{}, errors.New("statusline sidecar too large")
	}
	var side managedStatusline
	if err := decodeStrict(json.NewDecoder(bytes.NewReader(b)), &side); err != nil {
		return managedStatusline{}, errors.New("invalid statusline sidecar")
	}
	return side, nil
}

func readSettings(path string) (map[string]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, profileJSONLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > profileJSONLimit {
		return nil, errors.New("settings.json too large")
	}
	var m map[string]json.RawMessage
	if err := decodeStrict(json.NewDecoder(bytes.NewReader(b)), &m); err != nil {
		return nil, fmt.Errorf("settings.json: %w", err)
	}
	if m == nil {
		return nil, errors.New("settings.json must be a JSON object")
	}
	return m, nil
}

func buildAdapterCommand(adapter, inbox, binding, forward string) string {
	args := []string{"-inbox-dir", inbox, "-binding-id", binding}
	if forward != "" {
		args = append(args, "-forward-command", forward)
	}
	if isWindows() {
		script := "& " + psQuote(adapter)
		for i := 0; i < len(args); i += 2 {
			script += " " + psQuote(args[i]) + " " + psQuote(args[i+1])
		}
		u := utf16LE(script)
		return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(u)
	}
	parts := []string{shQuote(adapter)}
	for _, a := range args {
		parts = append(parts, shQuote(a))
	}
	return strings.Join(parts, " ")
}
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, x := range u {
		binary.LittleEndian.PutUint16(b[i*2:], x)
	}
	return b
}
