package worktreeretire

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transactionFixture struct {
	result    Transaction
	operation Operation
	remote    map[string]string
	reports   []Transaction
	head      string
	commits   int
	deletes   int
	removed   bool
	stop      string
	stopErr   error
}

func newTransactionFixture(t *testing.T) *transactionFixture {
	t.Helper()
	parent := strings.Repeat("a", 40)
	committed := strings.Repeat("b", 40)
	archive := strings.Repeat("c", 40)
	result := Transaction{Version: 1, Task: "task", Repository: "acme/app", ArchiveRepository: "acme/app-retired",
		Worktree: filepath.Join(t.TempDir(), "worktree"), Canonical: filepath.Join(t.TempDir(), "canonical"),
		Branch: "topic", OriginalRemoteSHA: parent, SourceSHA: parent, ClaimID: "claim", EffortID: "effort", RunID: "run", Phase: "planned"}
	result.ReportPath = ReportPath(t.TempDir(), result)
	f := &transactionFixture{result: result, head: parent, remote: map[string]string{}, stopErr: errors.New("interrupted")}
	f.remote["origin|refs/heads/topic"] = parent
	f.operation = Operation{
		ArchiveRemote: "archive", Now: func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) },
		AfterPhase: func(phase string) error {
			if phase == f.stop {
				return f.stopErr
			}
			return nil
		},
		WriteReport: func(result Transaction) error {
			if result.Phase == "commit_intent" && f.head != parent {
				return fmt.Errorf("commit preceded durable intent")
			}
			f.reports = append(f.reports, result)
			return nil
		},
		Remote: TransactionPorts{
			RemoteSHA: func(_ context.Context, _, remote, ref string) (string, error) { return f.remote[remote+"|"+ref], nil },
			PublishSourceRef: func(_ context.Context, result Transaction, ref string) error {
				f.remote["origin|"+ref] = result.SourceSHA
				return nil
			},
			DeleteAndTag: func(_ context.Context, result Transaction, ref, proofRef string) error {
				if len(f.reports) == 0 || f.reports[len(f.reports)-1].DeleteIntentSHA != parent {
					return fmt.Errorf("delete preceded durable intent")
				}
				f.deletes++
				delete(f.remote, "origin|"+ref)
				f.remote["origin|"+proofRef] = result.SourceSHA
				return nil
			},
		},
		Apply: ApplyPorts{
			ValidateHeld: func() error { return nil },
			CommitSource: func(_ context.Context, _ string, before func(string, string) error) (bool, error) {
				if err := before(strings.Repeat("d", 40), "retire topic"); err != nil {
					return false, err
				}
				f.commits++
				f.head = committed
				return true, nil
			},
			ValidateIntentCommit: func(_ context.Context, result Transaction, head string) error {
				if result.IntentParentSHA != parent || result.IntentTreeSHA != strings.Repeat("d", 40) || result.IntentMessage != "retire topic" || head != committed {
					return fmt.Errorf("wrong immutable commit intent")
				}
				return nil
			},
			CurrentHead:         func(context.Context) (string, error) { return f.head, nil },
			Clean:               func(context.Context) (bool, error) { return true, nil },
			SealWorkLog:         func(Transaction) error { return nil },
			CheckPrivateArchive: func(context.Context, Transaction) error { return nil },
			PublishArchive: func(_ context.Context, result *Transaction) error {
				result.ArchiveSHA = archive
				result.Phase = "archive_published"
				f.remote["archive|refs/heads/"+result.ArchiveRef] = archive
				return nil
			},
			BeforeDeletion: func(context.Context) error { return nil },
			RemoveLocal: func(_ context.Context, result *Transaction) error {
				if f.remote["origin|refs/heads/topic"] != "" || f.remote["origin|"+DeletionProofRef(*result)] != result.SourceSHA {
					return fmt.Errorf("local removal preceded exact remote proof")
				}
				f.removed = true
				result.Phase = "complete"
				if f.stop == "worktree_removed" {
					return f.stopErr
				}
				return nil
			},
		},
	}
	return f
}

