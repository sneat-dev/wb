package sessionrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkcourier"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalResumePreservesInspectionAndPersistenceFailureOrder(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"remote-route", "encode", "context-read", "authority", "inspect", "prepared-empty", "prepared-error", "prepared-success", "prepared-retry", "retryable", "retryable-typed", "prepare", "context-write", "fresh-authority", "root", "attach", "after", "resume-write", "inspected-attach", "no-final"} {
		t.Run(stage, func(t *testing.T) {
			f := newParkedResumeRefusalFixture(t)
			state := f.state
			want := errors.New("resume stage " + stage)
			calls := 0
			deps := ResumeDependencies{
				WithLocalCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, _ string, proceed func(*worktrees.ParkedLocalCustody) error) error {
					calls++
					return proceed(nil)
				},
				AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error { return nil },
				StartLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
					return sessionlaunch.Result{}, want
				},
				InspectLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
					return sessionlaunch.Result{}, want
				},
				InspectPrepared: func(context.Context, sessionlaunch.Options) (string, error) { return "", want },
			}
			aggregate := filepath.Join(f.store.Root, state.Bundle.ParkedSessionID)
			if strings.HasPrefix(stage, "prepared") || strings.HasPrefix(stage, "retryable") || stage == "inspect" || stage == "authority" || stage == "context-read" || stage == "inspected-attach" {
				if _, _, err := f.store.PrepareLocalUnderLock(f.lock, time.Unix(20, 0)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := f.store.EnsureLocalSuccessorContextUnderLock(f.lock, nil); err != nil {
					t.Fatal(err)
				}
				var err error
				state, err = f.store.LoadUnderLock(f.lock)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch stage {
			case "remote-route":
				state.ResumeRoute = &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteRemote, TargetMachine: "target"}
			case "encode":
				state.Bundle.Continuation = ""
			case "context-read":
				if err := os.WriteFile(filepath.Join(aggregate, sessionpark.SuccessorContextFileName), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "authority":
				state.Bundle.Source.Model = "model\ninvalid"
			case "prepared-empty", "prepared-error", "prepared-success", "prepared-retry":
				deps.InspectLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
					return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
				}
				deps.InspectPrepared = func(context.Context, sessionlaunch.Options) (string, error) {
					if stage == "prepared-empty" {
						return "", nil
					}
					if stage == "prepared-success" {
						return "attempt-private", nil
					}
					if stage == "prepared-retry" {
						return "", sessionlaunch.ErrNotReleased
					}
					return "", want
				}
			case "retryable", "retryable-typed":
				deps.InspectLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
					if stage == "retryable-typed" {
						return sessionlaunch.Result{}, errors.Join(sessionlaunch.ErrRetryableLaunch, &sessionlaunch.AttemptFailureError{Evidence: sessionlaunch.FailureEvidence{AttemptID: "attempt-private"}})
					}
					return sessionlaunch.Result{}, sessionlaunch.ErrRetryableLaunch
				}

			case "fresh-authority":
				state.Bundle.Source.Model = "model\ninvalid"
			case "context-write":
				if err := os.Mkdir(filepath.Join(aggregate, sessionpark.SuccessorContextFileName), 0700); err != nil {
					t.Fatal(err)
				}
			case "root":
				// A zero-member aggregate must create its private neutral root, refusing a file there.
				state.Bundle.Worktrees = nil
				// The real lock retains the original member; use a separate valid zero-member store below.
				f = zeroMemberResumeFixture(t)
				state = f.state
				aggregate = filepath.Join(f.store.Root, state.Bundle.ParkedSessionID)
				if err := os.WriteFile(filepath.Join(aggregate, sessionpark.LocalNeutralDirName), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "attach", "after", "resume-write":
				deps.StartLocal = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
					record := session.Record{PID: 91, WBSessionID: options.Authority.SuccessorWBSessionID, PredecessorWBSessionID: state.Bundle.Source.WBSessionID, Machine: "source", Runtime: "codex", StartedAt: time.Unix(30, 0)}
					if _, err := options.BeforeRelease(ctx, sessionlaunch.Prepared{Session: record, AttemptID: "attempt", AttemptIndex: 1}); err != nil {
						return sessionlaunch.Result{}, err
					}
					return sessionlaunch.Result{PID: record.PID, WBSessionID: record.WBSessionID, PredecessorWBSessionID: record.PredecessorWBSessionID, TargetMachine: record.Machine, Runtime: record.Runtime, StartedAt: record.StartedAt}, nil
				}
				if stage == "attach" {
					deps.AttachLocal = func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error {
						return want
					}
				}
				deps.AfterLocalLaunch = func(sessionlaunch.Result) error {
					if stage == "after" {
						return want
					}
					if stage == "resume-write" {
						return os.WriteFile(filepath.Join(aggregate, "events"), []byte("blocked"), 0600)
					}
					return nil
				}
			case "inspected-attach":
				deps.InspectLocal = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
					return sessionlaunch.Result{PID: 91, WBSessionID: "successor", AttemptID: "attempt-private", AttemptIndex: 1}, nil
				}
				deps.AttachLocal = func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error {
					return want
				}
			case "no-final":
				deps.WithLocalCustody = func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error {
					return nil
				}
			}
			if stage == "prepare" {
				deps.WithLocalCustody = func(_ context.Context, _ string, _ sessionpark.Bundle, _ string, proceed func(*worktrees.ParkedLocalCustody) error) error {
					if err := os.Mkdir(filepath.Join(aggregate, "resume-route.json"), 0700); err != nil {
						t.Fatal(err)
					}
					return proceed(nil)
				}
			}
			_, err := resumeParkedLocal(f.root, context.Background(), deps, f.store, f.lock, state, time.Unix(20, 0))
			if err == nil {
				t.Fatal("failure accepted")
			}
			if stage == "context-read" && !strings.Contains(err.Error(), "context conflicts") {
				t.Fatalf("wrong context refusal: %v", err)
			}
			if stage == "attach" || stage == "after" || stage == "inspect" || stage == "prepared-error" || stage == "prepared-retry" || strings.HasPrefix(stage, "retryable") {
				if !errors.Is(err, want) {
					t.Fatalf("error=%v, want=%v", err, want)
				}
			}
			if stage == "remote-route" || stage == "encode" || stage == "authority" || stage == "context-read" || stage == "inspect" || stage == "prepared-empty" || stage == "prepared-error" {
				if calls != 0 {
					t.Fatalf("custody reached after refusal: %d", calls)
				}
			}
		})
	}
}

