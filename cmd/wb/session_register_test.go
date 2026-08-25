package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
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
