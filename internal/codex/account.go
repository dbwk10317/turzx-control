// SPDX-License-Identifier: GPL-3.0-or-later

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	errAccountIdentity  = errors.New("Codex account identity unavailable")
	errAccountChanged   = errors.New("Codex account changed during usage read")
	errLogoutIncomplete = errors.New("Codex logout did not take effect")
)

// IsAccountChanged reports that the dedicated profile no longer matches the
// account identity captured when the App Server process started.
func IsAccountChanged(err error) bool { return errors.Is(err, errAccountChanged) }

// IsAuthRequired reports that the dedicated profile has no usable account
// identity and must be authenticated again.
func IsAuthRequired(err error) bool { return errors.Is(err, errAccountIdentity) }

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
	return errLogoutIncomplete
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

// parseAccount distinguishes a malformed reply (protocol problem) from a
// reply that is well-formed but names no usable ChatGPT account (login needed).
func parseAccount(raw json.RawMessage) (account, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return account{}, fmt.Errorf("%w: account/read", errInvalidResponse)
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
		return account{}, fmt.Errorf("%w: account/read", errInvalidResponse)
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
