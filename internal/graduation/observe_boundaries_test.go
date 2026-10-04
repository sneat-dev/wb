package graduation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func observerEvidenceFixture(t *testing.T) (EvidencePaths, time.Time) {
	t.Helper()
	inputs, now := validInputs()
	directory := t.TempDir()
	paths := EvidencePaths{LocalCheck: filepath.Join(directory, "local.json"), CIWait: filepath.Join(directory, "ci.json"), RemoteTarget: filepath.Join(directory, "remote.json"), DeployedRevision: filepath.Join(directory, "deployment.json"), TerminalCleanup: filepath.Join(directory, "cleanup.json")}
	for path, value := range map[string]any{paths.LocalCheck: inputs.LocalCheck, paths.CIWait: inputs.CIWait, paths.RemoteTarget: inputs.RemoteTarget, paths.DeployedRevision: inputs.DeployedRevision, paths.TerminalCleanup: inputs.TerminalCleanup} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths, now
}
func TestComposeFilesReportsEachMissingEvidenceBeforeProducingReceipt(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"local", "ci", "remote", "deployment", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			paths, now := observerEvidenceFixture(t)
			var target *string
			switch stage {
			case "local":
				target = &paths.LocalCheck
			case "ci":
				target = &paths.CIWait
			case "remote":
				target = &paths.RemoteTarget
			case "deployment":
				target = &paths.DeployedRevision
			case "cleanup":
				target = &paths.TerminalCleanup
			}
			*target = filepath.Join(t.TempDir(), "missing.json")
			raw, err := (Observer{Now: func() time.Time { return now }}).ComposeFiles(paths)
			if err == nil || !strings.Contains(err.Error(), "open --") || raw != nil {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
		})
	}
}
func TestComposeFilesRetainsCompositionAndEncodingFailure(t *testing.T) {
	t.Parallel()
	for _, encoding := range []bool{false, true} {
		t.Run(fmt.Sprint(encoding), func(t *testing.T) {
			t.Parallel()
			paths, now := observerEvidenceFixture(t)
			if encoding {
				now = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			} else {
				inputs, _ := validInputs()
				inputs.LocalCheck.Repositories[0].Revision = strings.Repeat("b", 40)
				raw, err := json.Marshal(inputs.LocalCheck)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.LocalCheck, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := (Observer{Now: func() time.Time { return now }}).ComposeFiles(paths)
			if err == nil || raw != nil {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
			if !encoding && !strings.Contains(err.Error(), "compose graduation receipt") {
				t.Fatalf("composition error=%v", err)
			}
		})
	}
}
func TestObserveRemoteTargetRejectsInvalidRequestsAndReportsGitPhase(t *testing.T) {
	t.Parallel()
	boom := errors.New("native boundary failure")
	for _, test := range []struct{ name, repository, remote, target, fail, want string }{
		{"repository", "bad", "origin", "main", "", "--repo"}, {"remote", "owner/repo", "-option", "main", "", "--remote"}, {"blank target", "owner/repo", "origin", "", "", "--target"}, {"padded target", "owner/repo", "origin", " main ", "", "--target"},
		{"invalid branch", "owner/repo", "origin", "main", "check-ref-format", "valid Git branch"}, {"remote lookup", "owner/repo", "origin", "main", "remote", "resolve remote"}, {"remote observation", "owner/repo", "origin", "main", "ls-remote", "observe origin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observer := Observer{Now: time.Now, RunGit: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] == test.fail {
					return nil, boom
				}
				if args[0] == "remote" {
					return []byte("git@github.com:owner/repo.git\n"), nil
				}
				return []byte(strings.Repeat("a", 40) + "\trefs/heads/main\n"), nil
			}}
			raw, err := observer.ObserveRemoteTarget(context.Background(), RemoteTargetRequest{Repository: test.repository, Remote: test.remote, Target: test.target, RepositoryPath: t.TempDir()})
			if raw != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("raw=%q err=%v", raw, err)
			}
		})
	}
	observer := Observer{resolvePath: func(string) (string, error) { return "", boom }}
	if _, err := observer.ObserveRemoteTarget(context.Background(), RemoteTargetRequest{Repository: "owner/repo", Remote: "origin", Target: "main"}); !errors.Is(err, boom) {
		t.Fatalf("path error=%v", err)
	}
}
func TestObserveRemoteTargetPersistsExactEvidenceBeforeReturningAndPreservesWriteFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"", "write", "encode"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			output := filepath.Join(t.TempDir(), "remote.json")
			if failure == "write" {
				if err := os.WriteFile(output, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			observer := Observer{Now: func() time.Time {
				if failure == "encode" {
					return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				return time.Now()
			}, RunGit: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch args[0] {
				case "remote":
					return []byte("git@github.com:owner/repo.git\n"), nil
				case "ls-remote":
					return []byte(strings.Repeat("A", 64) + "\trefs/heads/main\n"), nil
				}
				return nil, nil
			}}
			raw, err := observer.ObserveRemoteTarget(context.Background(), RemoteTargetRequest{Repository: "owner/repo", RepositoryPath: t.TempDir(), Remote: "origin", Target: "main", Output: output})
			if failure != "" {
				if err == nil || raw != nil {
					t.Fatalf("raw=%q err=%v", raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := os.ReadFile(output)
			if err != nil || !bytes.Equal(raw, persisted) {
				t.Fatalf("persisted=%q err=%v", persisted, err)
			}
			var evidence RemoteTargetEvidence
			if err := json.Unmarshal(raw, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.Revision != strings.Repeat("a", 64) {
				t.Fatalf("revision=%q", evidence.Revision)
			}
		})
	}
}

type evidenceReadFailure struct {
	info             os.FileInfo
	statErr, readErr error
}

func (file evidenceReadFailure) Stat() (os.FileInfo, error) { return file.info, file.statErr }
func (file evidenceReadFailure) Read([]byte) (int, error)   { return 0, file.readErr }
func TestEvidenceDescriptorFailuresAndSizeLimitRemainExplicit(t *testing.T) {
	t.Parallel()
	boom := errors.New("descriptor failure")
	if _, err := readEvidenceFile(evidenceReadFailure{statErr: boom}, "--local-check"); !errors.Is(err, boom) {
		t.Fatalf("stat error=%v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readEvidenceFile(evidenceReadFailure{info: info, readErr: boom}, "--ci-wait"); !errors.Is(err, boom) {
		t.Fatalf("read error=%v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxEvidenceBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEvidence(path, "--remote-target"); err == nil {
		t.Fatal("oversized evidence accepted")
	}
}
func TestDefaultObserverUsesNativeGitAndClock(t *testing.T) {
	t.Parallel()
	observer := DefaultObserver()
	if observer.Now().IsZero() {
		t.Fatal("default clock is zero")
	}
	directory := t.TempDir()
	output, err := observer.RunGit(context.Background(), directory, "--version")
	if err != nil || !strings.HasPrefix(string(output), "git version ") {
		t.Fatalf("native git output=%q err=%v", output, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := observer.RunGit(ctx, directory, "--version"); err == nil {
		t.Fatal("cancelled context reached native Git")
	}
}

func TestObserveRemoteTargetReadsRealLocalGitRemoteAtExactBranch(t *testing.T) {
	t.Parallel()
	observer := DefaultObserver()
	directory := t.TempDir()
	run := func(path string, args ...string) []byte {
		t.Helper()
		raw, err := observer.RunGit(context.Background(), path, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return raw
	}
	remote := filepath.Join(directory, "remote.git")
	run(directory, "init", "--bare", remote)
	checkout := filepath.Join(directory, "checkout")
	run(directory, "init", "-b", "main", checkout)
	run(checkout, "-c", "user.name=Receipt Test", "-c", "user.email=receipt@example.test", "commit", "--allow-empty", "-m", "receipt")
	run(checkout, "remote", "add", "origin", "git@github.com:owner/repo.git")
	run(checkout, "push", remote, "main")
	revision := strings.TrimSpace(string(run(checkout, "rev-parse", "HEAD")))
	native := observer.RunGit
	observer.RunGit = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ls-remote" {
			args = append([]string{"-c", "url." + remote + ".insteadOf=git@github.com:owner/repo.git"}, args...)
		}
		return native(ctx, path, args...)
	}
	raw, err := observer.ObserveRemoteTarget(context.Background(), RemoteTargetRequest{Repository: "owner/repo", RepositoryPath: checkout, Remote: "origin", Target: "main"})
	if err != nil {
		t.Fatal(err)
	}
	var evidence RemoteTargetEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Revision != revision || evidence.TargetRef != "refs/heads/main" || evidence.ObservedOutput != revision+"\trefs/heads/main\n" || evidence.ObservedOutputSHA256 != Digest([]byte(evidence.ObservedOutput)) {
		t.Fatalf("evidence=%+v", evidence)
	}
}

func TestDefaultObserverRejectsMissingEvidenceAndRepository(t *testing.T) {
	t.Parallel()
	observer := DefaultObserver()
	if _, err := observer.ComposeFiles(EvidencePaths{}); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if _, err := observer.ObserveRemoteTarget(context.Background(), RemoteTargetRequest{}); err == nil {
		t.Fatal("missing repository accepted")
	}
}
