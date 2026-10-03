package main

import (
	"bytes"
	"strings"
	"testing"
)

// wb install is registered at the root and offline: `wb install` with no
// arguments is a pure status probe (cli-install#req:list-offline-read-only)
// against wb's own PATH/host directory, so it must succeed without any
// injected environment or network access.
func TestInstallCmd_RegisteredAtRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"install", "--help"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("wb install --help exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "install") {
		t.Errorf("help does not mention install:\n%s", stdout.String())
	}
}

// AC: cli-install#ac:direct-install-writes-only-verified-new-files,
// cli-install#req:host-owned-exit-codes ("KindUnknownTarget to its usage or
// invalid-argument code") — `wb install nosuchcli` MUST fail before any
// confirmation, network request or write, exits exitUsage (review S2: the
// invocation was rejected before any work started, exactly wb's own
// documented meaning for exit 2), and the message carries an exact
// "install: " prefix and names the unknown target, never "self-update:" or
// "upgrade:" (review S1). Plan() rejects every unknown name before probing
// anything, so this is offline-safe with no injected Env.
func TestInstallCmd_UnknownTargetExitsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"install", "nosuchcli"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (%d); stderr: %s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "install: ") {
		t.Errorf("stderr does not carry the exact install: prefix: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "nosuchcli") {
		t.Errorf("stderr does not name the unknown target: %q", stderr.String())
	}
	for _, wrongPrefix := range []string{"self-update:", "upgrade:"} {
		if strings.Contains(stderr.String(), wrongPrefix) {
			t.Errorf("stderr carries a %s prefix; install errors MUST NOT (cli-install#req:host-owned-exit-codes): %q", wrongPrefix, stderr.String())
		}
	}
}
