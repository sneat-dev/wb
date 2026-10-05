//go:build e2e

package orchestrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestE2EPublishedAdoptionClosureEntryAndHeldReadRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"empty selector", "outside store", "malformed receipt", "held lane", "second read"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			projects := t.TempDir()
			home, err := wbhome.EnsureRoot(projects)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "reports", "worktree-merge", "receipt.json")
			r := WorktreeMergeReceipt{SchemaVersion: WorktreeMergeSchemaVersion, ReceiptPath: path, Lane: worktreeMergeLaneID("acme/app", "main")}
			if err := persistWorktreeMergeReceipt(r); err != nil {
				t.Fatal(err)
			}
			original := adoptionClosureBytes(t, path)
			options := WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: projects, Receipt: path, PullRequest: "7"}
			read := readWorktreeMergeReceipt
			sentinel := errors.New("selected held reread")
			consumed := false
			var held OperationLock
			switch stage {
			case "empty selector":
				options.Receipt = ""
			case "outside store":
				options.Receipt = filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(options.Receipt, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed receipt":
				if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "held lane":
				held, err = AcquireOperationLock(projects, r.Lane, true)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = held.Release() })
			case "second read":
				reads := 0
				read = func(p string) (WorktreeMergeReceipt, error) {
					if p != path {
						t.Fatalf("unexpected read %s", p)
					}
					reads++
					if reads == 2 {
						if probe, e := AcquireOperationLock(projects, r.Lane, true); e == nil {
							_ = probe.Release()
							t.Fatal("second read was not under actual lane lock")
						}
						consumed = true
						return WorktreeMergeReceipt{}, sentinel
					}
					return readWorktreeMergeReceipt(p)
				}
			}
			got, err := adoptPublishedWorktreeMergeCandidate(t.Context(), options, defaultRunner, read)
			if err == nil || got != (WorktreeMergePublishedCandidateAdoption{}) {
				t.Fatalf("entry %s result=%+v err=%v", stage, got, err)
			}
			if stage == "second read" && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("held read consumed=%t err=%v", consumed, err)
			}
			if stage == "held lane" {
				if err := held.Release(); err != nil {
					t.Fatal(err)
				}
			}
			adoptionClosureNoAck(t, r)
			if stage != "malformed receipt" && !bytes.Equal(adoptionClosureBytes(t, path), original) {
				t.Fatal("entry refusal mutated receipt")
			}
			unlocked, err := AcquireOperationLock(projects, r.Lane, true)
			if err != nil {
				t.Fatalf("owner leaked lane: %v", err)
			}
			if err := unlocked.Release(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

//nolint:paralleltest // Native source Work Log default-root selection and actual gh observation use process-wide environment.
func TestE2EPublishedAdoptionClosureOrderedProofRefusals(t *testing.T) {
	f, r := adoptionClosureFixture(t)
	for _, stage := range []string{"receipt shape", "candidate custody", "source clean", "origin read", "PR read", "PR decode", "remote read", "remote identity"} {
		//nolint:paralleltest // Sequential private state restoration and hosted provider environment.
		t.Run(stage, func(t *testing.T) {
			current := r
			run := defaultRunner
			sentinel := errors.New("selected adoption observation")
			consumed := false
			want := ""
			switch stage {
			case "receipt shape":
				current.Status = WorktreeMergePrepared
				want = "not an exact unlanded"
			case "candidate custody":
				current.Candidate.Worktree = t.TempDir()
				want = "validate candidate"
			case "source clean":
				dirty := filepath.Join(r.Sources[0].Worktree, "dirty.txt")
				if err := os.WriteFile(dirty, []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(dirty) })
				want = "re-read sources"
			case "origin read", "remote read":
				args := []string{"remote", "get-url", "origin"}
				want = "read candidate origin"
				if stage == "remote read" {
					args = []string{"ls-remote", "--heads", "origin", "refs/heads/" + r.Candidate.Branch}
					want = "read candidate remote ref"
				}
				run = adoptionClosureRunner{Runner: defaultRunner, before: func(dir, name string, got []string) error {
					if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(got, args) {
						consumed = true
						return sentinel
					}
					return nil
				}}
			case "PR read":
				t.Setenv("WB_TEST_PR_STATE", "closed")
				want = "not open"
			case "PR decode":
				t.Setenv("WB_TEST_PR_SHA", "\"")
				want = "decode pull request"
			case "remote identity":
				runEngineGit(t, f.canonical, "push", "--force", "origin", r.TargetSHA+":refs/heads/"+r.Candidate.Branch)
				t.Cleanup(func() {
					adoptionClosureCleanupGit(t, f.canonical, "push", "--force", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
				})
				want = "does not match receipted candidate"
			}
			err := provePublishedCandidateAdoption(t.Context(), run, f.githubDir, current, current.ReceiptPath, "7")
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("stage %s err=%v want %s", stage, err, want)
			}
			if (stage == "origin read" || stage == "remote read") && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("negative observation consumed=%t err=%v", consumed, err)
			}
			adoptionClosureNoAck(t, r)
		})
	}
	// Native public owner proof propagation, retaining exact return shape.
	bad := adoptionClosureOptions(f, r)
	bytesBefore := adoptionClosureRestore(t, r.ReceiptPath)
	invalid := r
	invalid.Status = WorktreeMergePrepared
	if err := persistWorktreeMergeReceipt(invalid); err != nil {
		t.Fatal(err)
	}
	if got, err := AdoptPublishedWorktreeMergeCandidate(t.Context(), bad); err == nil || got != (WorktreeMergePublishedCandidateAdoption{}) {
		t.Fatalf("public proof refusal=%+v %v", got, err)
	}
	if err := os.WriteFile(r.ReceiptPath, bytesBefore, 0600); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // Actual hosted provider plus native source Work Log default root are process-wide fixture inputs.
