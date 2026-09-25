package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/graduation"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// errBoomForCmdWB is a shared sentinel used across this package's
// task-9 PR-2 fault-injection tests (each migrated cmd/wb call site
// threads a *filewrite.Injector through so a test can force one write/
// sync/chmod/rename/close step to fail deterministically).
var errBoomForCmdWB = errors.New("boom")

func TestVerifyReceiptComposesExactMachineReadableEvidence(t *testing.T) {
	paths, now := writeGraduationEvidence(t)
	outputPath := filepath.Join(t.TempDir(), "graduation-receipt.json")
	command := newVerifyReceiptCmdWithDeps(graduationCommandDeps{now: func() time.Time { return now }})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--local-check", paths.localCheck, "--ci-wait", paths.ciWait, "--remote-target", paths.remoteTarget, "--deployed-revision", paths.deployed, "--terminal-cleanup", paths.cleanup, "--output", outputPath})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var receipt graduation.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Repository != "sneat-dev/wb" || receipt.Revision != strings.Repeat("a", 40) ||
		receipt.LocalCI.LocalCheck.SHA256 != graduation.Digest(readReceiptTestFile(t, paths.localCheck)) ||
		receipt.RemoteTarget.Evidence.TargetRef != "refs/heads/main" || !receipt.TerminalCleanup.Evidence.Results[0].WorktreeGone {
		t.Fatalf("receipt=%#v", receipt)
	}
	if persisted := readReceiptTestFile(t, outputPath); !bytes.Equal(persisted, stdout.Bytes()) {
		t.Fatalf("persisted receipt differs from stdout\n got %s\nwant %s", persisted, stdout.Bytes())
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&^0o644 != 0 {
		t.Fatalf("receipt output permissions info=%v err=%v", info, err)
	}
}

func TestVerifyReceiptRefusesOverwrite(t *testing.T) {
	paths, now := writeGraduationEvidence(t)
	outputPath := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(outputPath, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := newVerifyReceiptCmdWithDeps(graduationCommandDeps{now: func() time.Time { return now }})
	command.SetArgs([]string{"--local-check", paths.localCheck, "--ci-wait", paths.ciWait, "--remote-target", paths.remoteTarget, "--deployed-revision", paths.deployed, "--terminal-cleanup", paths.cleanup, "--output", outputPath})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("overwrite error=%v", err)
	}
	if got := string(readReceiptTestFile(t, outputPath)); got != "preserve" {
		t.Fatalf("existing receipt overwritten: %q", got)
	}
}

func TestVerifyReceiptRemoteTargetUsesFixedGitObservation(t *testing.T) {
	revision := strings.Repeat("a", 40)
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	repositoryPath := t.TempDir()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", repositoryPath, "check-ref-format", "--branch", "main"}, runner.Result{}, nil)
	fake.ExpectArgv([]string{"git", "-C", repositoryPath, "remote", "get-url", "origin"}, runner.Result{Stdout: "git@github.com:sneat-dev/wb.git\n"}, nil)
	fake.ExpectArgv([]string{"git", "-C", repositoryPath, "ls-remote", "--refs", "origin", "refs/heads/main"}, runner.Result{Stdout: revision + "\trefs/heads/main\n"}, nil)
	deps := graduationCommandDeps{
		now:    func() time.Time { return now },
		runner: fake,
	}
	command := newVerifyReceiptCmdWithDeps(deps)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"remote-target", "--repo", "sneat-dev/wb", "--repository-path", repositoryPath, "--remote", "origin", "--target", "main"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var evidence graduation.RemoteTargetEvidence
	if err := json.Unmarshal(stdout.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Producer != graduation.RemoteTargetProducer || evidence.Revision != revision || evidence.ObservedAt != now || evidence.ObservedOutputSHA256 != graduation.Digest([]byte(evidence.ObservedOutput)) {
		t.Fatalf("remote evidence=%#v", evidence)
	}
	if got := fake.CallCount(); got != 3 {
		t.Fatalf("git calls = %d, want 3 (calls=%+v)", got, fake.Calls())
	}
}

func TestVerifyReceiptRemoteTargetRejectsRepositoryAndPayloadMismatch(t *testing.T) {
	for name, remoteURL := range map[string]string{
		"different repository": "git@github.com:sneat-dev/other.git\n",
		"embedded credential":  "https://token@github.com/sneat-dev/wb.git\n",
	} {
		t.Run(name, func(t *testing.T) {
			fake := runnertest.New(t)
			fake.Expect(gitTailMatch("check-ref-format", "--branch", "main"), runner.Result{}, nil)
			fake.Expect(gitTailMatch("remote", "get-url", "origin"), runner.Result{Stdout: remoteURL}, nil)
			deps := graduationCommandDeps{now: time.Now, runner: fake}
			command := newVerifyReceiptCmdWithDeps(deps)
			command.SetArgs([]string{"remote-target", "--repo", "sneat-dev/wb", "--target", "main"})
			if err := command.Execute(); err == nil {
				t.Fatal("mismatched or credential-bearing remote was accepted")
			}
		})
	}
}

func TestVerifyReceiptRemoteTargetRejectsOptionLikeRemoteName(t *testing.T) {
	// An unscripted runnertest.Fake fails the test the moment anything calls
	// it (runnertest.go: "no script matched"), so no explicit assertion is
	// needed: reaching git at all fails this test.
	command := newVerifyReceiptCmdWithDeps(graduationCommandDeps{now: time.Now, runner: runnertest.New(t)})
	command.SetArgs([]string{"remote-target", "--repo", "sneat-dev/wb", "--remote=--upload-pack=evil", "--target", "main"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "safe configured remote") {
		t.Fatalf("unsafe remote error=%v", err)
	}
}

// gitTailMatch matches a runnertest.Call for `git -C <any directory> tail...`,
// ignoring the directory: several verify-receipt tests exercise git calls
// without pinning --repository-path to one exact resolved path.
func gitTailMatch(tail ...string) func(runnertest.Call) bool {
	return func(c runnertest.Call) bool {
		if c.Name != "git" || len(c.Args) < 2 || c.Args[0] != "-C" {
			return false
		}
		got := c.Args[2:]
		if len(got) != len(tail) {
			return false
		}
		for i, want := range tail {
			if got[i] != want {
				return false
			}
		}
		return true
	}
}

type graduationEvidencePaths struct {
	localCheck, ciWait, remoteTarget, deployed, cleanup string
}

func writeGraduationEvidence(t *testing.T) (graduationEvidencePaths, time.Time) {
	t.Helper()
	directory := t.TempDir()
	revision := strings.Repeat("a", 40)
	localAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	ciAt := localAt.Add(time.Minute)
	remoteAt := ciAt.Add(time.Minute)
	deployedAt := remoteAt.Add(time.Minute)
	cleanupAt := deployedAt.Add(time.Minute)
	paths := graduationEvidencePaths{
		localCheck: filepath.Join(directory, "local-check.json"), ciWait: filepath.Join(directory, "ci-wait.json"),
		remoteTarget: filepath.Join(directory, "remote-target.json"), deployed: filepath.Join(directory, "deployed.json"), cleanup: filepath.Join(directory, "cleanup.json"),
	}
	localCheck := graduation.VerificationIndex{SchemaVersion: graduation.SchemaVersion, GeneratedAt: localAt, Profile: "ci", Checks: []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild}, Repositories: []quality.VerificationReport{{
		Repository: "sneat-dev/wb", Path: "/projects/sneat-dev/wb", Revision: revision, WorkspaceClean: true, Status: quality.StatusPassed,
		Results: []quality.VerificationEntry{{Check: quality.CheckLint, Status: quality.StatusPassed}, {Check: quality.CheckTest, Status: quality.StatusPassed}, {Check: quality.CheckBuild, Status: quality.StatusPassed}},
	}}}
	ciWait := graduation.CIWaitReceipt{SchemaVersion: graduation.SchemaVersion, ObservedAt: ciAt, PullRequestWaitResult: orchestrate.PullRequestWaitResult{Status: orchestrate.PullRequestWaitPassed, Repository: "sneat-dev/wb", Target: "main", Head: revision, ObservedHead: revision, ObservedTargetHead: revision, CandidateContainsTarget: true, Checks: []orchestrate.RemoteCheck{{Name: "test", Bucket: "pass"}}, RequiredChecksAuthority: "github-rulesets", StableObservations: 2}}
	remoteOutput := revision + "\trefs/heads/main\n"
	remoteTarget := graduation.RemoteTargetEvidence{SchemaVersion: graduation.SchemaVersion, Producer: graduation.RemoteTargetProducer, Repository: "sneat-dev/wb", Remote: "origin", RemoteURL: "git@github.com:sneat-dev/wb.git", TargetRef: "refs/heads/main", Revision: revision, ObservedAt: remoteAt, ObservedOutput: remoteOutput, ObservedOutputSHA256: graduation.Digest([]byte(remoteOutput))}
	payload := `{"deployment":{"revision":"` + revision + `"}}`
	deployed := graduation.DeployedRevisionEvidence{SchemaVersion: graduation.SchemaVersion, Producer: graduation.DeploymentProducer, Provider: "github-actions", Repository: "sneat-dev/wb", RunURL: "https://github.com/sneat-dev/wb/actions/runs/42", Revision: revision, RevisionJSONPointer: "/deployment/revision", ObservedAt: deployedAt, PayloadJSON: payload, PayloadSHA256: graduation.Digest([]byte(payload))}
	cleanup := graduation.TerminalCleanupEvidence{GeneratedAt: cleanupAt, Phase: "applied", Task: "graduation", Apply: true, DeleteRemote: true, OlderThan: "24h0m0s", Results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "graduation", Repository: "sneat-dev/wb", CanonicalDir: "/projects/sneat-dev/wb", WorktreeDir: "/worktrees/graduation/sneat-dev/wb", Branch: "feature/graduation", Base: "main", HeadSHA: revision, RemoteHeadSHA: revision, RemoteTargetSHA: revision, IntegratedAtOrigin: true, Clean: true}, Eligible: true, Applied: true, RemoteDeleted: true, WorktreeGone: true, BranchDeleted: true}}}
	writeReceiptTestJSON(t, paths.localCheck, localCheck)
	writeReceiptTestJSON(t, paths.ciWait, ciWait)
	writeReceiptTestJSON(t, paths.remoteTarget, remoteTarget)
	writeReceiptTestJSON(t, paths.deployed, deployed)
	writeReceiptTestJSON(t, paths.cleanup, cleanup)
	return paths, cleanupAt.Add(time.Minute)
}