func zeroMemberResumeFixture(t *testing.T) parkedResumeRefusalFixture {
	t.Helper()
	root := t.TempDir()
	store := sessionpark.NewStore(filepath.Join(root, ".wb", sessionpark.SourceDirName))
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-zero", Source: session.Record{PID: 41, WBSessionID: "source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0)}, Continuation: "private", ParkedAt: time.Unix(11, 0)}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	state, err := store.LoadUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	return parkedResumeRefusalFixture{root: root, store: store, lock: lock, state: state}
}

func TestRemoteResumeRejectsTamperedDurableAdmissionBeforeTransport(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"prepare", "receipt", "no-final"} {
		t.Run(stage, func(t *testing.T) {
			f := newParkedResumeRefusalFixture(t)
			calls := 0
			aggregate := filepath.Join(f.store.Root, f.state.Bundle.ParkedSessionID)
			deps := ResumeDependencies{WithRemoteCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, proceed func() error) error {
				if stage == "no-final" {
					return nil
				}
				if stage == "prepare" {
					if err := os.Mkdir(filepath.Join(aggregate, "resume-route.json"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "receipt" {
					admission, err := f.store.PrepareRemoteUnderLock(f.lock, "target", "", "ssh", sessionmove.SSHConfig{Host: "target.example"}, time.Unix(20, 0))
					if err != nil {
						t.Fatal(err)
					}
					_ = admission
					if err := os.WriteFile(filepath.Join(aggregate, "receipt-target.json"), []byte("broken"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return proceed()
			}, DeliverSSH: func(context.Context, sessionmove.SSHConfig, []byte, sessionparkcourier.Options) (sessionparkcourier.Result, error) {
				calls++
				return sessionparkcourier.Result{}, errors.New("transport should not run")
			}}
			_, err := resumeParkedRemote(f.root, context.Background(), deps, f.store, f.lock, f.state, "target", "ssh", f.config, time.Unix(20, 0), io.Discard, filepath.Join(f.root, ".wb"))
			if err == nil {
				t.Fatal("tamper accepted")
			}
			if calls != 0 {
				t.Fatalf("transport reached %d: %v", calls, err)
			}
		})
	}
}

func TestResumeRepairsLifecycleOnlyAfterExactLocalWinner(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"missing", "failure", "load"} {
		t.Run(stage, func(t *testing.T) {
			f := zeroMemberResumeFixture(t)
			if _, _, err := f.store.PrepareLocalUnderLock(f.lock, time.Unix(20, 0)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ResumeUnderLock(f.lock, session.Record{PID: 91, WBSessionID: "successor", Machine: "source", Runtime: "codex", StartedAt: time.Unix(30, 0)}, time.Unix(30, 0)); err != nil {
				t.Fatal(err)
			}
			if err := f.lock.Close(); err != nil {
				t.Fatal(err)
			}
			if stage == "load" {
				if err := os.WriteFile(filepath.Join(f.store.Root, f.state.Bundle.ParkedSessionID, "events", "00000000000000000001.json"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := errors.New("lifecycle projection")
			deps := ResumeDependencies{Home: func(string) (string, error) { return filepath.Join(f.root, ".wb"), nil }}
			if stage == "failure" {
				deps.MarkResumed = func(string, int, string, string) (session.Record, error) { return session.Record{}, want }
			}
			_, err := NewResume(deps).Resume(context.Background(), ResumeRequest{ParkedSessionID: f.state.Bundle.ParkedSessionID, Stderr: io.Discard})
			if err == nil {
				t.Fatal("failed lifecycle accepted")
			}
			if stage == "failure" && !errors.Is(err, want) {
				t.Fatal(err)
			}
			if stage == "missing" && !strings.Contains(err.Error(), "projection dependency") {
				t.Fatal(err)
			}
		})
	}
}

func TestParkedRemoteConfigurationValidationAndRetainedRouteAreExact(t *testing.T) {
	t.Parallel()
	bad := &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteRemote, TargetMachine: "target", Courier: "ssh", SSHHost: "invalid host"}
	if _, err := parkedRemoteSSHConfig(bad, "target", "ssh", ""); err == nil {
		t.Fatal("invalid retained SSH accepted")
	}
	f := newParkedResumeRefusalFixture(t)
	state := f.state
	state.Status = sessionpark.StatusResumed
	state.ResumeRoute = bad
	state.Successor = &session.Record{WBSessionID: "successor"}
	state.RemoteReceipt = &sessionpark.Receipt{TargetMachine: "target"}
	if _, err := resumeParkedRemote(f.root, context.Background(), ResumeDependencies{}, f.store, f.lock, state, "target", "ssh", "", time.Unix(20, 0), io.Discard, filepath.Join(f.root, ".wb")); err == nil {
		t.Fatal("resumed malformed route accepted")
	}
	for _, body := range []string{"session_move:\n  targets:\n    other:\n      default_courier: ssh\n      ssh:\n        host: other.example\n", "session_move:\n  targets:\n    target:\n      default_courier: ssh\n      ssh:\n        host: invalid host\n"} {
		path := filepath.Join(t.TempDir(), "wb.yaml")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadParkedRemoteSSHConfig("target", "ssh", path); err == nil {
			t.Fatal("invalid remote configuration accepted")
		}
	}
	if _, err := loadParkedRemoteSSHConfig("target", "ssh", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if _, err := loadParkedRemoteSSHConfig("target", "ssh", ""); err == nil {
		t.Fatal("missing isolated default accepted")
	}
	if digest := publicReceiptDigest(sessionpark.Receipt{}); digest != "" {
		t.Fatalf("invalid public digest=%s", digest)
	}
}

func TestRemoteLifecycleAndReceiptPersistenceFailuresRemainAfterDelivery(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"projection-missing", "projection-failure", "receipt-write", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			f := newParkedResumeRefusalFixture(t)
			want := errors.New("projection failed")
			// The public operation must acquire its own native retained lock.
			if err := f.lock.Close(); err != nil {
				t.Fatal(err)
			}
			aggregate := filepath.Join(f.store.Root, f.state.Bundle.ParkedSessionID)
			deps := ResumeDependencies{Home: func(string) (string, error) { return filepath.Join(f.root, ".wb"), nil }, WithRemoteCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, proceed func() error) error { return proceed() }, DeliverSSH: func(_ context.Context, _ sessionmove.SSHConfig, raw []byte, _ sessionparkcourier.Options) (sessionparkcourier.Result, error) {
				envelope, err := sessionpark.DecodeEnvelope(raw)
				if err != nil {
					t.Fatal(err)
				}
				request := envelope.Request
				digest := sessionmove.DigestBytes(raw)
				runtime, model := sessionpark.RequestedRuntimeModel(request)
				receipt := sessionpark.Receipt{SchemaVersion: sessionpark.ReceiptSchemaVersion, ResumeID: request.ResumeID, RequestDigest: digest, ParkedSessionID: request.ParkedSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: runtime, Model: model, AttemptID: "000001-" + strings.Repeat("d", 32), AttemptIndex: 1, PID: 8181, StartedAt: time.Unix(50, 0).UTC(), Members: make([]sessionpark.ReceiptMember, len(request.Members))}
				for i, member := range request.Members {
					ref, err := sessionpark.TargetWorkLogReference(request, digest, member)
					if err != nil {
						t.Fatal(err)
					}
					receipt.Members[i] = sessionpark.ReceiptMember{MemberID: member.MemberID, Repository: member.Repository, TargetPath: "/target/" + member.MemberID, Pin: sessionpark.MemberPin(request.ResumeID, member.MemberID), Commit: member.Commit, TargetWorkLogReference: ref}
				}
				if err := sessionpark.ValidateReceipt(receipt, request, digest); err != nil {
					t.Fatal(err)
				}
				if stage == "receipt-write" {
					if err := os.Mkdir(filepath.Join(aggregate, "receipt-target.json"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "finalize" {
					if err := os.Remove(filepath.Join(aggregate, "events")); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(aggregate, "events"), []byte("blocked"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return sessionparkcourier.Result{Receipt: receipt}, nil
			}}
			if stage == "projection-failure" {
				deps.MarkResumed = func(string, int, string, string) (session.Record, error) { return session.Record{}, want }
			}
			_, err := NewResume(deps).Resume(context.Background(), ResumeRequest{ParkedSessionID: f.state.Bundle.ParkedSessionID, Target: "target", Via: "ssh", ConfigPath: f.config, Stderr: io.Discard})
			if err == nil {
				t.Fatal("failure accepted")
			}
			if stage == "projection-failure" && !errors.Is(err, want) {
				t.Fatal(err)
			}
			if stage == "projection-missing" && !strings.Contains(err.Error(), "projection dependency") {
				t.Fatal(err)
			}
			if stage == "finalize" {
				events := filepath.Join(aggregate, "events")
				if err := os.Remove(events); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(events, 0700); err != nil {
					t.Fatal(err)
				}
				deps.DeliverSSH = func(context.Context, sessionmove.SSHConfig, []byte, sessionparkcourier.Options) (sessionparkcourier.Result, error) {
					t.Fatal("persisted receipt was redelivered")
					return sessionparkcourier.Result{}, nil
				}
				deps.MarkResumed = func(string, int, string, string) (session.Record, error) { return f.state.Bundle.Source, nil }
				retry, retryErr := NewResume(deps).Resume(context.Background(), ResumeRequest{ParkedSessionID: f.state.Bundle.ParkedSessionID, Target: "target", Via: "ssh", ConfigPath: f.config, Stderr: io.Discard})
				if retryErr != nil || !retry.Replay || retry.Status != string(sessionpark.StatusResumed) {
					t.Fatalf("receipt repair=%+v error=%v", retry, retryErr)
				}
			}
		})
	}
}

func TestLocalInspectionRejectsPrivateNeutralRootAndAuthorityTampering(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"neutral-root", "authority"} {
		t.Run(stage, func(t *testing.T) {
			f := zeroMemberResumeFixture(t)
			if _, _, err := f.store.PrepareLocalUnderLock(f.lock, time.Unix(20, 0)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.store.EnsureLocalSuccessorContextUnderLock(f.lock, nil); err != nil {
				t.Fatal(err)
			}
			state, err := f.store.LoadUnderLock(f.lock)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "neutral-root" {
				if err := os.WriteFile(filepath.Join(f.store.Root, state.Bundle.ParkedSessionID, sessionpark.LocalNeutralDirName), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.store.LocalLaunchRootUnderLock(f.lock); err != nil {
					t.Fatal(err)
				}
				state.Bundle.Source.Model = "invalid\nmodel"
			}
			deps := ResumeDependencies{WithLocalCustody: func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error {
				t.Fatal("custody reached after tamper")
				return nil
			}, AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error { return nil }, StartLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
				return sessionlaunch.Result{}, nil
			}, InspectLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
				t.Fatal("inspect reached after tamper")
				return sessionlaunch.Result{}, nil
			}, InspectPrepared: func(context.Context, sessionlaunch.Options) (string, error) { return "", nil }}
			if _, err := resumeParkedLocal(f.root, context.Background(), deps, f.store, f.lock, state, time.Unix(20, 0)); err == nil {
				t.Fatal("tamper accepted")
			}
		})
	}
}
