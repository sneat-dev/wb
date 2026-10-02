//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// installClosedPullRequestGH stubs the one GitHub read --closed-pr makes:
// the pull request object of an unmerged, closed duplicate.
func installClosedPullRequestGH(t *testing.T, state, branch, head string) {
	t.Helper()
	binDir := t.TempDir()
	payload := fmt.Sprintf(`{"number":6,"html_url":"https://github.com/acme/app/pull/6","state":%q,"merged_at":null,"head":{"ref":%q,"sha":%q},"base":{"ref":"main"}}`, state, branch, head)
	script := "#!/bin/sh\nset -eu\nif [ \"$1\" = \"api\" ]; then\n    printf '%s\\n' '" + payload + "'\n    exit 0\nfi\necho \"unexpected gh command: $*\" >&2\nexit 2\n"
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The datatug/backstage shape end to end through the cobra wiring: the
// duplicate's pull request is closed unmerged, the discard names it with a
// reason, and wb leaves an audit record where it removed the checkout.
func TestE2EWorktreeAbortClosedPullRequestDiscardsADuplicateAndReportsTheAudit(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "a duplicate of a landed pull request")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-dup", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	worktree := filepath.Join(projects, "acme", "app", ".worktrees", "cli-dup")
	branch := gitOutput(t, worktree, "branch", "--show-current")
	head := gitOutput(t, worktree, "rev-parse", "HEAD")

	// A pull request still open is shown as not sealable in the dry run, with the reason.
	installClosedPullRequestGH(t, "open", branch, head)
	args := []string{"--projects-root", projects, "worktree", "abort", "cli-dup", "--disposition", "discarded",
		"--closed-pr", "https://github.com/acme/app/pull/6", "--reason", "duplicate of pull request 5"}
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != exitOK || !strings.Contains(stdout.String(), "cannot seal acme/app discarded: --closed-pr pull request acme/app#6 is not closed") {
		t.Fatalf("open pull request: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	// A closed stdout is reported, not swallowed, whichever line it fails on.
	if code := run(args, &failAtCallWriter{failAt: 1}, &stderr); code == exitOK {
		t.Fatal("a failed write of the refusal line was swallowed")
	}

	installClosedPullRequestGH(t, "closed", branch, head)
	if code := run(args, &failAtCallWriter{failAt: 2}, &stderr); code == exitOK {
		t.Fatal("a failed write of the closed pull request line was swallowed")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != exitOK || !strings.Contains(stdout.String(), "closed pull request acme/app#6 head "+head) {
		t.Fatalf("dry run: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("dry run removed the worktree: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(append(args, "--apply", "--remote", "--format", "json"), &stdout, &stderr); code != exitOK {
		t.Fatalf("apply: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var results []struct {
		Applied           bool `json:"applied"`
		WorktreeGone      bool `json:"worktree_gone"`
		ClosedPullRequest struct {
			AuditPath string `json:"audit_path"`
			Reason    string `json:"reason"`
		} `json:"closed_pull_request"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil || len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone ||
		results[0].ClosedPullRequest.Reason != "duplicate of pull request 5" {
		t.Fatalf("json result: %v\n%s", err, stdout.String())
	}
	if _, err := os.Stat(results[0].ClosedPullRequest.AuditPath); err != nil {
		t.Fatalf("audit record missing: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
}
