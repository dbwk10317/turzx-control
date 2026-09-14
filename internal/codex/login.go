// SPDX-License-Identifier: GPL-3.0-or-later

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const loginVerifyTimeout = 10 * time.Second

var errLoginFailed = errors.New("Codex login failed")

// LoginSession owns a short-lived App Server process while the user completes
// the official ChatGPT browser login flow.
type LoginSession struct {
	process *Process
	loginID string
	authURL string
}

type loginCompletion struct {
	loginID string
	success bool
	err     error
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

// AuthURL is the official URL the user should open to continue login.
func (s *LoginSession) AuthURL() string { return s.authURL }

// Wait waits for the matching completion notification and verifies that the
// resulting profile exposes a stable ChatGPT account identity.
func (s *LoginSession) Wait(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.process.closeDone:
			return errProcessStopped
		case <-s.process.waitDone:
			return s.process.stopped()
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

func validAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "chatgpt.com" || host == "auth.openai.com"
}
