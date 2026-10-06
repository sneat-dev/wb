//go:build windows

package daemonruntime

import (
	"testing"

	"golang.org/x/sys/windows"
)

// This is a consumer-level Windows regression test for cli-helpers' named-path
// ACL implementation. ProtectOwnerOnlyFile uses file.Name(), so both WB lock
// handles must retain the actual filesystem path rather than a display label.

func setWindowsTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// Off macOS there is no fixed-label launchd service for a start to remove, so
// the other-root check never refuses.
