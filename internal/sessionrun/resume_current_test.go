package sessionrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCurrentPickupRequiresContextAcknowledgmentAndNeverLaunches(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"none", "attach", "registry", "source-marker"} {
		t.Run(fault, func(t *testing.T) {
			f := zeroMemberResumeFixture(t)
			caller := f.state.Bundle.Source
			caller.NativeHarnessID, caller.ProcessStartedAt = "current-native", time.Unix(5, 0)
			attached, committed, finalized := 0, 0, 0
			want := errors.New("interrupted " + fault)
			fail := fault != "none"
			attempt := ""
			deps := ResumeDependencies{
				ResolveCurrent: func(string) (session.Record, bool, error) { return caller, true, nil },
				WithLocalCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, replay string, proceed func(*worktrees.ParkedLocalCustody) error) error {
					if attempt != "" && replay != attempt {
						t.Fatal("retry lost stable attempt custody")
					}
					return proceed(nil)
				},
				AttachLocal: func(_ context.Context, _ *worktrees.ParkedLocalCustody, successor session.Record, id string, index uint64) error {
					attached++
					if successor.PID != caller.PID || successor.NativeHarnessID != caller.NativeHarnessID || index != 1 {
						t.Fatal("not current harness")
					}
					if attempt != "" && attempt != id {
						t.Fatal("retry changed attempt")
					}
					attempt = id
					if fail && fault == "attach" {
						fail = false
						return want
					}
					return nil
				},
				WithCurrentTransition: func(_ string, original, successor session.Record, apply func(func() error) error) error {
					return apply(func() error {
						if fail && fault == "registry" {
							fail = false
							return want
						}
						committed++
						caller = successor
						return nil
					})
				},
				MarkSourceResumed: func(_ string, source session.Record, parkID, successorID string) (session.Record, error) {
					if fail && fault == "source-marker" {
						fail = false
						return session.Record{}, want
					}
					finalized++
					return source, nil
				},
				StartLocal: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
					t.Fatal("current pickup launched another agent")
					return sessionlaunch.Result{}, nil
				},
			}
			request := ResumeRequest{ProjectsRoot: f.root, ParkedSessionID: f.state.Bundle.ParkedSessionID, Current: true}
			out, err := resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, f.state, time.Unix(30, 0))
			if err != nil || out.Status != "awaiting_context_ack" || out.ContinuationPath == "" || out.ContinuationDigest == "" {
				t.Fatalf("preparation: %#v %v", out, err)
			}
			if attached != 0 || committed != 0 || finalized != 0 {
				t.Fatal("preparation changed custody")
			}
			request.Acknowledge = out.ContinuationDigest
			out, err = resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, f.state, time.Unix(40, 0))
			if fault != "none" {
				if !errors.Is(err, want) {
					t.Fatalf("fault %s: %v", fault, err)
				}
				state, loadErr := f.store.LoadUnderLock(f.lock)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				out, err = resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, state, time.Unix(50, 0))
			}
			if err != nil || out.Status != "resumed" || finalized != 1 {
				t.Fatalf("pickup: %#v finalized=%d %v", out, finalized, err)
			}
		})
	}
}

func TestUnidentifiedCurrentPickupNeverFallsBackToLauncher(t *testing.T) {
	t.Parallel()
	f := zeroMemberResumeFixture(t)
	deps := ResumeDependencies{ResolveCurrent: func(string) (session.Record, bool, error) { return session.Record{}, false, nil }, WithLocalCustody: func(context.Context, string, sessionpark.Bundle, string, func(*worktrees.ParkedLocalCustody) error) error {
		t.Fatal("unidentified caller entered custody")
		return nil
	}, AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error { return nil }, WithCurrentTransition: session.WithCurrentTransition, MarkSourceResumed: session.MarkSourceResumed}
	if _, err := resumeParkedCurrent(context.Background(), deps, ResumeRequest{}, f.root, f.store, f.lock, f.state, time.Now()); err == nil {
		t.Fatal("missing caller accepted")
	}
}

