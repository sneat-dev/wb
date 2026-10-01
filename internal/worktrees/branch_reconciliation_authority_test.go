package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBranchReconciliationDurableBundleAuthority(t *testing.T) {
	t.Parallel()
	record := branchReconciliationRecord{ClaimBranch: "wb/claim", LocalHead: strings.Repeat("a", 40), RemoteHead: strings.Repeat("b", 40)}
	for _, tc := range []struct{ name, kind string }{
		{"missing evidence", "missing"},
		{"changed reference", "reference"},
		{"missing bundle", "bundle"},
		{"changed bytes", "digest"},
		{"verified", "valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			for _, item := range []struct{ kind, ref, head string }{
				{"local", "refs/heads/" + record.ClaimBranch, record.LocalHead},
				{"remote", "refs/remotes/origin/" + record.ClaimBranch, record.RemoteHead},
			} {
				if tc.kind == "missing" && item.kind == "local" {
					continue
				}
				content := []byte(item.kind + " bundle")
				digest := sha256.Sum256(content)
				evidence := branchReconciliationEvidence{Version: 1, Head: item.head, Ref: item.ref,
					Bundle: item.kind + ".bundle", SHA256: hex.EncodeToString(digest[:])}
				if item.kind == "local" && tc.kind == "reference" {
					evidence.Ref = "refs/heads/other"
				}
				if err := writeJSONAtomicAt(directory, item.kind+".json", evidence, 0o600); err != nil {
					t.Fatal(err)
				}
				if item.kind == "local" && tc.kind == "bundle" {
					continue
				}
				if item.kind == "local" && tc.kind == "digest" {
					content = []byte("changed bytes")
				}
				if err := writeBytesImmutableAt(directory, item.kind+".bundle", content, 0o600, true); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyReconciliationBundles(directory, record)
			if tc.kind == "valid" && err != nil {
				t.Fatalf("valid bundle proof: %v", err)
			}
			if tc.kind != "valid" && err == nil {
				t.Fatalf("accepted %s bundle proof", tc.kind)
			}
		})
	}
}

func TestBranchReconciliationEventReplayUsesRecordedPayload(t *testing.T) {
	t.Parallel()
	record := branchReconciliationRecord{EventID: "event-1", Reason: "restore branch", Actor: "operator",
		LiveBranch: "wb/live", ClaimBranch: "wb/claim", ExpectedHead: strings.Repeat("c", 40),
		LocalHead: strings.Repeat("a", 40), RemoteHead: strings.Repeat("b", 40)}
	prior := reconciliationEvent(record, LocalGitEvidence{Branch: "wb/claim", Head: strings.Repeat("c", 40)})
	prior.Version, prior.Seq, prior.At = 1, 1, time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	marker := errors.New("event port failure")
	for _, tc := range []struct {
		name               string
		events             []LocalWorkLogEvent
		readErr, appendErr error
		want               string
	}{
		{"read failure", nil, marker, nil, "port failure"},
		{"missing Git", []LocalWorkLogEvent{func() LocalWorkLogEvent { e := prior; e.Git = nil; return e }()}, nil, nil, "no Git evidence"},
		{"wrong Git branch", []LocalWorkLogEvent{func() LocalWorkLogEvent {
			e := prior
			git := *e.Git
			git.Branch = "wb/unrelated"
			e.Git = &git
			return e
		}()}, nil, nil, "Git identity"},
		{"wrong Git head", []LocalWorkLogEvent{func() LocalWorkLogEvent {
			e := prior
			git := *e.Git
			git.Head = strings.Repeat("d", 40)
			e.Git = &git
			return e
		}()}, nil, nil, "Git identity"},
		{"different payload", []LocalWorkLogEvent{func() LocalWorkLogEvent { e := prior; e.Message = "other"; return e }()}, nil, nil, "differs"},
		{"append failure", []LocalWorkLogEvent{prior}, nil, marker, "port failure"},
		{"exact replay", []LocalWorkLogEvent{prior}, nil, nil, ""},
		{"new event", nil, nil, nil, ""},
		{"new observation has wrong Git identity", nil, nil, nil, "Git identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			observed := 0
			ports := reconciliationPorts{
				readEvents: func(string) ([]LocalWorkLogEvent, error) { return tc.events, tc.readErr },
				observeGit: func(context.Context, string) LocalGitEvidence {
					observed++
					if tc.name == "new observation has wrong Git identity" {
						return LocalGitEvidence{Branch: "later", Head: record.ExpectedHead}
					}
					return LocalGitEvidence{Branch: record.ClaimBranch, Head: record.ExpectedHead}
				},
				appendEvent: func(_ string, event LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
					if tc.name == "exact replay" && !sameLocalEvent(event, prior) {
						t.Fatalf("replayed event changed: %+v", event)
					}
					return event, LocalWorkLogProjection{}, tc.appendErr
				},
			}
			event, _, err := appendReconciliationEvent(context.Background(), "/unused", record, ports)
			if tc.want == "" && (err != nil || event.ID != record.EventID) {
				t.Fatalf("event=%+v err=%v", event, err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("event error=%v, want %s", err, tc.want)
			}
			if tc.name == "exact replay" && observed != 0 {
				t.Fatalf("replay re-observed Git %d times", observed)
			}
			if tc.name == "new event" && observed != 1 {
				t.Fatalf("new event observed Git %d times", observed)
			}
		})
	}
}

