package worktreebranches

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

func TestWorkflowAdoptionUsesExactGitBlobBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		targetBody string
		want       string
	}{
		{name: "same exact file", targetBody: "name: provider tools\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"},
		{name: "trailing newline drift", targetBody: "name: provider tools\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4", want: "differs from the reviewed source bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gitBlobTest(t, root, "init")
			gitBlobTest(t, root, "config", "user.name", "WB test")
			gitBlobTest(t, root, "config", "user.email", "wb-test@example.invalid")
			workflowPath := filepath.Join(root, ".github", "workflows", "provider-tools.yml")
			if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitBlobTest(t, root, "add", "README.md")
			gitBlobTest(t, root, "commit", "-m", "base")
			base := gitBlobOutput(t, root, "rev-parse", "HEAD")

			sourceBody := "name: provider tools\njobs:\n  test:\n    steps:\n      - uses: actions/checkout@v4\n"
			if err := os.WriteFile(workflowPath, []byte(sourceBody), 0o644); err != nil {
				t.Fatal(err)
			}
			gitBlobTest(t, root, "add", ".github/workflows/provider-tools.yml")
			gitBlobTest(t, root, "commit", "-m", "source workflow")
			source := gitBlobOutput(t, root, "rev-parse", "HEAD")

			gitBlobTest(t, root, "reset", "--hard", base)
			if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workflowPath, []byte(tc.targetBody), 0o644); err != nil {
				t.Fatal(err)
			}
			gitBlobTest(t, root, "add", ".github/workflows/provider-tools.yml")
			gitBlobTest(t, root, "commit", "-m", "landed replacement workflow")
			target := gitBlobOutput(t, root, "rev-parse", "HEAD")
			sha := sha256.Sum256([]byte(sourceBody))
			receipt := SupersessionReceipt{
				OriginalHead: source, TargetHead: target,
				Replacements:      []SupersessionReplacement{{Kind: "commit", Ref: target, SHA: target}},
				Approval:          SupersessionApproval{Actor: "reviewer", Trusted: true, Decision: "approved", ReceiptID: "blob-proof", ApprovedAt: time.Now().UTC()},
				WorkflowAdoptions: []SupersessionWorkflowAdoption{{Path: ".github/workflows/provider-tools.yml", SourceSHA256: hex.EncodeToString(sha[:]), ReplacementSHA: target, Reviewed: true}},
			}
			entry := SupersessionEntry{Task: "integration", Branch: "wb/integration/main", CanonicalDir: root, HeadSHA: source, RemoteTargetSHA: target}
			service := SupersessionService{Ports: SupersessionPorts{
				Git: func(_ context.Context, repository string, args ...string) (string, error) {
					out, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput()
					if err != nil {
						return "", err
					}
					// Match the production text Git adapter: it trims output and is
					// suitable for metadata, but not for authenticating file bytes.
					return strings.TrimSpace(string(out)), nil
				},
				ReadGitFileBytes: func(_ context.Context, repository, revision, file string) ([]byte, error) {
					return exec.Command("git", "-C", repository, "cat-file", "blob", revision+":"+file).Output()
				},
				IsAncestor: func(_ context.Context, repository, ancestor, descendant string) (bool, error) {
					err := exec.Command("git", "-C", repository, "merge-base", "--is-ancestor", ancestor, descendant).Run()
					if err == nil {
						return true, nil
					}
					if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
						return false, nil
					}
					return false, err
				},
			}}
			got := service.ValidateDependencyDeltasReason(context.Background(), receipt, entry)
			if tc.want == "" && got != "" {
				t.Fatalf("exact raw workflow adoption refused: %s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("trailing-newline-only mismatch refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func gitBlobTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitBlobOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