func TestTransactionPhaseFaultsReplayFromDurableReceipt(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"source_committed", "source_published", "archive_published", "original_delete_intent", "original_delete_pushed", "original_deleted"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			f := newTransactionFixture(t)
			f.stop = phase
			partial, err := f.operation.ApplyTransaction(context.Background(), f.result, false, f.head, "retire topic")
			if !errors.Is(err, f.stopErr) {
				t.Fatalf("phase %s: %v", phase, err)
			}
			if len(f.reports) == 0 {
				t.Fatal("no durable receipt before interruption")
			}
			if phase == "source_committed" {
				if partial.Phase != "commit_intent" || f.reports[0].Phase != "commit_intent" {
					t.Fatalf("missing exact commit intent: %+v", partial)
				}
			} else if phase == "original_delete_intent" || phase == "original_delete_pushed" {
				if partial.DeleteIntentSHA != f.result.OriginalRemoteSHA {
					t.Fatalf("missing exact deletion intent: %+v", partial)
				}
			}
			f.stop = ""
			finished, err := f.operation.ApplyTransaction(context.Background(), partial, true, f.head, "ignored on replay")
			if err != nil || finished.Phase != "complete" || !f.removed {
				t.Fatalf("replay: %+v, %v", finished, err)
			}
			if f.commits != 1 || f.deletes != 1 {
				t.Fatalf("repeated mutation: commits=%d deletes=%d", f.commits, f.deletes)
			}
			if f.reports[len(f.reports)-1].Phase != "complete" {
				t.Fatal("completion not durable")
			}
		})
	}
}

