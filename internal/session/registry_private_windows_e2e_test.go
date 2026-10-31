//go:build e2e && windows

package session

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWindowsRegistryPrivateScopesAndLifecycle(t *testing.T) {
	root, caller, peer := sharedScopeFixture(t)
	peerBefore, err := os.ReadFile(recordPath(root, peer.PID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkRecordParked(root, caller, "park-windows-scope"); err != nil {
		t.Fatal(err)
	}
	if _, live, err := LookupActiveRecordExact(root, caller); err != nil || live {
		t.Fatalf("Windows parked scope treated active: %v", err)
	}
	if _, err := MarkSourceResumed(root, caller, "park-windows-scope", "wbs-windows-successor"); err != nil {
		t.Fatal(err)
	}
	peerAfter, err := os.ReadFile(recordPath(root, peer.PID))
	if err != nil || string(peerBefore) != string(peerAfter) {
		t.Fatalf("Windows scoped lifecycle touched peer: %v", err)
	}
	held, err := openRecordDirectory(root, caller.Scope, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;GR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(held.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := checkRegistryPrivacy(held, 0o700, true); err == nil {
		t.Fatal("POSIX-looking directory with broad Windows ACL was accepted")
	}
	if err := protectRegistryPrivate(held); err != nil {
		t.Fatal(err)
	}

}

func TestWindowsRegistryLockExcludesAnotherProcess(t *testing.T) {
	root := t.TempDir()
	pid := os.Getpid()
	release, err := lockPID(root, pid)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	// A canceled nonblocking waiter must leave the original native lock held.
	waitCtx, stopWait := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stopWait()
	waitDone := make(chan error, 1)
	go func() {
		unlock, err := lockPIDUsing(root, pid, func(file *os.File) (func(), error) { return lockRegistryFileContext(waitCtx, file) })
		if unlock != nil {
			unlock()
		}
		waitDone <- err
	}()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("native canceled lock waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native canceled waiter remained blocked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsRegistryLockChild$")
	child.Env = append(os.Environ(), "WB_TEST_OWNED_REGISTRY_ROOT="+root, "WB_TEST_OWNED_REGISTRY_PID="+strconv.Itoa(pid))
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	began := time.Now()
	for {
		if _, err := os.Stat(filepath.Join(root, "child-started")); err == nil {
			break
		}
		if time.Since(began) > 2*time.Second {
			t.Fatal("owned lock child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("another process crossed held registry barrier: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(root, "child-acquired")); !os.IsNotExist(err) {
		t.Fatalf("another process acquired held registry barrier: %v", err)
	}
	release()
	held = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := os.Stat(filepath.Join(root, "child-acquired")); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsRegistryLockChild(t *testing.T) {
	root := os.Getenv("WB_TEST_OWNED_REGISTRY_ROOT")
	if root == "" {
		return
	}
	pid, err := strconv.Atoi(os.Getenv("WB_TEST_OWNED_REGISTRY_PID"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "child-started"), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := lockPID(root, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.WriteFile(filepath.Join(root, "child-acquired"), []byte("exclusive registry lock acquired"), 0o600); err != nil {
		t.Fatal(err)
	}
}
