// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package metric

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const fileDeleteChild windows.ACCESS_MASK = 0x40

// writeAccess is every right that changes the snapshot or the directory holding
// it. replaceAccess is the smaller set that swaps a whole directory out from
// under the reader; an owner is included in both because an owner can always
// rewrite the DACL.
// trustedInstallerSID owns the volume root and %ProgramFiles% on a stock
// Windows install, so ancestors of a protected directory may legitimately
// belong to it. It is a fixed well-known SID with no CreateWellKnownSid entry.
const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

var (
	writeAccess   = windows.ACCESS_MASK(windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_WRITE|windows.GENERIC_ALL) | fileDeleteChild
	replaceAccess = windows.ACCESS_MASK(windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_ALL) | fileDeleteChild
)

func openSnapshotFile(path string) (*os.File, error) {
	if err := rejectReparseParents(path); err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("sensor snapshot must be a regular non-reparse file")
	}
	if err := validateSnapshotHandleACL(handle, false, writeAccess); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openSnapshotFileForRead(path string, fixture bool) (*os.File, error) {
	if fixture {
		return os.Open(path)
	}
	return openSnapshotFile(path)
}

// validateTrustedSnapshotPath accepts <installation root>\<user SID>\snapshot.json
// for any installation root the administrator chose. Trust comes from the ACLs,
// not from a fixed prefix: the snapshot directory must be administrator owned
// with a protected DACL and no untrusted write access, and no ancestor may let
// an untrusted principal replace it.
func validateTrustedSnapshotPath(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	clean = filepath.Clean(clean)
	dir := filepath.Dir(clean)
	if !strings.EqualFold(filepath.Base(clean), "snapshot.json") || !strings.EqualFold(filepath.Base(dir), user.User.Sid.String()) {
		return errors.New("sensor snapshot must be snapshot.json in a directory named after the current user SID")
	}
	// The file itself is checked on its open handle in openSnapshotFile.
	if err := validateSnapshotACL(dir, true, writeAccess); err != nil {
		return fmt.Errorf("unsafe sensor snapshot directory: %w", err)
	}
	// Ancestors legitimately let users create their own subdirectories
	// (%ProgramData% does), so only the rights that replace an existing
	// directory disqualify them.
	for parent := filepath.Dir(dir); ; parent = filepath.Dir(parent) {
		if err := validateAncestorACL(parent); err != nil {
			return fmt.Errorf("unsafe sensor snapshot parent %s: %w", parent, err)
		}
		if next := filepath.Dir(parent); next == parent {
			return nil
		}
	}
}

// validateAncestorACL checks a directory above the snapshot directory. Stock
// Windows ancestors are owned by TrustedInstaller and carry inherit-only ACEs
// for CREATOR OWNER and Authenticated Users; neither grants access to the
// directory itself, so both are accepted.
func validateAncestorACL(path string) error {
	installer, err := windows.StringToSid(trustedInstallerSID)
	if err != nil {
		return err
	}
	return validateSnapshotACL(path, false, replaceAccess, installer)
}

func validateSnapshotACL(path string, protected bool, dangerous windows.ACCESS_MASK, trusted ...*windows.SID) error {
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return validateSnapshotHandleACL(h, protected, dangerous, trusted...)
}

func validateSnapshotHandleACL(h windows.Handle, protected bool, dangerous windows.ACCESS_MASK, trusted ...*windows.SID) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		if err != nil {
			return err
		}
		return errors.New("missing security descriptor")
	}
	return validateSnapshotDescriptor(sd, protected, dangerous, trusted...)
}

// trusted names extra principals beyond Administrators and SYSTEM that may own
// the object and hold dangerous rights on it.
func validateSnapshotDescriptor(sd *windows.SECURITY_DESCRIPTOR, protected bool, dangerous windows.ACCESS_MASK, trusted ...*windows.SID) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	trusted = append(trusted, admin, system)
	if owner == nil || !anySID(trusted, owner) {
		return errors.New("owner must be BUILTIN\\Administrators, SYSTEM or a trusted installer principal")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PRESENT == 0 {
		return errors.New("DACL is not present")
	}
	if protected && control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("directory DACL is not protected")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		if err != nil {
			return err
		}
		return errors.New("missing DACL")
	}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil || ace == nil {
			if err != nil {
				return err
			}
			return errors.New("invalid ACE")
		}
		// A deny ACE only removes access, so an administrator hardening the
		// directory further must not break the reader.
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported ACE type")
		}
		// An inherit-only ACE grants nothing on this object; it only seeds
		// children, and every directory this code trusts has a protected DACL
		// that blocks inheritance.
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !anySID(trusted, sid) && windows.ACCESS_MASK(ace.Mask)&dangerous != 0 {
			return errors.New("untrusted principal has write access")
		}
	}
	return nil
}

func rejectReparseParents(path string) error {
	current := filepath.Clean(path)
	for parent := filepath.Dir(current); parent != current; parent = filepath.Dir(parent) {
		attrs, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(parent))
		if err != nil && !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return err
		}
		if err == nil && attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("sensor snapshot parent is a reparse point")
		}
		if strings.EqualFold(parent, filepath.VolumeName(parent)+string(filepath.Separator)) {
			break
		}
		current = parent
	}
	return nil
}

func anySID(list []*windows.SID, sid *windows.SID) bool {
	for _, item := range list {
		if sid.Equals(item) {
			return true
		}
	}
	return false
}
