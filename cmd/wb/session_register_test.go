package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestSessionRegisterAcceptsPreallocatedSuccessorIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	previousRuntimeProcessCheck := sessionRegisterRuntimeProcess
	previousCurrentPID := sessionRegisterCurrentPID
	sessionRegisterCurrentPID = func() int { return -1 }
	sessionRegisterRuntimeProcess = func(pid int, runtime string) bool {
		return pid == os.Getpid() && runtime == "codex"
	}
	t.Cleanup(func() {
		sessionRegisterRuntimeProcess = previousRuntimeProcessCheck
		sessionRegisterCurrentPID = previousCurrentPID
	})
	command := newSessionRegisterCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{
		"--pid", strconv.Itoa(os.Getpid()),
		"--wb-session-id", "wbs-successor",
		"--machine", "hetzner-vm1",
		"--runtime", "codex",
		"--native-harness-id", "native-123",
		"--tmux-name", "wb-session-wbs-successor",
		"--predecessor-wb-session-id", "wbs-source",
		"--handoff-id", "handoff-123",
	})
	if err := command.Execute(); err != nil {
		t.Fatalf("session register: %v", err)
	}

	record, ok := session.Lookup(filepath.Join(home, session.DirName), os.Getpid())
	if !ok {
		t.Fatal("registered successor was not readable")
	}
	if record.WBSessionID != "wbs-successor" || record.Machine != "hetzner-vm1" ||
		record.NativeHarnessID != "native-123" || record.TmuxName != "wb-session-wbs-successor" ||
		record.PredecessorWBSessionID != "wbs-source" || record.HandoffID != "handoff-123" {
		t.Fatalf("record = %+v", record)
	}
}

func TestSessionRegisterRejectsOwnPID(t *testing.T) {
	command := newSessionRegisterCmd()
	command.SetArgs([]string{"--pid", strconv.Itoa(os.Getpid()), "--runtime", "codex"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "WB itself") {
		t.Fatalf("register own PID error = %v, want self-registration rejection", err)
	}
}

func TestSessionRegisterRejectsImmediateShellPID(t *testing.T) {
	command := newSessionRegisterCmd()
	command.SetArgs([]string{"--pid", strconv.Itoa(os.Getppid()), "--runtime", "codex"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "intermediate shell") {
		t.Fatalf("register immediate parent error = %v, want intermediate-shell rejection", err)
	}
}
