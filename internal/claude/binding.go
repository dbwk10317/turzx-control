// SPDX-License-Identifier: GPL-3.0-or-later
package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ConfirmStatusline records that the installed adapter belongs to a successful
// login after the caller has verified authentication through Claude.
func ConfirmStatusline(configDir, bindingID string, account Account) error {
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return err
	}
	if err := validID(bindingID, "binding_id"); err != nil {
		return err
	}

	path := filepath.Join(dir, profileSidecar)
	side, err := readManagedStatuslineFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("statusline sidecar not found")
		}
		return err
	}
	if side.ManagedCommand == "" {
		return errors.New("invalid statusline sidecar")
	}
	if side.BindingID != bindingID || validID(side.BindingID, "binding_id") != nil {
		return errors.New("statusline binding mismatch")
	}
	if _, err := absoluteDir(side.InboxDir, "inbox directory"); err != nil {
		return fmt.Errorf("statusline sidecar: %w", err)
	}

	settings, err := readSettings(filepath.Join(dir, "settings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("statusline settings not found")
		}
		return err
	}
	current, ok := currentStatuslineCommand(settings)
	if !ok || current != side.ManagedCommand {
		return errors.New("statusline command mismatch")
	}

	side.Confirmed = true
	// The account is recorded so a restart can tell whether the profile has
	// since been signed into a different one; the statusline payload itself
	// carries no account identity.
	side.AccountEmail, side.AccountOrgID = account.Email, account.OrgID
	b, err := marshalManagedStatusline(side)
	if err != nil {
		return err
	}
	return atomicReplace(path, b)
}

// InstalledBinding returns the confirmed binding for the currently installed
// adapter. Missing, pending, or changed installations require reconnection.
func InstalledBinding(configDir, inboxDir string) (string, error) {
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return "", err
	}
	inbox, err := absoluteDir(inboxDir, "inbox directory")
	if err != nil {
		return "", err
	}

	side, err := readManagedStatuslineFile(filepath.Join(dir, profileSidecar))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if side.BindingID == "" || side.InboxDir == "" || !side.Confirmed {
		return "", nil
	}
	if side.ManagedCommand == "" {
		return "", errors.New("invalid statusline sidecar")
	}
	if err := validID(side.BindingID, "binding_id"); err != nil {
		return "", err
	}
	sideInbox, err := absoluteDir(side.InboxDir, "inbox directory")
	if err != nil {
		return "", err
	}
	if sideInbox != inbox {
		return "", nil
	}

	settings, err := readSettings(filepath.Join(dir, "settings.json"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	current, ok := currentStatuslineCommand(settings)
	if !ok || current != side.ManagedCommand {
		return "", nil
	}
	return side.BindingID, nil
}

// ForgetStatuslineConfirmation invalidates the local confirmation before a
// logout starts, so a failed logout cannot reuse the previous binding.
func ForgetStatuslineConfirmation(configDir string) error {
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, profileSidecar)
	side, err := readManagedStatuslineFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	side.Confirmed = false
	b, err := marshalManagedStatusline(side)
	if err != nil {
		return err
	}
	return atomicReplace(path, b)
}

func currentStatuslineCommand(settings map[string]json.RawMessage) (string, bool) {
	raw, ok := settings["statusLine"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var line map[string]json.RawMessage
	if json.Unmarshal(raw, &line) != nil {
		return "", false
	}
	var command string
	if json.Unmarshal(line["command"], &command) != nil {
		return "", false
	}
	return command, true
}

func marshalManagedStatusline(side managedStatusline) ([]byte, error) {
	b, err := json.Marshal(side)
	if err != nil {
		return nil, err
	}
	b = append(b, '\n')
	return b, nil
}

// InstalledAccount returns the account the confirmed binding was issued for.
// An empty account means the installation predates account recording, in which
// case the next observation establishes the baseline.
func InstalledAccount(configDir string) (Account, error) {
	dir, err := absoluteDir(configDir, "config directory")
	if err != nil {
		return Account{}, err
	}
	side, err := readManagedStatuslineFile(filepath.Join(dir, profileSidecar))
	if err != nil {
		if os.IsNotExist(err) {
			return Account{}, nil
		}
		return Account{}, err
	}
	if !side.Confirmed {
		return Account{}, nil
	}
	return Account{LoggedIn: side.AccountEmail != "", Email: side.AccountEmail, OrgID: side.AccountOrgID}, nil
}
