package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/testenv"
)

func TestCwWtWorktreeCorrectIdentityInProcess(t *testing.T) {
	projects, _, worktree := initGCFixture(t)

	// Read the exact durable claim identity from the fixture's own journal.
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeWorkLogCmd(&invocation{}) }, "show", worktree, "--format", "json")
	if err != nil {
		t.Fatalf("log show: %v", err)
	}
	var document struct {
		View struct {
			Claim struct {
				EffortID string `json:"effort_id"`
				RunID    string `json:"run_id"`
				ClaimID  string `json:"claim_id"`
			} `json:"claim"`
		} `json:"view"`
	}
	var claim struct {
		EffortID string
		RunID    string
		ClaimID  string
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decode work log view: %v\n%s", err, stdout)
	}
	claim = struct {
		EffortID string
		RunID    string
		ClaimID  string
	}{document.View.Claim.EffortID, document.View.Claim.RunID, document.View.Claim.ClaimID}
	if claim.ClaimID == "" || claim.EffortID == "" || claim.RunID == "" {
		t.Fatalf("claim identity = %+v", claim)
	}

	args := []string{
		claim.EffortID, claim.RunID, claim.ClaimID,
		"--mode", "manual", "--initiator", "cwWt",
		"--actor", "cwWt-operator", "--reason", "cwWt correction",
		"--event-id", "cw-wt-correction-1",
	}
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCorrectIdentityCmd(&invocation{}) }, append(args, "--model", "gpt-5", "--cli", "codex", "--provider", "openai")...)
	if err != nil {
		t.Fatalf("correct-identity: %v", err)
	}
	if !strings.Contains(stdout, "corrected execution identity for claim "+claim.ClaimID) {
		t.Fatalf("correct-identity stdout = %q", stdout)
	}

	// The json spelling encodes the same result.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCorrectIdentityCmd(&invocation{}) }, append(args,
		"--model", "gpt-5", "--cli=", "--provider=", "--event-id", "cw-wt-correction-2", "--format", "json")...)
	if err != nil {
		t.Fatalf("correct-identity json: %v", err)
	}
	if !strings.Contains(stdout, `"claim_id"`) {
		t.Fatalf("correct-identity json = %q", stdout)
	}

	// Invalid identifiers are refused by the backend.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCorrectIdentityCmd(&invocation{}) }, "e", "r", "c",
		"--mode", "manual", "--initiator", "cwWt", "--actor", "a", "--reason", "r", "--event-id", "x"); err == nil {
		t.Fatal("correct-identity with invalid identifiers must fail")
	}
}

func TestCwWtWorktreeAbortFilteredRepositories(t *testing.T) {
	projects, _, _ := initGCFixture(t)

	// A --filter that excludes every repository leaves the task unresolved and
	// reports it rather than pretending the abort covered it.
	command := newWorktreeAbortCmd(&invocation{projectsRoot: projects, filterFlag: "no-such-repository"})
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"gc-cli", "--disposition", "discarded"})
	if err := command.Execute(); err != nil {
		t.Fatalf("abort with an excluding filter: %v (stderr=%s)", err, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "excluded by --filter acme/app discarded: not evaluated") {
		t.Fatalf("abort excluded row = %q", text)
	}
	if !strings.Contains(text, "1 repositories excluded by --filter remain unresolved for task \"gc-cli\"") {
		t.Fatalf("abort excluded footer = %q", text)
	}

	// The json spelling carries the same excluded rows.
	out.Reset()
	command = newWorktreeAbortCmd(&invocation{filterFlag: "no-such-repository"})
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"gc-cli", "--disposition", "discarded", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatalf("abort json with an excluding filter: %v", err)
	}
	if !strings.Contains(out.String(), `"excluded": true`) {
		t.Fatalf("abort excluded json = %q", out.String())
	}
}

func TestCwWtWorktreeCreateDerivesRepositoryFromOrigin(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	// The origin path ends in acme/app.git so the derived slug is acme/app and
	// the canonical clone lives exactly where create resolves it.
	clone := filepath.Join(projects, "acme", "app")
	remote := cwCovCloneWithOrigin(t, seed, "acme/app", clone)
	cwCovPointOriginAtForge(t, clone, remote, "github.com", "acme/app")
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	cwWtWriteFile(t, prompt, "derived repository prompt\n")

	// With no repository argument, create derives owner/repository from the
	// current checkout's origin remote.
	t.Chdir(clone)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{projectsRoot: projects}) }, "cw-wt-derived",
		"--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
		"--original-prompt-file", prompt, "--no-claim")
	if err != nil {
		t.Fatalf("create deriving its repository: %v", err)
	}
	if !strings.Contains(stdout, filepath.Join(projects, ".worktrees", "cw-wt-derived", "github.com", "acme", "app")) {
		t.Fatalf("derived create stdout = %q", stdout)
	}
}

