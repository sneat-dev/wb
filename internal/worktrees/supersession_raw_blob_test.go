package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupersessionServiceAuthenticatesExactWorkflowBlobWhitespace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		targetBody string
		want       string
	}{
		{name: "same exact bytes preserve trailing spaces and newline", targetBody: "name: provider tools  \njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"},
		{name: "target trailing whitespace drift is rejected", targetBody: "name: provider tools  \njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n ", want: "differs from the reviewed source bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			supersessionBlobGit(t, root, "init")
			supersessionBlobGit(t, root, "config", "user.name", "WB test")
			supersessionBlobGit(t, root, "config", "user.email", "wb-test@example.invalid")
			workflowPath := filepath.Join(root, ".github", "workflows", "provider-tools.yml")
			if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			supersessionBlobGit(t, root, "add", "README.md")
			supersessionBlobGit(t, root, "commit", "-m", "base")
			base := supersessionBlobGitOutput(t, root, "rev-parse", "HEAD")

			sourceBody := "name: provider tools  \njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"
			if err := os.WriteFile(workflowPath, []byte(sourceBody), 0o644); err != nil {
				t.Fatal(err)
			}
			supersessionBlobGit(t, root, "add", ".github/workflows/provider-tools.yml")
			supersessionBlobGit(t, root, "commit", "-m", "source workflow")
			source := supersessionBlobGitOutput(t, root, "rev-parse", "HEAD")

			supersessionBlobGit(t, root, "reset", "--hard", base)
			if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workflowPath, []byte(tc.targetBody), 0o644); err != nil {
				t.Fatal(err)
			}
			supersessionBlobGit(t, root, "add", ".github/workflows/provider-tools.yml")
			supersessionBlobGit(t, root, "commit", "-m", "landed replacement workflow")
			target := supersessionBlobGitOutput(t, root, "rev-parse", "HEAD")
			sha := sha256.Sum256([]byte(sourceBody))
			receipt := SupersessionReceipt{
				OriginalHead: source, TargetHead: target,
				Replacements:      []SupersessionReplacement{{Kind: "commit", Ref: target, SHA: target}},
				Approval:          SupersessionApproval{Actor: "reviewer", Trusted: true, Decision: "approved", ReceiptID: "production-blob-proof", ApprovedAt: time.Now().UTC()},
				WorkflowAdoptions: []SupersessionWorkflowAdoption{{Path: ".github/workflows/provider-tools.yml", SourceSHA256: hex.EncodeToString(sha[:]), ReplacementSHA: target, Reviewed: true}},
			}
			entry := ListResult{Task: "integration", Branch: "wb/integration/main", Repository: "demo-db/websites", Base: "main", CanonicalDir: root, WorktreeDir: root, HeadSHA: source, RemoteTargetSHA: target}
			got := supersessionService().ValidateDependencyDeltasReason(context.Background(), receipt, supersessionEntry(entry))
			if tc.want == "" && got != "" {
				t.Fatalf("exact production workflow adoption refused: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("whitespace-drift refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func supersessionBlobGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func supersessionBlobGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
