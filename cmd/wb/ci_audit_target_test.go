package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestCIAuditWithTargetReportsAPendingTotalRise drives `wb ci audit <repo>
// --target <branch>` through the real CLI dispatch, exercising ci.go's
// ciaudit.CompareAgainstTarget call site (cmd/wb/ci.go, "also compare ...
// target branch") against a real Git repository rather than only through
// internal/ciaudit's own unit tests, which cover CompareAgainstTarget's
// composition and error propagation but never this command's own call site.
//
// It reuses the real-git fixture helpers coverage_ratchet_test.go already
// counts on unit_tier.pending (newRatchetFixtureRepo, its
// writeFile/commitAll methods, and runCommand), building the bare origin
// remote through runCommand plus the same gc.auto/maintenance.auto/
// receive.autogc config calls testenv.InitBareRemoteForTest itself makes,
// rather than calling that named helper directly, so this file adds no
// exec, git-helper or PATH pattern of its own and the pending list's counts
// stay exact.
//
// printCIAudit prints through the bare os.Stdout, not the writer `run` is
// given, so this test captures it with cwCovCaptureStdout -- which is why it
// is not t.Parallel(): that helper swaps the process-global os.Stdout.
func TestCIAuditWithTargetReportsAPendingTotalRise(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	// A non-test .go file so ciaudit.Audit sets HasGo and printCIAudit does
	// not short-circuit before the findings loop ("no Go/frontend/deploy CI
	// policy applies" -- see cmd/wb/ci.go's printCIAudit).
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("internal/quality/testdata/unit_tier.pending", "a_test.go\t3\ttask-1\n")
	repo.commitAll("seed the pending list")

	origin := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(filepath.Dir(origin), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(origin), err)
	}
	runCommand(t, "", "git", "init", "--bare", "--initial-branch=main", origin)
	for index, key := range [3]string{"gc.auto", "maintenance.auto", "receive.autogc"} {
		value := [3]string{"0", "false", "false"}[index]
		runCommand(t, origin, "git", "config", key, value)
	}
	runCommand(t, repo.dir, "git", "remote", "add", "origin", origin)
	runCommand(t, repo.dir, "git", "branch", "-M", "main")
	runCommand(t, repo.dir, "git", "push", "-q", "origin", "main")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature/x")
	repo.writeFile("internal/quality/testdata/unit_tier.pending",
		"a_test.go\t3\ttask-1\nb_test.go\t1\ttask-2\n")
	repo.commitAll("grow the pending total")

	testenv.Isolate(t)
	var stderr bytes.Buffer
	var code int
	args := []string{"ci", "audit", repo.dir, "--target", "main", "--strict"}
	stdout := cwCovCaptureStdout(t, func() {
		code = run(args, &bytes.Buffer{}, &stderr)
	})
	if code != 1 {
		t.Fatalf("run(%q) exit = %d, stdout=%s stderr=%s", args, code, stdout, stderr.String())
	}
	if !strings.Contains(stdout, "unit-tier-pending-total-rose") {
		t.Fatalf("stdout = %q, want a unit-tier-pending-total-rose finding", stdout)
	}
}