func TestCwWtWorktreeCreateDeriveRepositoryFailure(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	cwWtWriteFile(t, prompt, "no origin here\n")

	// A directory with no origin remote cannot supply a repository.
	t.Chdir(t.TempDir())
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCreateCmd(&invocation{}) }, "cw-wt-derived",
		"--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
		"--original-prompt-file", prompt)
	if err == nil || !strings.Contains(err.Error(), "derive current repository") {
		t.Fatalf("create without a derivable repository = %v", err)
	}
}

func TestCwWtWorktreeCreateStdinReadFailure(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	command := newWorktreeCreateCmd(&invocation{projectsRoot: projects})
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetIn(cwWtErrorReader{})
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs([]string{"t", "acme/app", "--model", "unknown", "--mode", "manual", "--initiator", "cwWt",
		"--original-prompt-file", "-"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "read --original-prompt-file - from stdin") {
		t.Fatalf("create with a failing stdin = %v", err)
	}
}

func TestCwWtWorktreeRelocateAndGuardUsageErrors(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seed, "app", clone)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	// --apply in manual mode without --initiator is refused by admission.
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRelocateCmd(&invocation{}) }, "absent-task", "--to", "local", "--apply", "--mode", "manual")
	if err == nil || !strings.Contains(err.Error(), "--initiator") {
		t.Fatalf("relocate --apply --mode manual = %v", err)
	}

	// guard's own format validation.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{}) }, clone, "--format", "yaml"); err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("guard --format yaml = %v", err)
	}

	// The single ok: line is the only write on a clean checkout, so a writer
	// that fails immediately must turn the successful report into an error.
	if err := cwWtExecOut(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{}) },
		&cwWtFailWriter{Allow: 0}, &bytes.Buffer{}, clone); err == nil {
		t.Fatal("guard with a failing writer returned nil")
	}
	if err := cwWtExecOut(t, projects, func() *cobra.Command { return newWorktreeGuardCmd(&invocation{}) },
		&cwWtFailWriter{Allow: 0}, &bytes.Buffer{}, clone, "--format", "json"); err == nil {
		t.Fatal("guard json with a failing writer returned nil")
	}
}

func TestCwWtWorktreeCleanupWritesArtifactAndQuarantineWarnings(t *testing.T) {
	projects, home, _ := initGCFixture(t)
	// A reserved stage directory is WB's own control-plane residue: the next
	// cleanup reports it on stderr and keeps going.
	stage := filepath.Join(home, "worktrees", "gc-cli", ".wb-stage-leftover")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "junk"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) }, "gc-cli")
	if err != nil {
		t.Fatalf("cleanup with residue: %v", err)
	}
	if !strings.Contains(stdout, "skip gc-cli acme/app") {
		t.Fatalf("cleanup stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "info: cleanup WB internal") || !strings.Contains(stderr, stage) {
		t.Fatalf("cleanup did not report the reserved stage on stderr: %q", stderr)
	}

	// The same residue is a first-class artefact in the json envelope.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeCleanupCmd(&invocation{}) }, "gc-cli", "--format", "json")
	if err != nil {
		t.Fatalf("cleanup json with residue: %v", err)
	}
	if !strings.Contains(stdout, `"artifacts"`) {
		t.Fatalf("cleanup json did not carry artifacts: %q", stdout)
	}
}

func TestCwWtExecOutHelperIsolated(t *testing.T) {
	// cwWtExecOut builds and executes the caller's command against the
	// caller-supplied output streams.
	testenv.Isolate(t)
	built := false
	if err := cwWtExecOut(t, "/tmp/cw-wt-projects", func() *cobra.Command {
		built = true
		return &cobra.Command{Use: "noop", RunE: func(*cobra.Command, []string) error { return nil }}
	}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !built {
		t.Fatal("cwWtExecOut did not invoke the build function")
	}
}