func TestCurrentReplayRefusesDepartureAppearingBeforeMemberCustody(t *testing.T) {
	t.Parallel()
	f := zeroMemberResumeFixture(t)
	directory := filepath.Join(f.root, session.DirName)
	scope, err := session.NativeScope("codex", "native-departure")
	if err != nil {
		t.Fatal(err)
	}
	caller, err := session.Register(directory, session.Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "native-departure", Scope: scope, Model: session.Unknown, Machine: f.state.Bundle.Source.Machine})
	if err != nil {
		t.Fatal(err)
	}
	attachCount := 0
	depart := false
	custodyCount := 0
	deps := ResumeDependencies{
		ResolveCurrent: func(string) (session.Record, bool, error) { return caller, true, nil },
		WithLocalCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, _ string, proceed func(*worktrees.ParkedLocalCustody) error) error {
			custodyCount++
			if depart {
				if _, err := session.MarkRecordParked(directory, caller, "park-newer-departure"); err != nil {
					return err
				}
			}
			return proceed(nil)
		},
		AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error {
			attachCount++
			return nil
		},
		WithCurrentTransition: func(root string, original, successor session.Record, apply func(func() error) error) error {
			err := session.WithCurrentTransition(root, original, successor, apply)
			if err == nil {
				caller = successor
			}
			return err
		},
		MarkSourceResumed: func(_ string, source session.Record, _ string, _ string) (session.Record, error) { return source, nil },
	}
	request := ResumeRequest{ProjectsRoot: f.root, ParkedSessionID: f.state.Bundle.ParkedSessionID, Current: true}
	out, err := resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, f.state, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request.Acknowledge = out.ContinuationDigest
	out, err = resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, f.state, time.Now().UTC())
	if err != nil || out.Status != "resumed" {
		t.Fatalf("initial pickup: %#v %v", out, err)
	}
	state, err := f.store.LoadUnderLock(f.lock)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(directory, ".scopes", scope, fmt.Sprint(caller.PID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	attemptsBefore := attachCount
	custodyBefore := custodyCount
	depart = true
	_, err = resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, state, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "park-newer-departure") {
		t.Fatalf("replay ignored intervening departure: %v", err)
	}
	if attachCount != attemptsBefore || custodyCount != custodyBefore+1 {
		t.Fatal("departure fence ran outside member custody or after attachment")
	}
	after, err := os.ReadFile(filepath.Join(directory, ".scopes", scope, fmt.Sprint(caller.PID)+".json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refusal changed registry: %v", err)
	}
}

func TestCurrentPickupRefusesOriginalCallerDepartureBeforeAcknowledgment(t *testing.T) {
	for _, stage := range []string{"before acknowledgment", "before member custody", "before PID barrier"} {
		t.Run(stage, func(t *testing.T) {
			f := zeroMemberResumeFixture(t)
			directory := filepath.Join(f.root, session.DirName)
			scope, err := session.NativeScope("codex", "native-original-caller")
			if err != nil {
				t.Fatal(err)
			}
			caller, err := session.Register(directory, session.Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "native-original-caller", Scope: scope, Machine: f.state.Bundle.Source.Machine})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, ".scopes", scope, fmt.Sprint(caller.PID)+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			depart, attached, transitions := false, 0, 0
			parkCaller := func() error {
				_, err := session.MarkRecordParked(directory, caller, "park-caller-newer-checkpoint")
				return err
			}
			deps := ResumeDependencies{
				ResolveCurrent: func(string) (session.Record, bool, error) { return caller, true, nil },
				WithLocalCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, _ string, proceed func(*worktrees.ParkedLocalCustody) error) error {
					if depart && stage == "before member custody" {
						if err := parkCaller(); err != nil {
							return err
						}
					}
					return proceed(nil)
				},
				AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error {
					attached++
					return nil
				},
				WithCurrentTransition: func(root string, original, successor session.Record, apply func(func() error) error) error {
					transitions++
					if depart && stage == "before PID barrier" {
						if err := parkCaller(); err != nil {
							return err
						}
					}
					return session.WithCurrentTransition(root, original, successor, apply)
				},
				MarkSourceResumed: func(string, session.Record, string, string) (session.Record, error) {
					t.Fatal("departed caller finalized source")
					return session.Record{}, nil
				},
			}
			request := ResumeRequest{ProjectsRoot: f.root, ParkedSessionID: f.state.Bundle.ParkedSessionID, Current: true}
			prepared, err := resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, f.state, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			request.Acknowledge, depart = prepared.ContinuationDigest, true
			if stage == "before acknowledgment" {
				if err := parkCaller(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := resumeParkedCurrent(context.Background(), deps, request, f.root, f.store, f.lock, f.state, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "park-caller-newer-checkpoint") {
				t.Fatalf("prepared caller departure ignored: %v", err)
			}
			if attached != 0 || (stage == "before PID barrier" && transitions != 1) {
				t.Fatalf("attachment=%d transitions=%d", attached, transitions)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal mutated caller row: %v", err)
			}
			ack := filepath.Join(f.store.Root, f.state.Bundle.ParkedSessionID, "current-resume-ack.json")
			if _, err := os.Stat(ack); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("departure was acknowledged: %v", err)
			}
			state, err := f.store.LoadUnderLock(f.lock)
			if err != nil || state.Status != f.state.Status || state.Successor != nil {
				t.Fatalf("refusal mutated winner: %#v %v", state, err)
			}
		})
	}
}

