//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"github.com/strongo/cli-helpers/daemonlifecycle"
	"golang.org/x/sys/windows"
)

const windowsFileDeleteChild windows.ACCESS_MASK = 0x00000040

const bridgeAncestorMutationRights = windows.GENERIC_ALL | windows.GENERIC_WRITE |
	windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES |
	windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windowsFileDeleteChild

func verifyBridgePathSecurity(path string, _ os.FileInfo, _ os.FileMode, private bool) error {
	if !private {
		return validateBridgeAncestorACL(path)
	}
	return daemonlifecycle.ValidateOwnerOnly(path)
}

func validateBridgeAncestorACL(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect daemon runtime ancestor ACL: %w", err)
	}
	if descriptor == nil {
		return errors.New("inspect daemon runtime ancestor ACL: security descriptor is missing")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("inspect current Windows user: %w", err)
	}
	localSystem, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	administrators, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return fmt.Errorf("resolve Administrators SID: %w", err)
	}
	trusted := []*windows.SID{user.User.Sid, localSystem, administrators}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("inspect daemon runtime ancestor owner: %w", err)
	}
	if !bridgeTrustedWindowsPrincipal(owner, trusted) {
		return errors.New("daemon runtime ancestor must be owned by the current user, LocalSystem, or Administrators")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("inspect daemon runtime ancestor DACL: missing or invalid discretionary ACL")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("inspect daemon runtime ancestor DACL entry %d: %w", index, err)
		}
		if ace == nil || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask&bridgeAncestorMutationRights == 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("daemon runtime ancestor ACL has an unsupported writable entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !bridgeTrustedWindowsPrincipal(sid, trusted) {
			return errors.New("daemon runtime ancestor ACL grants mutation access to an untrusted Windows principal")
		}
	}
	return nil
}

func bridgeTrustedWindowsPrincipal(candidate *windows.SID, trusted []*windows.SID) bool {
	if candidate == nil {
		return false
	}
	for _, principal := range trusted {
		if candidate.Equals(principal) {
			return true
		}
	}
	return false
}
