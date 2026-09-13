//go:build windows

package lifecyclehooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTrustAcceptsPrivateExecutableAndRejectsScript(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "indexer.exe")
	if err := os.WriteFile(executable, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedExecutable(executable, info); err != nil {
		t.Fatalf("private executable rejected: %v", err)
	}
	script := filepath.Join(filepath.Dir(executable), "indexer.cmd")
	if err := validateTrustedExecutable(script, info); err == nil || !strings.Contains(err.Error(), ".exe or .com") {
		t.Fatalf("script error=%v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GW;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(executable, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedExecutable(executable, info); err == nil || !strings.Contains(err.Error(), "broad Windows principal") {
		t.Fatalf("broad ACL error=%v", err)
	}
}
