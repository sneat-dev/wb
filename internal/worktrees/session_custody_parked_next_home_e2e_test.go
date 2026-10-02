//go:build e2e

package worktrees

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

func TestE2ESessionCustodyNextMissingHomeResolver(t *testing.T) {
	t.Parallel()
	const marker = "WB_SESSION_CUSTODY_NEXT_HOME_CHILD"
	if os.Getenv(marker) == "1" {
		if _, err := os.UserHomeDir(); err == nil {
			t.Fatal("child native home remains available")
		}
		if reason := ParkedSessionReason("", nil); reason != "" {
			t.Fatalf("home refusal reason=%q", reason)
		}
		if reference, owner, err := parkedSessionWorkLogSnapshotUnderLock("", "unreached", session.Record{}, nil); reference != "" || owner != "" || err == nil || !strings.Contains(err.Error(), "resolve user home") {
			t.Fatalf("home refusal snapshot=%q,%q,%v", reference, owner, err)
		}
		if err := corroborateProjectionAcrossHomes("", "unreached", workLogProjection{}); err == nil || !strings.Contains(err.Error(), "resolve user home") {
			t.Fatalf("home refusal corroboration=%v", err)
		}
		return
	}
	deadline := time.Now().Add(time.Minute)
	if parent, ok := t.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2ESessionCustodyNextMissingHomeResolver$")
	command.Env = append(omitEnv(os.Environ(), []string{"HOME", "USERPROFILE", "WB_PROJECTS_ROOT", marker}), marker+"=1")
	if testing.CoverMode() != "" {
		if sink := wtLifeCovCoverDir(); sink != "" {
			command.Args = append(command.Args, "-test.gocoverdir="+sink)
			command.Env = append(command.Env, "GOCOVERDIR="+sink)
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native child home refusal=%v\n%s", err, output)
	}
}
