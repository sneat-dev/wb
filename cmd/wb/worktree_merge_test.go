package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestWorktreeMergeForcedProgressIsNewlineDelimited(t *testing.T) {
	var output bytes.Buffer
	writer := &worktreeMergeLineWriter{out: &output}
	for _, text := range []string{"\rworktree merge: preparing", "\rworktree merge: waiting", "\n"} {
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), "worktree merge: preparing\nworktree merge: waiting\n"; got != want {
		t.Fatalf("progress output = %q, want %q", got, want)
	}
}

func TestWorktreeMergeCommandExposesCombinedAndTwoPhaseJourney(t *testing.T) {
	command := newWorktreeMergeCmd()
	if command.Use != "merge <source-worktree...>" {
		t.Fatalf("Use = %q", command.Use)
	}
	for _, flag := range []string{"target", "route", "cleanup", "on-failure", "format", "progress"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("combined merge is missing --%s", flag)
		}
	}
	if command.Flags().Lookup("rebatch-receipt") != nil {
		t.Error("combined merge must not expose --rebatch-receipt; rebatching is a prepare-only transition")
	}
	prepare, _, err := command.Find([]string{"prepare"})
	if err != nil || prepare == nil || prepare.Flags().Lookup("rebatch-receipt") == nil {
		t.Fatalf("merge prepare must expose --rebatch-receipt: command=%v err=%v", prepare, err)
	}
	if route := command.Flags().Lookup("route"); route == nil || route.DefValue != "auto" {
		t.Fatalf("--route = %#v, want auto", route)
	}
	if cleanup := command.Flags().Lookup("cleanup"); cleanup == nil || cleanup.DefValue != "false" {
		t.Fatalf("--cleanup = %#v, want false", cleanup)
	}
	for _, name := range []string{"prepare", "land", "resume", "revert", "acknowledge-landed-failed", "acknowledge-receipt-collision", "seal-validation-failed", "supersede-validation-failed", "correct-self-supersession", "prepare-published-forward-repair"} {
		if child, _, err := command.Find([]string{name}); err != nil || child == nil || child.Name() != name {
			t.Errorf("merge command is missing %s: child=%v err=%v", name, child, err)
			continue
		}
		child, _, _ := command.Find([]string{name})
		if name != "acknowledge-landed-failed" && name != "acknowledge-receipt-collision" && name != "seal-validation-failed" && name != "supersede-validation-failed" && name != "correct-self-supersession" && name != "prepare-published-forward-repair" && child.Flags().Lookup("progress") == nil {
			t.Errorf("merge %s is missing --progress", name)
		}
	}
	resume, _, err := command.Find([]string{"resume"})
	if err != nil || resume == nil || resume.Flags().Lookup("stop-before-merge") == nil {
		t.Fatalf("merge resume must expose --stop-before-merge: command=%v err=%v", resume, err)
	}
	land, _, err := command.Find([]string{"land"})
	if err != nil || land == nil || land.Flags().Lookup("stop-before-merge") != nil {
		t.Fatalf("merge land must not expose resume-only --stop-before-merge: command=%v err=%v", land, err)
	}
	ack, _, err := command.Find([]string{"acknowledge-landed-failed"})
	if err != nil || ack == nil || ack.Flags().Lookup("apply") == nil || ack.Flags().Lookup("actor") == nil || ack.Flags().Lookup("reason") == nil {
		t.Fatalf("acknowledge-landed-failed flags = %#v err=%v", ack, err)
	}
	seal, _, err := command.Find([]string{"seal-validation-failed"})
	if err != nil || seal == nil || seal.Flags().Lookup("apply") == nil || seal.Flags().Lookup("actor") == nil || seal.Flags().Lookup("reason") == nil || seal.Flags().Lookup("model") == nil {
		t.Fatalf("seal-validation-failed flags = %#v err=%v", seal, err)
	}
	supersede, _, err := command.Find([]string{"supersede-validation-failed"})
	if err != nil || supersede == nil || supersede.Flags().Lookup("apply") == nil || supersede.Flags().Lookup("actor") == nil || supersede.Flags().Lookup("reason") == nil {
		t.Fatalf("supersede-validation-failed flags = %#v err=%v", supersede, err)
	}
	correct, _, err := command.Find([]string{"correct-self-supersession"})
	if err != nil || correct == nil || correct.Flags().Lookup("apply") == nil || correct.Flags().Lookup("actor") == nil || correct.Flags().Lookup("reason") == nil || correct.Flags().Lookup("expected-supersession-sha256") == nil || correct.Flags().Lookup("expected-immutable-claim-sha256") == nil {
		t.Fatalf("correct-self-supersession flags = %#v err=%v", correct, err)
	}
	for _, phrase := range []string{"prepared locally, not landed", "never force-push", "exact remote target", "forward revert", "forward repair", "acknowledge-landed-failed", "seal-validation-failed", "supersede-validation-failed"} {
		if !strings.Contains(command.Long, phrase) {
			t.Errorf("merge help is missing %q", phrase)
		}
	}
}