func TestE2EPublishedAdoptionClosureLateNativeFileAndSidecarRefusals(t *testing.T) {
	f, r := adoptionClosureFixture(t)
	options := adoptionClosureOptions(f, r)
	for _, stage := range []string{"receipt hash", "different existing evidence", "malformed sidecar", "publication permission"} {
		//nolint:paralleltest // Rows share immutable setup and restore private receipt, sidecar and directory mode before the next row.
		t.Run(stage, func(t *testing.T) {
			if stage == "publication permission" && runtime.GOOS == "windows" {
				t.Skip("Windows chmod does not remove native directory write permission")
			}
			before := adoptionClosureRestore(t, r.ReceiptPath)
			ackPath := publishedCandidateAdoptionPath(r.ReceiptPath)
			t.Cleanup(func() { _ = os.Remove(ackPath) })
			run := defaultRunner
			consumed := false
			if stage == "different existing evidence" {
				seedOptions := options
				seedOptions.Apply = false
				a, err := AdoptPublishedWorktreeMergeCandidate(t.Context(), seedOptions)
				if err != nil {
					t.Fatal(err)
				}
				a.PullRequest = "8"
				a.ID = publishedCandidateAdoptionID(a)
				if err := persistPublishedCandidateAdoption(ackPath, a); err != nil {
					t.Fatal(err)
				}
				parsed, err := readPublishedCandidateAdoption(ackPath, r)
				if err != nil || !reflect.DeepEqual(parsed, a) || parsed.PullRequest != "8" || parsed.Actor != options.Actor || parsed.Reason != options.Reason {
					t.Fatalf("native alternate sidecar must authenticate before owner mismatch: %+v %v", parsed, err)
				}
			}
			if stage == "malformed sidecar" {
				if err := os.WriteFile(ackPath, []byte("broken JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "receipt hash" || stage == "publication permission" {
				parent := filepath.Dir(r.ReceiptPath)
				info, err := os.Stat(parent)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(parent, info.Mode().Perm()); err != nil {
						t.Error(err)
					}
				})
				run = adoptionClosureRunner{Runner: defaultRunner, after: func(dir, name string, args []string) {
					if !consumed && dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"ls-remote", "--heads", "origin", "refs/heads/" + r.Candidate.Branch}) {
						consumed = true
						if stage == "receipt hash" {
							if err := os.Remove(r.ReceiptPath); err != nil {
								t.Fatal(err)
							}
						} else {
							if err := os.Chmod(parent, 0500); err != nil {
								t.Fatal(err)
							}
						}
					}
				}}
			}
			var existingSidecar []byte
			if stage == "different existing evidence" || stage == "malformed sidecar" {
				existingSidecar = adoptionClosureBytes(t, ackPath)
			}
			got, err := adoptPublishedWorktreeMergeCandidate(t.Context(), options, run, readWorktreeMergeReceipt)
			if existingSidecar != nil && !bytes.Equal(adoptionClosureBytes(t, ackPath), existingSidecar) {
				t.Fatal("owner refusal changed existing sidecar")
			}
			if head := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", "HEAD")); head != r.Candidate.SHA {
				t.Fatalf("owner refusal changed actual candidate HEAD: %s", head)
			}
			if remote := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "ls-remote", "--heads", "origin", "refs/heads/"+r.Candidate.Branch)); !strings.HasPrefix(remote, r.Candidate.SHA+"\t") {
				t.Fatalf("owner refusal changed actual remote: %s", remote)
			}
			unlocked, lockErr := AcquireOperationLock(f.githubDir, r.Lane, true)
			if lockErr != nil {
				t.Fatalf("owner refusal leaked lane: %v", lockErr)
			}
			if releaseErr := unlocked.Release(); releaseErr != nil {
				t.Fatal(releaseErr)
			}
			if err == nil || got != (WorktreeMergePublishedCandidateAdoption{}) {
				t.Fatalf("late %s result=%+v err=%v", stage, got, err)
			}
			if (stage == "receipt hash" || stage == "publication permission") && !consumed {
				t.Fatal("native final observation not consumed")
			}
			if stage == "publication permission" && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("publication error=%v", err)
			}
			if stage == "different existing evidence" && !strings.Contains(err.Error(), "binds different evidence") {
				t.Fatalf("identity error=%v", err)
			}
			if stage == "receipt hash" || stage == "publication permission" {
				adoptionClosureNoAck(t, r)
			}
			if stage != "receipt hash" && !bytes.Equal(adoptionClosureBytes(t, r.ReceiptPath), before) {
				t.Fatal("refusal mutated receipt")
			}
		})
	}
}