func TestTransactionRemovedRecoveryRequiresExactRemoteProof(t *testing.T) {
	t.Parallel()
	f := newTransactionFixture(t)
	result, err := f.operation.ApplyTransaction(context.Background(), f.result, false, f.head, "retire topic")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRemovedReceipt(result, result.ReportPath, result.Task, result.Repository); err != nil {
		t.Fatal(err)
	}
	if err := f.operation.VerifyRemovedRemote(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if err := f.operation.CompleteRemoved(&result); err != nil || result.Phase != "complete" {
		t.Fatalf("completion: %+v, %v", result, err)
	}
	delete(f.remote, "origin|"+DeletionProofRef(result))
	if err := f.operation.VerifyRemovedRemote(context.Background(), result); err == nil || !strings.Contains(err.Error(), "deletion proof changed") {
		t.Fatalf("lost proof: %v", err)
	}
}

func TestTransactionPortFailuresStopBeforeLocalRemoval(t *testing.T) {
	t.Parallel()
	failure := errors.New("port failed")
	cases := []struct {
		name string
		fail func(*transactionFixture)
		want string
	}{
		{"held checkout", func(f *transactionFixture) { f.operation.Apply.ValidateHeld = func() error { return failure } }, "port failed"},
		{"commit", func(f *transactionFixture) {
			f.operation.Apply.CommitSource = func(context.Context, string, func(string, string) error) (bool, error) { return false, failure }
		}, "port failed"},
		{"commit intent write", func(f *transactionFixture) { failReportPhase(f, "commit_intent", failure) }, "port failed"},
		{"head read", func(f *transactionFixture) {
			f.operation.Apply.CurrentHead = func(context.Context) (string, error) { return "", failure }
		}, "port failed"},
		{"dirty checkout", func(f *transactionFixture) {
			f.operation.Apply.Clean = func(context.Context) (bool, error) { return false, nil }
		}, "source checkout is dirty"},
		{"checkout cleanliness error", func(f *transactionFixture) {
			f.operation.Apply.Clean = func(context.Context) (bool, error) { return false, failure }
		}, "port failed"},
		{"committed report write", func(f *transactionFixture) { failReportPhase(f, "committed", failure) }, "port failed"},
		{"source publish", func(f *transactionFixture) {
			f.operation.Remote.PublishSourceRef = func(context.Context, Transaction, string) error { return failure }
		}, "port failed"},
		{"source report write", func(f *transactionFixture) { failReportPhase(f, "source_published", failure) }, "port failed"},
		{"work log seal", func(f *transactionFixture) {
			f.operation.Apply.SealWorkLog = func(Transaction) error { return failure }
		}, "port failed"},
		{"archive preflight first", func(f *transactionFixture) {
			f.operation.Apply.CheckPrivateArchive = func(context.Context, Transaction) error { return failure }
		}, "port failed"},
		{"archive publication", func(f *transactionFixture) {
			f.operation.Apply.PublishArchive = func(context.Context, *Transaction) error { return failure }
		}, "port failed"},
		{"archive report write", func(f *transactionFixture) { failReportPhase(f, "archive_published", failure) }, "port failed"},
		{"source receipt changed", func(f *transactionFixture) {
			original := f.operation.Apply.PublishArchive
			f.operation.Apply.PublishArchive = func(ctx context.Context, result *Transaction) error {
				if err := original(ctx, result); err != nil {
					return err
				}
				f.remote["origin|"+SourceRef(*result)] = strings.Repeat("e", 40)
				return nil
			}
		}, "retired source receipt changed"},
		{"archive receipt changed", func(f *transactionFixture) {
			original := f.operation.Apply.PublishArchive
			f.operation.Apply.PublishArchive = func(ctx context.Context, result *Transaction) error {
				if err := original(ctx, result); err != nil {
					return err
				}
				f.remote["archive|refs/heads/"+result.ArchiveRef] = strings.Repeat("e", 40)
				return nil
			}
		}, "private archive receipt changed"},
		{"archive preflight second", func(f *transactionFixture) {
			count := 0
			f.operation.Apply.CheckPrivateArchive = func(context.Context, Transaction) error {
				count++
				if count == 2 {
					return failure
				}
				return nil
			}
		}, "port failed"},
		{"final authority", func(f *transactionFixture) {
			f.operation.Apply.BeforeDeletion = func(context.Context) error { return failure }
		}, "port failed"},
		{"original ref changed", func(f *transactionFixture) {
			f.operation.Apply.BeforeDeletion = func(context.Context) error { f.remote["origin|refs/heads/topic"] = strings.Repeat("e", 40); return nil }
		}, "disappeared or moved"},
		{"proof already present", func(f *transactionFixture) {
			f.operation.Apply.BeforeDeletion = func(context.Context) error {
				f.remote["origin|"+DeletionProofRef(f.reports[len(f.reports)-1])] = "other"
				return nil
			}
		}, "proof ref is already present"},
		{"deletion intent write", func(f *transactionFixture) { failDeletionIntentWrite(f, failure) }, "port failed"},
		{"atomic delete", func(f *transactionFixture) {
			f.operation.Remote.DeleteAndTag = func(context.Context, Transaction, string, string) error { return failure }
		}, "port failed"},
		{"deletion report write", func(f *transactionFixture) { failReportPhase(f, "original_deleted", failure) }, "port failed"},
		{"local removal", func(f *transactionFixture) {
			f.operation.Apply.RemoveLocal = func(context.Context, *Transaction) error { return failure }
		}, "port failed"},
		{"completion write", func(f *transactionFixture) { failReportPhase(f, "complete", failure) }, "port failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newTransactionFixture(t)
			tc.fail(f)
			_, err := f.operation.ApplyTransaction(context.Background(), f.result, false, f.head, "retire topic")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("failure = %v, want %q", err, tc.want)
			}
			if f.removed && tc.name != "completion write" {
				t.Fatal("removed checkout before verified phase")
			}
		})
	}
}

func failReportPhase(f *transactionFixture, phase string, failure error) {
	original := f.operation.WriteReport
	f.operation.WriteReport = func(result Transaction) error {
		if result.Phase == phase {
			return failure
		}
		return original(result)
	}
}

func failDeletionIntentWrite(f *transactionFixture, failure error) {
	original := f.operation.WriteReport
	f.operation.WriteReport = func(result Transaction) error {
		if result.DeleteIntentSHA != "" && result.Phase == "archive_published" {
			return failure
		}
		return original(result)
	}
}

