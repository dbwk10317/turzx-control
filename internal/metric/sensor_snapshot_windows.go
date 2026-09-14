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
	if err := validateSnapshotHandleACL(handle, false); err != nil {
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

func validateTrustedSnapshotPath(path string) error {
	programData := os.Getenv("ProgramData")
	if strings.TrimSpace(programData) == "" {
		return errors.New("ProgramData is unavailable")
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	root := filepath.Join(programData, "TURZXControl", "Sensors", sid)
	expected := filepath.Join(root, "snapshot.json")
	clean, err := filepath.Abs(path)
	if err != nil || !strings.EqualFold(filepath.Clean(clean), filepath.Clean(expected)) {
		return errors.New("sensor snapshot path is outside the trusted ProgramData location")
	}
	for _, item := range []string{filepath.Join(programData, "TURZXControl"), filepath.Join(programData, "TURZXControl", "Sensors"), root} {
		if err := validateSnapshotACL(item, true); err != nil {
			return fmt.Errorf("unsafe sensor snapshot directory: %w", err)
		}
	}
	return validateSnapshotACL(expected, false)
}

func validateSnapshotACL(path string, protected bool) error {
	h, err := windows.CreateFile(windows.StringToUTF16Ptr(path), windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return validateSnapshotHandleACL(h, protected)
}

func validateSnapshotHandleACL(h windows.Handle, protected bool) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		if err != nil {
			return err
		}
		return errors.New("missing security descriptor")
	}
	return validateSnapshotDescriptor(sd, protected)
}

func validateSnapshotDescriptor(sd *windows.SECURITY_DESCRIPTOR, protected bool) error {
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
	if owner == nil || (!owner.Equals(admin) && !owner.Equals(system)) {
		return errors.New("owner must be BUILTIN\\Administrators or SYSTEM")
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
	const fileDeleteChild windows.ACCESS_MASK = 0x40
	dangerous := windows.ACCESS_MASK(windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.GENERIC_WRITE|windows.GENERIC_ALL) | fileDeleteChild
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil || ace == nil {
			if err != nil {
				return err
			}
			return errors.New("invalid ACE")
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported ACE type")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(admin) && !sid.Equals(system) && windows.ACCESS_MASK(ace.Mask)&dangerous != 0 {
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