//nolint:paralleltest // Native Work Log source reads use the genuine default projects-root selection.
func TestE2EPublishedAdoptionClosureSourceObservationsStayNative(t *testing.T) {
	f, r := adoptionClosureFixture(t)
	source := r.Sources[0]
	for _, stage := range []string{"branch read", "HEAD read", "Work Log read", "claim identity"} {
		//nolint:paralleltest // These rows restore one private source and its claim sequentially.
		t.Run(stage, func(t *testing.T) {
			if err := validatePublishedCandidateAdoptionSources(t.Context(), defaultRunner, r); err != nil {
				t.Fatalf("native source preflight: %v", err)
			}
			sentinel := errors.New("selected source observation")
			consumed := false
			var run runner.Runner
			if stage == "branch read" || stage == "HEAD read" {
				args := []string{"branch", "--show-current"}
				if stage == "HEAD read" {
					args = []string{"rev-parse", "--verify", "HEAD^{commit}"}
				}
				run = adoptionClosureRunner{Runner: defaultRunner, before: func(dir, name string, got []string) error {
					if dir == source.Worktree && name == "git" && reflect.DeepEqual(got, args) {
						consumed = true
						return sentinel
					}
					return nil
				}}
			} else {
				view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: source.Worktree})
				if err != nil || view.Claim == nil {
					t.Fatalf("actual initial claim=%+v err=%v", view, err)
				}
				if stage == "claim identity" {
					adoptionClosureRestore(t, view.Claim.ClaimPath)
				}
				gitPath := filepath.Join(source.Worktree, ".wb", "local", "prompts")
				held := gitPath + ".held-adoption"
				moved := false
				t.Cleanup(func() {
					if moved {
						if err := os.Remove(gitPath); err != nil && !os.IsNotExist(err) {
							t.Error(err)
						}
						if err := os.Rename(held, gitPath); err != nil {
							t.Error(err)
						}
					}
				})
				run = adoptionClosureRunner{Runner: defaultRunner, after: func(dir, name string, args []string) {
					if !consumed && dir == source.Worktree && name == "git" && reflect.DeepEqual(args, []string{"merge-base", source.SHA, source.SHA}) {
						consumed = true
						if stage == "Work Log read" {
							if err := os.Rename(gitPath, held); err != nil {
								t.Fatal(err)
							}
							moved = true
							if err := os.WriteFile(gitPath, []byte("private non-directory prompt fixture"), 0600); err != nil {
								t.Fatal(err)
							}
						} else {
							b := adoptionClosureBytes(t, view.Claim.ClaimPath)
							var fields map[string]any
							if err := json.Unmarshal(b, &fields); err != nil {
								t.Fatal(err)
							}
							fields["task"] = "different-owned-task"
							changed, err := json.Marshal(fields)
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(view.Claim.ClaimPath, changed, 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
				}}
			}
			err := validatePublishedCandidateAdoptionSources(t.Context(), run, r)
			if err == nil || !consumed {
				t.Fatalf("source %s consumed=%t err=%v", stage, consumed, err)
			}
			if stage == "branch read" || stage == "HEAD read" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("source sentinel lost: %v", err)
				}
			} else {
				want := "load source Work Log"
				if stage == "claim identity" {
					want = "no matching active Work Log claim"
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("source %s err=%v want %s", stage, err, want)
				}
			}
			adoptionClosureNoAck(t, r)
		})
	}
}