func TestBranchReconciliationStageRevalidationPorts(t *testing.T) {
	t.Parallel()
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	record := reconciliationRecordForClaim(claim)
	marker := errors.New("lifecycle unavailable")
	options := LogRecoverOptions{ExpectedHead: record.ExpectedHead}
	if err := revalidateReconciliationStage(context.Background(), options, claim.Worktree, claim, record,
		nil, (reconciliationPorts{lifecycle: func(context.Context, string, string, workLogClaim) (ListResult, error) { return ListResult{}, marker }}).withDefaults()); !errors.Is(err, marker) {
		t.Fatalf("lifecycle error = %v", err)
	}
	if err := revalidateReconciliationStage(context.Background(), options, claim.Worktree, claim, record,
		nil, (reconciliationPorts{lifecycle: func(context.Context, string, string, workLogClaim) (ListResult, error) {
			return ListResult{HeadSHA: "other"}, nil
		}}).withDefaults()); err == nil {
		t.Fatal("invalid lifecycle evidence passed")
	}
}

func TestBranchReconciliationBundlePublicationPortFailures(t *testing.T) {
	t.Parallel()
	marker := errors.New("bundle publication failed")
	for _, failed := range []string{"verify", "advertisement", "readback", "durable write"} {
		t.Run(failed, func(t *testing.T) {
			t.Parallel()
			canonical := reconciliationFakeCanonical(t)
			directory, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = directory.Close() })
			gitCalls := 0
			ports := reconciliationBundlePorts{
				secureGit: func(context.Context, *canonicalRepository, ...string) error {
					gitCalls++
					if failed == "verify" && gitCalls == 2 {
						return marker
					}
					return nil
				},
				advertise: func(context.Context, *canonicalRepository, string, string, string) error {
					if failed == "advertisement" {
						return marker
					}
					return nil
				},
				readBytes: func(*os.File, string) ([]byte, error) {
					if failed == "readback" {
						return nil, marker
					}
					return []byte("verified bundle"), nil
				},
				writeBytes: func(*os.File, string, []byte, os.FileMode, bool) error {
					if failed == "durable write" {
						return marker
					}
					return nil
				},
			}
			err = bundleClaimHead(context.Background(), canonical, directory, "event-1", "local", "refs/heads/wb/claim", strings.Repeat("a", 40), ports)
			if !errors.Is(err, marker) {
				t.Fatalf("%s error = %v", failed, err)
			}
		})
	}
}

func TestBranchReconciliationLifecycleRejectsUnreadableProjectsRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	if _, err := reconciliationLifecycleEvidence(context.Background(), filepath.Join(file, "child"), claim.Worktree, claim); err == nil {
		t.Fatal("list accepted a projects root beneath a regular file")
	}
}