func TestTransactionCommitIntentReplayRejectsChangedEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		alter func(*transactionFixture)
		want  string
	}{
		{"staged tree", func(f *transactionFixture) {
			f.operation.Apply.CommitSource = func(_ context.Context, _ string, before func(string, string) error) (bool, error) {
				return false, before("wrong", "retire topic")
			}
		}, "staged source changed"},
		{"commit failure", func(f *transactionFixture) {
			f.operation.Apply.CommitSource = func(context.Context, string, func(string, string) error) (bool, error) {
				return false, errors.New("commit failed")
			}
		}, "commit failed"},
		{"staged changes lost", func(f *transactionFixture) {
			f.operation.Apply.CommitSource = func(context.Context, string, func(string, string) error) (bool, error) { return false, nil }
		}, "lost its staged changes"},
		{"head read", func(f *transactionFixture) {
			f.operation.Apply.CurrentHead = func(context.Context) (string, error) { return "", errors.New("head failed") }
		}, "head failed"},
		{"commit attestation", func(f *transactionFixture) {
			f.operation.Apply.ValidateIntentCommit = func(context.Context, Transaction, string) error { return errors.New("attestation failed") }
		}, "attestation failed"},
		{"committed report", func(f *transactionFixture) { failReportPhase(f, "committed", errors.New("report failed")) }, "report failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newTransactionFixture(t)
			f.stop = "source_committed"
			intent, err := f.operation.ApplyTransaction(context.Background(), f.result, false, f.head, "retire topic")
			if !errors.Is(err, f.stopErr) || intent.Phase != "commit_intent" {
				t.Fatalf("setup: %+v %v", intent, err)
			}
			f.head = intent.IntentParentSHA // replay executes the commit from its exact parent
			f.stop = ""
			tc.alter(f)
			_, err = f.operation.ApplyTransaction(context.Background(), intent, true, f.head, "ignored")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("replay error=%v, want %q", err, tc.want)
			}
		})
	}
	f := newTransactionFixture(t)
	f.result.Phase = "committed"
	f.result.SourceSHA = strings.Repeat("e", 40)
	if _, err := f.operation.ApplyTransaction(context.Background(), f.result, true, f.head, "ignored"); err == nil || !strings.Contains(err.Error(), "checkout moved") {
		t.Fatalf("moved checkout accepted: %v", err)
	}
	f = newTransactionFixture(t)
	f.stop = "source_committed"
	intent, err := f.operation.ApplyTransaction(context.Background(), f.result, false, f.head, "retire topic")
	if !errors.Is(err, f.stopErr) {
		t.Fatalf("setup interruption: %v", err)
	}
	if _, err := f.operation.ApplyTransaction(context.Background(), intent, true, f.head, "ignored"); !errors.Is(err, f.stopErr) {
		t.Fatalf("replay interruption: %v", err)
	}
}

func TestTransactionRemovedReceiptRejectsIdentityAndRemoteChanges(t *testing.T) {
	t.Parallel()
	f := newTransactionFixture(t)
	result, err := f.operation.ApplyTransaction(context.Background(), f.result, false, f.head, "retire topic")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRemovedReceipt(result, result.ReportPath, result.Task, "acme/other"); err == nil || !strings.Contains(err.Error(), "repository mismatch") {
		t.Fatalf("repository accepted: %v", err)
	}
	if err := ValidateRemovedReceipt(result, result.ReportPath+"-other", result.Task, result.Repository); err == nil || !strings.Contains(err.Error(), "does not authorize") {
		t.Fatalf("path accepted: %v", err)
	}
	if err := ValidateRemovedReceipt(result, result.ReportPath, result.Task, ""); err != nil {
		t.Fatalf("optional filter rejected: %v", err)
	}
	f.remote["origin|refs/heads/topic"] = result.SourceSHA
	if err := f.operation.VerifyRemovedRemote(context.Background(), result); err == nil || !strings.Contains(err.Error(), "original remote branch remains") {
		t.Fatalf("restored original accepted: %v", err)
	}
	delete(f.remote, "origin|refs/heads/topic")
	f.remote["origin|"+SourceRef(result)] = strings.Repeat("e", 40)
	if err := f.operation.VerifyRemovedRemote(context.Background(), result); err == nil || !strings.Contains(err.Error(), "retired source receipt changed") {
		t.Fatalf("changed source accepted: %v", err)
	}
}

func TestTransactionOperationDefaultsPersistWithoutHooks(t *testing.T) {
	t.Parallel()
	f := newTransactionFixture(t)
	f.operation.AfterPhase = nil
	if err := f.operation.after("complete"); err != nil {
		t.Fatal(err)
	}
	f.operation.WriteReport = nil
	if err := f.operation.CompleteRemoved(&f.result); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(f.result.ReportPath); err == nil {
		t.Fatal("incomplete receipt unexpectedly validated")
	}
}