func TestWriteGraduationReceiptWritesTheRawBytes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeGraduationReceipt(path, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("content = %q, want %q", got, "payload")
	}
}

func TestWriteGraduationReceiptReportsAMissingOutputDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing", "receipt.json")
	if err := writeGraduationReceipt(path, []byte("payload")); err == nil {
		t.Fatal("writeGraduationReceipt under a missing directory = nil, want an error")
	}
}

func TestWriteGraduationReceiptReportsTheOutputDirectoryBeingAFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notADirectory := filepath.Join(dir, "notadir")
	if err := os.WriteFile(notADirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(notADirectory, "receipt.json")
	if err := writeGraduationReceipt(path, []byte("payload")); err == nil {
		t.Fatal("writeGraduationReceipt with a file where the directory should be = nil, want an error")
	}
}

func TestWriteGraduationReceiptReportsAnExistingReceiptFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeGraduationReceipt(path, []byte("payload")); err == nil {
		t.Fatal("writeGraduationReceipt over an existing file = nil, want an error")
	}
}

func TestWriteGraduationReceiptInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Name: path, Err: errBoomForCmdWB}
	if err := writeGraduationReceiptInjected(path, []byte("payload"), inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeGraduationReceiptInjected with injected write failure = %v, want errBoomForCmdWB", err)
	}
}

func TestWriteGraduationReceiptInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Name: path, Err: errBoomForCmdWB}
	if err := writeGraduationReceiptInjected(path, []byte("payload"), inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeGraduationReceiptInjected with injected sync failure = %v, want errBoomForCmdWB", err)
	}
}

func TestWriteGraduationReceiptInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Name: path, Err: errBoomForCmdWB}
	if err := writeGraduationReceiptInjected(path, []byte("payload"), inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeGraduationReceiptInjected with injected close failure = %v, want errBoomForCmdWB", err)
	}
}

func writeReceiptTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readReceiptTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