func TestWorktreeMergeReceiptCollisionCommandRequiresExpectedEvidence(t *testing.T) {
	command := newWorktreeMergeCmd()
	child, _, err := command.Find([]string{"acknowledge-receipt-collision"})
	if err != nil || child == nil {
		t.Fatalf("find collision acknowledgement command: child=%v err=%v", child, err)
	}
	for _, flag := range []string{"expected-receipt-sha256", "expected-immutable-claim-sha256", "expected-target", "expected-candidate", "expected-current-source", "expected-historical-refresh-source"} {
		if child.Flags().Lookup(flag) == nil {
			t.Errorf("collision acknowledgement is missing --%s", flag)
		}
	}
	command.SetArgs([]string{"acknowledge-receipt-collision", "receipt.json"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "all expected receipt, claim, target, candidate, current-source, and historical-source identities are required") {
		t.Fatalf("missing collision evidence error = %v", err)
	}
}

func TestWorktreeMergeCorrectSelfSupersessionCommandRequiresExpectedEvidence(t *testing.T) {
	command := newWorktreeMergeCmd()
	command.SetArgs([]string{"correct-self-supersession", "receipt.json", "replacement-worktree"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--expected-supersession-sha256 and --expected-immutable-claim-sha256 are required") {
		t.Fatalf("missing self-supersession evidence error = %v", err)
	}
}

func TestWorktreeMergePublishedForwardRepairCommandRequiresPinnedEvidence(t *testing.T) {
	command := newWorktreeMergeCmd()
	child, _, err := command.Find([]string{"prepare-published-forward-repair"})
	if err != nil || child == nil {
		t.Fatalf("find published forward-repair command: child=%v err=%v", child, err)
	}
	for _, flag := range []string{"expected-receipt-sha256", "expected-immutable-claim-sha256", "expected-supersession-sha256", "expected-current-target", "expected-source-sha", "apply", "actor", "reason"} {
		if child.Flags().Lookup(flag) == nil {
			t.Errorf("published forward-repair is missing --%s", flag)
		}
	}
	for _, phrase := range []string{"historical ancestry root", "historical worktrees need not remain live", "current WB-managed worktree", "exact active claim"} {
		if !strings.Contains(child.Long, phrase) {
			t.Errorf("published forward-repair help is missing %q", phrase)
		}
	}
	command.SetArgs([]string{"prepare-published-forward-repair", "receipt.json", "source-worktree"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "expected receipt, immutable claim, self-supersession, current target, and one expected SHA per source") {
		t.Fatalf("missing published forward-repair evidence error = %v", err)
	}
}

func TestValidateWorktreeMergeFlagsStopBeforeMerge(t *testing.T) {
	tests := []struct {
		name    string
		flags   worktreeMergeFlags
		wantErr string
	}{
		{
			name:    "requires pull request route",
			flags:   worktreeMergeFlags{format: "text", route: "auto", onFailure: "stop", timeout: time.Second, stopBeforeMerge: true},
			wantErr: "requires --route pr",
		},
		{
			name:    "cannot clean before merge",
			flags:   worktreeMergeFlags{format: "text", route: "pr", onFailure: "stop", timeout: time.Second, cleanup: true, stopBeforeMerge: true},
			wantErr: "cannot be combined with --cleanup",
		},
		{
			name:  "valid PR handoff",
			flags: worktreeMergeFlags{format: "text", route: "pr", onFailure: "stop", timeout: time.Second, stopBeforeMerge: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorktreeMergeFlags(tt.flags)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateWorktreeMergeFlags() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestWorktreeMergeRecoveryApplyUsesAdmissionFlags(t *testing.T) {
	t.Run("acknowledge-landed-failed", func(t *testing.T) {
		fixture := newCLIWorktreeMergeFixture(t, 1)
		originalReceipt := readCLIFile(t, fixture.receiptPath)
		runCLIWorktreeGit(t, fixture.canonical, "update-ref", "refs/heads/main", fixture.receipt.Candidate.SHA)
		runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "acknowledge-landed-failed", fixture.receiptPath,
			"--apply", "--actor", "test-operator", "--reason", "regression-test",
			"--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("production acknowledge apply failed: %v\nstderr: %s", err, stderr.String())
		}
		var acknowledgement orchestrate.WorktreeMergeLandedFailureAcknowledgement
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("decode acknowledge output %q: %v", stdout.String(), err)
		}
		assertCLIWorktreeMergeAcknowledgement(t, fixture.receiptPath, originalReceipt, acknowledgement.AcknowledgementPath)
	})

	t.Run("supersede-validation-failed", func(t *testing.T) {
		fixture := newCLIWorktreeMergeFixture(t, 2)
		originalReceipt := readCLIFile(t, fixture.receiptPath)
		for _, source := range fixture.sources {
			runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
		}
		writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
		runCLIWorktreeGit(t, fixture.canonical, "add", "target.txt")
		runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: advance target for CLI supersession")
		runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")
		replacement := createCLIWorktreeSource(t, fixture, "supersede-replacement", "feature/supersede-replacement", "replacement.txt", "replacement\n")
		runCLIWorktreeGit(t, replacement.WorktreeDir, "fetch", "origin")
		for _, source := range fixture.sources {
			runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+source.Branch)
		}

		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "supersede-validation-failed", fixture.receiptPath, replacement.WorktreeDir,
			"--apply", "--actor", "test-operator", "--reason", "regression-test",
			"--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("production supersede apply failed: %v\nstderr: %s", err, stderr.String())
		}
		var acknowledgement orchestrate.WorktreeMergeValidationFailureSupersession
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("decode supersede output %q: %v", stdout.String(), err)
		}
		assertCLIWorktreeMergeAcknowledgement(t, fixture.receiptPath, originalReceipt, acknowledgement.AcknowledgementPath)
	})
}

type cliWorktreeMergeFixture struct {
	projectsRoot string
	canonical    string
	receiptPath  string
	receipt      orchestrate.WorktreeMergeReceipt
	sources      []worktrees.CreateResult
}

func newCLIWorktreeMergeFixture(t *testing.T, sourceCount int) cliWorktreeMergeFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, ".wb"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	writeCLIWorktreeFile(t, filepath.Join(seed, "initial.txt"), "initial\n")
	runCLIWorktreeGit(t, seed, "init", "-b", "main")
	runCLIWorktreeGit(t, seed, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, seed, "config", "user.email", "wb@example.test")
	runCLIWorktreeGit(t, seed, "add", "-A")
	runCLIWorktreeGit(t, seed, "commit", "-m", "initial")
	runCLIWorktreeGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, root, "clone", remote, canonical)
	runCLIWorktreeGit(t, canonical, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, canonical, "config", "user.email", "wb@example.test")

	fixture := cliWorktreeMergeFixture{projectsRoot: projectsRoot, canonical: canonical}
	for i := 0; i < sourceCount; i++ {
		task := "cli-supersede-source-" + string(rune('a'+i))
		branch := "feature/cli-supersede-" + string(rune('a'+i))
		fixture.sources = append(fixture.sources, createCLIWorktreeSource(t, fixture, task, branch, task+".txt", task+"\n"))
	}
	sourcePaths := make([]string, 0, len(fixture.sources))
	for _, source := range fixture.sources {
		sourcePaths = append(sourcePaths, source.WorktreeDir)
	}
	receipt, err := orchestrate.PrepareWorktreeMerge(context.Background(), orchestrate.WorktreeMergePrepareOptions{
		ProjectsRoot: projectsRoot, Sources: sourcePaths, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = orchestrate.WorktreeMergeValidationFailed
	receipt.Failure = "historical validation failure"
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(receipt.ReceiptPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.receipt = receipt
	fixture.receiptPath = receipt.ReceiptPath
	return fixture
}

func createCLIWorktreeSource(t *testing.T, fixture cliWorktreeMergeFixture, task, branch, name, contents string) worktrees.CreateResult {
	t.Helper()
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("CLI recovery command fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, Branch: branch, BranchChosen: true, Base: "main",
		WorkLog: worktrees.WorkLogOptions{
			EffortID: task, RunID: task + "-run", Initiator: "test", AgentID: task, AgentRuntime: "test", Model: "test-model",
			OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	writeCLIWorktreeFile(t, filepath.Join(result.WorktreeDir, name), contents)
	runCLIWorktreeGit(t, result.WorktreeDir, "add", name)
	runCLIWorktreeGit(t, result.WorktreeDir, "commit", "-m", "feat: add "+name)
	return result
}

func assertCLIWorktreeMergeAcknowledgement(t *testing.T, receiptPath string, originalReceipt []byte, acknowledgementPath string) {
	t.Helper()
	if strings.TrimSpace(acknowledgementPath) == "" {
		t.Fatal("production command returned an empty acknowledgement path")
	}
	if _, err := os.Stat(acknowledgementPath); err != nil {
		t.Fatalf("acknowledgement artifact missing: %v", err)
	}
	if got := readCLIFile(t, receiptPath); string(got) != string(originalReceipt) {
		t.Fatal("production command rewrote the historical receipt")
	}
}

func writeCLIWorktreeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func runCLIWorktreeGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
