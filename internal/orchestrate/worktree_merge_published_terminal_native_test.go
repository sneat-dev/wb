package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

func terminalOwnerAssertReleasedAndUnchanged(t *testing.T, root string, r WorktreeMergeReceipt, before []byte) {
	t.Helper()
	got, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(got) != string(before) {
		t.Fatalf("historical receipt changed: %v", err)
	}
	lock, err := AcquireOperationLock(root, r.Lane, true)
	if err != nil {
		t.Fatalf("owner kept operation lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // Existing GH fixture changes PATH, XDG_STATE_HOME and provider environment; rows reuse one private native repository sequentially.
func TestRetiredPublicationOwnerNativeStagesPreserveEvidenceAndRelease(t *testing.T) {
	f := newExplicitRootEngineFixture(t)
	original := buildStuckRetiredPublicationReceipt(t, f, "terminal-retired-source", "feature/terminal-retired", "terminal-retired.txt")
	runEngineGit(t, original.Candidate.Worktree, "push", "origin", "--delete", original.Candidate.Branch)
	target := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	installRetiredPublicationGH(t, f.repository.Slug, original.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", original.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", original.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")
	for _, mode := range []string{"dry run", "apply", "replay", "different proof", "malformed sidecar", "hash", "persist", "remote branch", "publication read", "fetch", "ancestry", "empty preserved SHA", "empty published SHA"} {
		t.Run(mode, func(t *testing.T) {
			r := original
			ackPath := retiredPublicationAcknowledgementPath(r.ReceiptPath)
			if err := os.Remove(ackPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if mode == "empty preserved SHA" {
				r.Candidate.SHA = ""
			}
			if mode == "empty published SHA" {
				r.PublishedCandidateSHA = ""
			}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			options := WorktreeMergeRetiredPublicationAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: true, Actor: "operator", Reason: "native proof"}
			if mode == "dry run" {
				options.Apply = false
			}
			sentinel := errors.New("selected retired " + mode)
			consumed := false
			hash := worktreeMergeReceiptSHA256
			persist := persistRetiredPublicationAcknowledgement
			run := defaultRunner
			want := ""
			remoteRestored := false
			switch mode {
			case "hash":
				hash = func(path string) (string, error) {
					if path == r.ReceiptPath {
						consumed = true
						return "", sentinel
					}
					return worktreeMergeReceiptSHA256(path)
				}
				want = sentinel.Error()
			case "persist":
				persist = func(path string, ack WorktreeMergeRetiredPublicationAcknowledgement) error {
					if path == ackPath {
						consumed = true
						return sentinel
					}
					return persistRetiredPublicationAcknowledgement(path, ack)
				}
				want = sentinel.Error()
			case "publication read", "fetch", "ancestry":
				args := []string{"ls-remote", "--heads", "origin", "refs/heads/" + r.Candidate.Branch}
				if mode == "fetch" {
					args = []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}
				}
				if mode == "ancestry" {
					args = []string{"merge-base", r.PublishedCandidateSHA, target}
				}
				run = prepareOwnerObservedRunner{Runner: defaultRunner, before: func(_ context.Context, dir, name string, got []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(got, args) {
						consumed = true
						return sentinel
					}
					return nil
				}}
				want = sentinel.Error()
			case "remote branch":
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", r.Candidate.Branch)
				t.Cleanup(func() {
					if remoteRestored {
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, _, err := runCommand(ctx, defaultRunner, 0, 0, r.Candidate.Worktree, "git", "push", "origin", "--delete", r.Candidate.Branch); err != nil {
						t.Errorf("restore private candidate publication: %v", err)
					}
				})
				want = "still carries a remote ref"
			case "malformed sidecar":
				if err := os.WriteFile(ackPath, []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "decode retired-publication acknowledgement"
			case "replay", "different proof":
				ack, err := AcknowledgeRetiredPublication(t.Context(), WorktreeMergeRetiredPublicationAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: options.Actor, Reason: options.Reason})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "different proof" {
					ack.CurrentTargetSHA = r.TargetSHA
					ack.ID = retiredPublicationAcknowledgementID(ack)
					want = "binds different immutable evidence"
				}
				if err := persistRetiredPublicationAcknowledgement(ackPath, ack); err != nil {
					t.Fatal(err)
				}
			}
			ack, err := acknowledgeRetiredPublication(t.Context(), options, run, readWorktreeMergeReceipt, hash, persist, githubRead)
			if want == "" {
				if err != nil || ack.CurrentTargetSHA != target || ack.PullRequestState != "CLOSED" {
					t.Fatalf("native %s=%+v %v", mode, ack, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("native %s error=%v want %q", mode, err, want)
			}
			if mode == "hash" || mode == "persist" || mode == "publication read" || mode == "fetch" || mode == "ancestry" {
				if !consumed || !errors.Is(err, sentinel) {
					t.Fatalf("named observation consumed=%v error=%v", consumed, err)
				}
			}
			terminalOwnerAssertReleasedAndUnchanged(t, f.githubDir, r, before)
			remote := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "ls-remote", "--heads", "origin", "refs/heads/"+r.Candidate.Branch))
			if mode == "remote branch" {
				if !strings.HasPrefix(remote, r.Candidate.SHA+"\t") {
					t.Fatalf("existing publication changed: %s", remote)
				}
				runEngineGit(t, r.Candidate.Worktree, "push", "origin", "--delete", r.Candidate.Branch)
				remoteRestored = true
			} else if remote != "" {
				t.Fatalf("acknowledgement republished candidate: %s", remote)
			}
			if mode == "dry run" || mode == "hash" || mode == "persist" || mode == "publication read" || mode == "fetch" || mode == "ancestry" || mode == "remote branch" {
				if _, statErr := os.Stat(ackPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("refusal/dry run wrote sidecar: %v", statErr)
				}
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(ackPath), ".retired-publication-ack-*.tmp"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("staging artifacts=%v %v", matches, err)
			}
		})
	}
}

// terminalOwnerProviderFailure installs only an exact negative read; every
// other provider operation delegates the original native-DAG-backed script.
func terminalOwnerProviderFailure(t *testing.T) string {
	t.Helper()
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	script := filepath.Join(bin, "gh")
	native := filepath.Join(bin, "gh-native")
	if err := os.Rename(script, native); err != nil {
		t.Fatal(err)
	}
	fault := filepath.Join(bin, "selected-read")
	if err := os.WriteFile(fault, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nset -eu\nif [ \"$*\" = \"$(cat '" + fault + "')\" ]; then echo 'selected exact terminal provider read' >&2; exit 1; fi\nexec '" + native + "' \"$@\"\n"
	if err := testenv.WriteExecutableFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return fault
}

//nolint:paralleltest // Existing GH fixture changes PATH/XDG/provider environment; all rows share one private remote sequentially.
func TestStrandedLandingOwnerNativeRemoteOnlyStagesPreserveEvidence(t *testing.T) {
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "terminal-stranded-source", "feature/terminal-stranded", "terminal-stranded.txt", "native source\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = WorktreeMergePhaseLand
	r.Status = WorktreeMergeConflict
	r.PullRequest = "https://example.test/acme/app/pull/91"
	r.PublishedCandidateSHA = r.Candidate.SHA
	r.Failure = "historical landing observation missing"
	runEngineGit(t, f.canonical, "merge", "--ff-only", r.Candidate.SHA)
	runEngineGit(t, f.canonical, "push", "origin", "main")
	tree := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", r.Candidate.SHA+"^{tree}"))
	descendant := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "commit-tree", tree, "-p", r.Candidate.SHA, "-m", "test: native terminal descendant"))
	unrelated := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "commit-tree", tree, "-p", r.TargetSHA, "-m", "test: native tree-identical squash"))
	runEngineGit(t, r.Candidate.Worktree, "push", "origin", descendant+":refs/heads/terminal-descendant", unrelated+":refs/heads/terminal-squash")
	if err := os.RemoveAll(r.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	installStrandedLandingGH(t, r.PullRequest, f.repository.CloneURL)
	fault := terminalOwnerProviderFailure(t)
	original := r
	for _, mode := range []string{"dry run", "apply", "replay", "different proof", "malformed sidecar", "hash", "persist", "strict descendant", "head unrelated", "head provider", "target provider", "merge unrelated", "merge provider", "observed head absent", "observed head provider", "tree identical", "tree provider", "original target absent", "original target provider"} {
		t.Run(mode, func(t *testing.T) {
			r := original
			remoteTarget, head, merge := r.Candidate.SHA, r.Candidate.SHA, r.Candidate.SHA
			want := ""
			faultArgs := ""
			ackPath := strandedLandingAcknowledgementPath(r.ReceiptPath)
			if err := os.Remove(ackPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			switch mode {
			case "strict descendant":
				head = descendant
				remoteTarget = descendant
			case "head unrelated":
				head = unrelated
				want = "does not contain exact receipted candidate"
			case "head provider":
				head = descendant
				faultArgs = "api repos/acme/app/compare/" + r.Candidate.SHA + "..." + head + " --include"
				want = "selected exact terminal provider read"
			case "target provider":
				faultArgs = "api repos/acme/app/git/ref/heads/main --include"
				want = "selected exact terminal provider read"
			case "merge unrelated":
				merge = unrelated
				want = "does not contain proved merge commit"
			case "merge provider":
				faultArgs = "api repos/acme/app/compare/" + merge + "..." + remoteTarget + " --include"
				want = "selected exact terminal provider read"
			case "observed head absent":
				head = descendant
				want = "does not contain observed pull request head"
			case "observed head provider":
				head = descendant
				faultArgs = "api repos/acme/app/compare/" + head + "..." + remoteTarget + " --include"
				want = "selected exact terminal provider read"
			case "tree identical":
				merge = unrelated
				remoteTarget = unrelated
			case "tree provider":
				merge = unrelated
				remoteTarget = unrelated
				faultArgs = "api repos/acme/app/git/commits/" + merge + " --include"
				want = "selected exact terminal provider read"
			case "original target absent":
				r.TargetSHA = descendant
				want = "no longer contains its own recorded pre-merge target"
			case "original target provider":
				faultArgs = "api repos/acme/app/compare/" + r.TargetSHA + "..." + r.Candidate.SHA + " --include"
				want = "selected exact terminal provider read"
			case "hash", "persist":
				want = "selected stranded " + mode
			case "malformed sidecar":
				want = "decode stranded-landing acknowledgement"
			case "different proof":
				want = "binds different landing or target evidence"
			}
			runEngineGit(t, f.canonical, "--git-dir="+f.repository.CloneURL, "update-ref", "refs/heads/main", remoteTarget)
			t.Setenv("WB_TEST_PR_STATE", "MERGED")
			t.Setenv("WB_TEST_CANDIDATE_SHA", r.Candidate.SHA)
			t.Setenv("WB_TEST_PR_HEAD_SHA", head)
			t.Setenv("WB_TEST_MERGE_COMMIT_SHA", merge)
			if err := os.WriteFile(fault, []byte(faultArgs), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(r.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			options := WorktreeMergeStrandedLandingAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Apply: mode != "dry run", Actor: "operator", Reason: "native remote proof"}
			hash := worktreeMergeReceiptSHA256
			persist := persistStrandedLandingAcknowledgement
			sentinel := errors.New(want)
			consumed := false
			if mode == "hash" {
				hash = func(path string) (string, error) {
					if path == r.ReceiptPath {
						consumed = true
						return "", sentinel
					}
					return worktreeMergeReceiptSHA256(path)
				}
			}
			if mode == "persist" {
				persist = func(path string, ack WorktreeMergeStrandedLandingAcknowledgement) error {
					if path == ackPath {
						consumed = true
						return sentinel
					}
					return persistStrandedLandingAcknowledgement(path, ack)
				}
			}
			if mode == "malformed sidecar" {
				if err := os.WriteFile(ackPath, []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "replay" || mode == "different proof" {
				ack, err := AcknowledgeStrandedPullRequestLanding(t.Context(), WorktreeMergeStrandedLandingAcknowledgementOptions{ProjectsRoot: f.githubDir, Receipt: r.ReceiptPath, Actor: options.Actor, Reason: options.Reason})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "different proof" {
					ack.CurrentTargetSHA = r.TargetSHA
					ack.ID = strandedLandingAcknowledgementID(ack)
				}
				if err := persistStrandedLandingAcknowledgement(ackPath, ack); err != nil {
					t.Fatal(err)
				}
			}
			ack, err := acknowledgeStrandedPullRequestLanding(t.Context(), options, readWorktreeMergeReceipt, hash, persist, githubRead)
			if want == "" {
				if err != nil || ack.CurrentTargetSHA != remoteTarget || ack.ProvedLandingSHA != merge {
					t.Fatalf("native remote-only %s=%+v %v", mode, ack, err)
				}
				if mode == "tree identical" && (ack.CandidateLanding != "tree-identical" || ack.CandidateLandingTreeSHA != tree) {
					t.Fatalf("actual shared native tree proof=%+v", ack)
				}
				if mode == "strict descendant" && ack.PullRequestHeadSHA != descendant {
					t.Fatalf("native observed descendant=%+v", ack)
				}
			} else if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("remote-only %s=%v want %q", mode, err, want)
			}
			if mode == "hash" || mode == "persist" {
				if !consumed || !errors.Is(err, sentinel) {
					t.Fatalf("named stage consumed=%v identity=%v", consumed, err)
				}
			}
			terminalOwnerAssertReleasedAndUnchanged(t, f.githubDir, r, before)
			if _, statErr := os.Stat(r.Candidate.Worktree); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("remote proof recreated candidate: %v", statErr)
			}
			got := strings.TrimSpace(runEngineGit(t, f.canonical, "--git-dir="+f.repository.CloneURL, "rev-parse", "refs/heads/main"))
			if got != remoteTarget {
				t.Fatalf("acknowledgement changed native target: %s", got)
			}
			if mode == "dry run" || mode == "hash" || mode == "persist" || faultArgs != "" || strings.HasSuffix(mode, "absent") || mode == "head unrelated" || mode == "merge unrelated" {
				if _, statErr := os.Stat(ackPath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("refusal/dry run wrote sidecar: %v", statErr)
				}
			}
		})
	}
}