//nolint:paralleltest // Genuine hosted PR observation and default Work Log root are bound by the provider fixture.
func TestE2EPublishedAdoptionClosureMaterializedReaderAndReplay(t *testing.T) {
	f, r := adoptionClosureFixture(t)
	options := adoptionClosureOptions(f, r)
	ack, err := AdoptPublishedWorktreeMergeCandidate(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	ackPath := ack.AcknowledgementPath
	ackBytes := adoptionClosureBytes(t, ackPath)
	receiptBytes := adoptionClosureBytes(t, r.ReceiptPath)
	if again, err := AdoptPublishedWorktreeMergeCandidate(t.Context(), options); err != nil || again.ID != ack.ID {
		t.Fatalf("actual replay=%+v err=%v", again, err)
	}
	if !bytes.Equal(adoptionClosureBytes(t, r.ReceiptPath), receiptBytes) {
		t.Fatal("actual adoption changed immutable receipt")
	}
	for _, stage := range []string{"missing sidecar", "invalid sidecar", "empty materialized head", "ancestry error", "ancestry false", "unpublished descendant", "hosted refusal", "published descendant"} {
		//nolint:paralleltest // One actual adoption is restored exactly between sequential materialization-contract observations.
		t.Run(stage, func(t *testing.T) {
			t.Cleanup(func() {
				if err := os.WriteFile(ackPath, ackBytes, 0600); err != nil {
					t.Error(err)
				}
			})
			current := r
			run := defaultRunner
			wantErr := ""
			wantFound := false
			wantAck := true
			sentinel := errors.New("selected materialized ancestry")
			consumed := false
			switch stage {
			case "missing sidecar":
				if err := os.Remove(ackPath); err != nil {
					t.Fatal(err)
				}
				wantAck = false
			case "invalid sidecar":
				if err := os.WriteFile(ackPath, []byte("invalid JSON"), 0600); err != nil {
					t.Fatal(err)
				}
				wantErr = "invalid character"
				wantAck = false
			default:
				current.PullRequest = ack.PullRequest
				current.PublishedCandidateSHA = ack.Candidate.SHA
				tree := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "rev-parse", r.Candidate.SHA+"^{tree}"))
				parent := r.Candidate.SHA
				if stage == "ancestry false" {
					parent = r.TargetSHA
				}
				head := strings.TrimSpace(runEngineGit(t, r.Candidate.Worktree, "commit-tree", tree, "-p", parent, "-m", "test: materialized adoption contract"))
				current.Candidate.SHA = head
				switch stage {
				case "empty materialized head":
					current.Candidate.SHA = ""
				case "ancestry error":
					wantErr = sentinel.Error()
					wantAck = false
					run = adoptionClosureRunner{Runner: defaultRunner, before: func(dir, name string, args []string) error {
						if dir == r.Candidate.Worktree && name == "git" && reflect.DeepEqual(args, []string{"merge-base", ack.Candidate.SHA, head}) {
							consumed = true
							return sentinel
						}
						return nil
					}}
				case "ancestry false":
					wantErr = "does not retain adopted predecessor"
					wantAck = false
				case "hosted refusal", "published descendant":
					current.PublishedCandidateSHA = head
					runEngineGit(t, r.Candidate.Worktree, "reset", "--hard", head)
					t.Cleanup(func() { adoptionClosureCleanupGit(t, r.Candidate.Worktree, "reset", "--hard", r.Candidate.SHA) })
					runEngineGit(t, r.Candidate.Worktree, "push", "--force", "origin", head+":refs/heads/"+r.Candidate.Branch)
					t.Cleanup(func() {
						adoptionClosureCleanupGit(t, r.Candidate.Worktree, "push", "--force", "origin", r.Candidate.SHA+":refs/heads/"+r.Candidate.Branch)
					})
					t.Setenv("WB_TEST_PR_SHA", head)
					if stage == "hosted refusal" {
						t.Setenv("WB_TEST_PR_STATE", "closed")
						wantErr = "not open"
						wantAck = false
					} else {
						wantFound = true
					}
				}
			}
			got, found, err := adoptedPublishedCandidateWithRunner(t.Context(), run, current)
			if wantErr == "" {
				if err != nil {
					t.Fatalf("materialized %s err=%v", stage, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("materialized %s err=%v want %s", stage, err, wantErr)
			}
			if found != wantFound || (wantAck && got.ID != ack.ID) || (!wantAck && got != (WorktreeMergePublishedCandidateAdoption{})) {
				t.Fatalf("materialized %s result=%+v found=%t", stage, got, found)
			}
			if stage == "ancestry error" && (!consumed || !errors.Is(err, sentinel)) {
				t.Fatalf("ancestry consumed=%t err=%v", consumed, err)
			}
			if !bytes.Equal(adoptionClosureBytes(t, r.ReceiptPath), receiptBytes) {
				t.Fatal("materialization query changed immutable receipt")
			}
		})
	}
	if got, found, err := adoptedPublishedCandidate(t.Context(), r); err != nil || !found || got.ID != ack.ID {
		t.Fatalf("native reader default=%+v found=%t err=%v", got, found, err)
	}
}
