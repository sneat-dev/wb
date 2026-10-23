package main

import (
	"bytes"
	"strings"
	"testing"
)

// wb upgrade is registered at the root and its help renders without any
// injected environment or network access.
func TestUpgradeCmd_RegisteredAtRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"upgrade", "--help"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("wb upgrade --help exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"upgrade", "--all", "--check", "--dry-run", "--yes"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help does not mention %q:\n%s", want, stdout.String())
		}
	}
}

// AC: install#req:upgrade-exit-code-mapping, cli-install#req:upgrade-targets
// (unknown-target-refused), review S2 — `wb upgrade nosuchcli` MUST fail
// before any confirmation, network request or write, exits exitUsage (the
// invocation was rejected before any work started), and the message
// carries an exact "upgrade: " prefix and names the unknown target, never
// "install:"/"self-update:" (review S1). namedUpgradeCandidates rejects
// every unknown name before probing or looking up anything, so this is
// offline-safe with no injected Env.
func TestUpgradeCmd_UnknownTargetExitsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"upgrade", "nosuchcli"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (%d); stderr: %s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "upgrade: ") {
		t.Errorf("stderr does not carry the exact upgrade: prefix: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "nosuchcli") {
		t.Errorf("stderr does not name the unknown target: %q", stderr.String())
	}
	for _, wrongPrefix := range []string{"install:", "self-update:"} {
		if strings.Contains(stderr.String(), wrongPrefix) {
			t.Errorf("stderr carries a %s prefix; upgrade errors MUST NOT: %q", wrongPrefix, stderr.String())
		}
	}
}
