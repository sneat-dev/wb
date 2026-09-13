//go:build windows

package lifecyclehooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateTrustedExecutable(path string, info os.FileInfo) error {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".exe" && extension != ".com" {
		return fmt.Errorf("run must resolve to a direct .exe or .com executable on Windows")
	}
	return validateTrustedWindowsACL(path, info, "run")
}

func validateTrustedControlFile(path string, info os.FileInfo, purpose string) error {
	return validateTrustedWindowsACL(path, info, purpose)
}

func validateTrustedWindowsACL(path string, _ os.FileInfo, purpose string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect %s ACL: %w", purpose, err)
	}
	if sd == nil {
		return fmt.Errorf("inspect %s ACL: security descriptor is missing", purpose)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("inspect %s owner: %w", purpose, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("inspect current Windows user: %w", err)
	}
	localSystem, _ := windows.StringToSid("S-1-5-18")
	administrators, _ := windows.StringToSid("S-1-5-32-544")
	if !owner.Equals(user.User.Sid) && !owner.Equals(localSystem) && !owner.Equals(administrators) {
		return fmt.Errorf("%s must be owned by the current user, LocalSystem, or Administrators", purpose)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("inspect %s DACL: missing or invalid discretionary ACL", purpose)
	}
	everyone, _ := windows.StringToSid("S-1-1-0")
	authenticatedUsers, _ := windows.StringToSid("S-1-5-11")
	users, _ := windows.StringToSid("S-1-5-32-545")
	broad := []*windows.SID{everyone, authenticatedUsers, users}
	const writable = windows.GENERIC_ALL | windows.GENERIC_WRITE |
		windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES |
		windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			break
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&writable == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		for _, candidate := range broad {
			if sid.Equals(candidate) {
				return fmt.Errorf("%s ACL grants write access to a broad Windows principal", purpose)
			}
		}
	}
	return nil
}