func TestCurrentPickupAllowsItsExactAlreadyParkedSourceAndCompletionReplay(t *testing.T) {
	f := zeroMemberResumeFixture(t)
	directory := filepath.Join(f.root, session.DirName)
	scope, err := session.NativeScope("codex", "native-original-source")
	if err != nil {
		t.Fatal(err)
	}
	caller, err := session.Register(directory, session.Record{PID: os.Getpid(), Runtime: "codex", NativeHarnessID: "native-original-source", Scope: scope, Machine: f.state.Bundle.Source.Machine})
	if err != nil {
		t.Fatal(err)
	}
	bundle := f.state.Bundle
	bundle.Source = caller
	store := sessionpark.NewStore(filepath.Join(t.TempDir(), "own-source"))
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
	if _, err := session.MarkRecordParked(directory, caller, bundle.ParkedSessionID); err != nil {
		t.Fatal(err)
	}
	deps := ResumeDependencies{
		ResolveCurrent: func(string) (session.Record, bool, error) { return caller, true, nil },
		WithLocalCustody: func(_ context.Context, _ string, _ sessionpark.Bundle, _ string, proceed func(*worktrees.ParkedLocalCustody) error) error {
			return proceed(nil)
		},
		AttachLocal: func(context.Context, *worktrees.ParkedLocalCustody, session.Record, string, uint64) error { return nil },
		WithCurrentTransition: func(root string, original, successor session.Record, apply func(func() error) error) error {
			err := session.WithCurrentTransition(root, original, successor, apply)
			if err == nil {
				caller = successor
			}
			return err
		},
		MarkSourceResumed: session.MarkSourceResumed,
	}
	request := ResumeRequest{ProjectsRoot: f.root, ParkedSessionID: bundle.ParkedSessionID, Current: true}
	prepared, err := resumeParkedCurrent(context.Background(), deps, request, f.root, store, lock, state, time.Now().UTC())
	if err != nil {
		t.Fatalf("own parked-source preparation refused: %v", err)
	}
	request.Acknowledge = prepared.ContinuationDigest
	completed, err := resumeParkedCurrent(context.Background(), deps, request, f.root, store, lock, state, time.Now().UTC())
	if err != nil || completed.Status != "resumed" {
		t.Fatalf("own source pickup failed: %#v %v", completed, err)
	}
	state, err = store.LoadUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := resumeParkedCurrent(context.Background(), deps, request, f.root, store, lock, state, time.Now().UTC()); err != nil || replay.Status != "resumed" {
		t.Fatalf("own-source completion replay failed: %#v %v", replay, err)
	}
}

func TestUnstartedLaunchAssertionCannotSelectAnotherResumeMode(t *testing.T) {
	for _, request := range []ResumeRequest{
		{Launch: true}, {Target: "remote"}, {RecoverCurrent: true},
	} {
		request.UnstartedLaunchAcknowledgment = sessionmove.Digest("sha256:" + strings.Repeat("a", 64))
		service := NewResume(ResumeDependencies{Home: func(string) (string, error) {
			t.Fatal("incompatible assertion reached filesystem custody")
			return "", nil
		}})
		if _, err := service.Resume(context.Background(), request); err == nil || !strings.Contains(err.Error(), "--ack-unstarted-launch applies only") {
			t.Fatalf("incompatible mode accepted: %#v %v", request, err)
		}
	}
}
