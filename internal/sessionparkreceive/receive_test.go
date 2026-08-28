package sessionparkreceive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestReceiveCompletesOneAndTwoMemberBundles(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(string(rune('0'+count))+"-members", func(t *testing.T) {
			fixture := newReceiveFixture(t, count)
			result, err := Receive(context.Background(), fixture.options())
			if err != nil {
				t.Fatal(err)
			}
			if result.Phase != PhaseCompleted || result.Receipt == nil || len(result.Receipt.Members) != count {
				t.Fatalf("result = %#v", result)
			}
			if got := fixture.prepared.Load(); got != int32(count) {
				t.Fatalf("prepared claims = %d, want %d", got, count)
			}
			if got := fixture.completed.Load(); got != int32(count) {
				t.Fatalf("completed claims = %d, want %d", got, count)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), fixture.request.Continuation) || strings.Contains(string(raw), sessionpark.SuccessorContextFileName) {
				t.Fatalf("normal receiver output disclosed private continuation data: %s", raw)
			}
			for index, member := range result.Receipt.Members {
				if member.Repository != fixture.request.Members[index].Repository || member.TargetPath == "" || member.Pin == "" || member.TargetWorkLogReference == "" {
					t.Fatalf("receipt member = %#v", member)
				}
			}
		})
	}
}

func TestReceiveTargetMachineGuardRefusesBeforeAdmissionOrMemberMutation(t *testing.T) {
	fixture := newReceiveFixture(t, 1)
	options := fixture.options()
	options.LocalMachine = "wrong-machine"
	if _, err := Receive(context.Background(), options); err == nil || !strings.Contains(err.Error(), "targets machine") {
		t.Fatalf("error = %v", err)
	}
	if fixture.received.Load() != 0 || fixture.prepared.Load() != 0 || fixture.completed.Load() != 0 {
		t.Fatal("target guard allowed member mutation")
	}
	if _, err := os.Stat(options.Store.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target store mutated before machine authorization: %v", err)
	}
}

func TestReceiveRetriesAfterMembersAndReceiptInterruptions(t *testing.T) {
	for _, point := range []string{"members", "receipt"} {
		t.Run(point, func(t *testing.T) {
			fixture := newReceiveFixture(t, 2)
			injected := errors.New("injected interruption")
			var once atomic.Bool
			options := fixture.options()
			if point == "members" {
				options.AfterMembersReady = func() error {
					if once.CompareAndSwap(false, true) {
						return injected
					}
					return nil
				}
			} else {
				options.AfterReceipt = func() error {
					if once.CompareAndSwap(false, true) {
						return injected
					}
					return nil
				}
			}
			if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
				t.Fatalf("first receive error = %v", err)
			}
			result, err := Receive(context.Background(), options)
			if err != nil || result.Receipt == nil || result.Phase != PhaseCompleted {
				t.Fatalf("retry result=%#v err=%v", result, err)
			}
		})
	}
}

func TestReceiveRetriesAfterIndividualMemberInterruptions(t *testing.T) {
	injected := errors.New("injected per-member interruption")
	t.Run("receive second member", func(t *testing.T) {
		fixture := newReceiveFixture(t, 2)
		options := fixture.options()
		baseReceive := options.ReceiveMember
		baseStart := options.StartSuccessor
		var interrupted atomic.Bool
		var successfulLaunches atomic.Int32
		options.ReceiveMember = func(ctx context.Context, member worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
			if member.Spec.MemberKey == fixture.request.Members[1].MemberID && interrupted.CompareAndSwap(false, true) {
				return worktrees.SessionReceiveResult{}, injected
			}
			return baseReceive(ctx, member)
		}
		options.StartSuccessor = func(ctx context.Context, launch sessionlaunch.Options) (sessionlaunch.Result, error) {
			result, err := baseStart(ctx, launch)
			if err == nil {
				successfulLaunches.Add(1)
			}
			return result, err
		}
		if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
			t.Fatalf("first receive error = %v", err)
		}
		result, err := Receive(context.Background(), options)
		if err != nil || result.Phase != PhaseCompleted || result.Receipt == nil {
			t.Fatalf("retry result=%#v err=%v", result, err)
		}
		if successfulLaunches.Load() != 1 || fixture.received.Load() != 3 {
			t.Fatalf("successful launches=%d member receives=%d", successfulLaunches.Load(), fixture.received.Load())
		}
	})

	t.Run("prepare second claim", func(t *testing.T) {
		fixture := newReceiveFixture(t, 2)
		options := fixture.options()
		basePrepare := options.PrepareMember
		baseStart := options.StartSuccessor
		var interrupted atomic.Bool
		var successfulLaunches atomic.Int32
		options.PrepareMember = func(ctx context.Context, member worktrees.ParkedSessionWorkLogPrepareOptions) (worktrees.ParkedSessionWorkLogPrepareResult, error) {
			if member.Member.MemberID == fixture.request.Members[1].MemberID && interrupted.CompareAndSwap(false, true) {
				return worktrees.ParkedSessionWorkLogPrepareResult{}, injected
			}
			return basePrepare(ctx, member)
		}
		options.StartSuccessor = func(ctx context.Context, launch sessionlaunch.Options) (sessionlaunch.Result, error) {
			result, err := baseStart(ctx, launch)
			if err == nil {
				successfulLaunches.Add(1)
			}
			return result, err
		}
		if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
			t.Fatalf("first claim error = %v", err)
		}
		result, err := Receive(context.Background(), options)
		if err != nil || result.Phase != PhaseCompleted || result.Receipt == nil {
			t.Fatalf("retry result=%#v err=%v", result, err)
		}
		if successfulLaunches.Load() != 1 || fixture.prepared.Load() != 3 {
			t.Fatalf("successful launches=%d prepared claims=%d", successfulLaunches.Load(), fixture.prepared.Load())
		}
	})

	t.Run("complete second owner", func(t *testing.T) {
		fixture := newReceiveFixture(t, 2)
		options := fixture.options()
		baseComplete := options.CompleteMember
		baseStart := options.StartSuccessor
		var interrupted atomic.Bool
		var starts atomic.Int32
		var launched sessionlaunch.Result
		options.StartSuccessor = func(ctx context.Context, launch sessionlaunch.Options) (sessionlaunch.Result, error) {
			starts.Add(1)
			result, err := baseStart(ctx, launch)
			if err == nil {
				launched = result
			}
			return result, err
		}
		options.InspectSuccessor = func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			if launched.WBSessionID == "" {
				return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
			}
			return launched, nil
		}
		options.CompleteMember = func(member worktrees.ParkedTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error) {
			if member.Member.MemberID == fixture.request.Members[1].MemberID && interrupted.CompareAndSwap(false, true) {
				return worktrees.LocalWorkLogEvent{}, injected
			}
			return baseComplete(member)
		}
		if _, err := Receive(context.Background(), options); !errors.Is(err, injected) {
			t.Fatalf("first completion error = %v", err)
		}
		result, err := Receive(context.Background(), options)
		if err != nil || result.Phase != PhaseCompleted || result.Receipt == nil {
			t.Fatalf("retry result=%#v err=%v", result, err)
		}
		if starts.Load() != 1 || fixture.completed.Load() != 3 {
			t.Fatalf("successor starts=%d completed owners=%d", starts.Load(), fixture.completed.Load())
		}
	})
}

func TestConcurrentReceiveCreatesOneSuccessorAndIdenticalReceipt(t *testing.T) {
	fixture := newReceiveFixture(t, 2)
	options := fixture.options()
	var launches atomic.Int32
	baseStart := options.StartSuccessor
	options.StartSuccessor = func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
		launches.Add(1)
		return baseStart(ctx, options)
	}
	results := make(chan Result, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := Receive(context.Background(), options)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if launches.Load() != 1 {
		t.Fatalf("successor launches = %d, want 1", launches.Load())
	}
	var receipts []sessionpark.Receipt
	for result := range results {
		receipts = append(receipts, *result.Receipt)
	}
	if !reflect.DeepEqual(receipts[0], receipts[1]) {
		t.Fatalf("concurrent receipts differ: %#v %#v", receipts[0], receipts[1])
	}
}

type receiveFixture struct {
	t         *testing.T
	request   sessionpark.RemoteRequest
	raw       []byte
	store     sessionpark.TargetStore
	paths     []string
	received  atomic.Int32
	prepared  atomic.Int32
	completed atomic.Int32
	startedAt time.Time
}

func newReceiveFixture(t *testing.T, members int) *receiveFixture {
	t.Helper()
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: "resume-test", ParkedSessionID: "park-test",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "source", TargetMachine: "target", SourceRuntime: "codex",
		Continuation: "private continuation marker", CreatedAt: time.Unix(100, 0).UTC(),
	}
	paths := make([]string, members)
	for index := range members {
		commit := strings.Repeat(string(rune('a'+index)), 40)
		request.Members = append(request.Members, sessionpark.RemoteMember{
			MemberID: "m-00" + string(rune('1'+index)) + "-abcdef01", Repository: "acme/app" + string(rune('a'+index)),
			RepositoryRemote: "https://github.com/acme/app" + string(rune('a'+index)) + ".git", Branch: "feature/test",
			Commit: commit, SourceWorkLogReference: "worklog:parked/run/" + strings.Repeat(string(rune('b'+index)), 64),
		})
		paths[index] = filepath.Join(t.TempDir(), "target", string(rune('a'+index)))
	}
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	return &receiveFixture{t: t, request: request, raw: raw,
		store: sessionpark.NewTargetStore(filepath.Join(t.TempDir(), sessionpark.TargetDirName)), paths: paths,
		startedAt: time.Unix(200, 0).UTC()}
}

func (fixture *receiveFixture) options() Options {
	return Options{
		Store: fixture.store, ProjectsRoot: fixture.t.TempDir(), LocalMachine: "target", RawEnvelope: fixture.raw,
		ReceiveMember: func(_ context.Context, options worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
			index := fixture.memberIndex(options.Spec.MemberKey)
			fixture.received.Add(1)
			return worktrees.SessionReceiveResult{Repository: fixture.request.Members[index].Repository,
				WorktreeDir: fixture.paths[index], Commit: fixture.request.Members[index].Commit}, nil
		},
		VerifyMember: func(_ context.Context, options worktrees.SessionMemberReceiveOptions) (worktrees.SessionReceiveResult, error) {
			index := fixture.memberIndex(options.Spec.MemberKey)
			return worktrees.SessionReceiveResult{Repository: fixture.request.Members[index].Repository,
				WorktreeDir: fixture.paths[index], Commit: fixture.request.Members[index].Commit, Reused: true}, nil
		},
		InspectSuccessor: func(context.Context, sessionlaunch.Options) (sessionlaunch.Result, error) {
			return sessionlaunch.Result{}, sessionlaunch.ErrNotReleased
		},
		StartSuccessor: func(ctx context.Context, options sessionlaunch.Options) (sessionlaunch.Result, error) {
			record := session.Record{PID: 4242, WBSessionID: fixture.request.SuccessorWBSessionID,
				PredecessorWBSessionID: fixture.request.PredecessorWBSessionID, Machine: fixture.request.TargetMachine,
				Runtime: "codex", TmuxName: "wb-session-" + fixture.request.SuccessorWBSessionID,
				HandoffID: fixture.request.ResumeID, StartedAt: fixture.startedAt}
			ref, err := options.BeforeRelease(ctx, sessionlaunch.Prepared{Authority: *options.Authority,
				RequestDigest: sessionmove.Digest(options.Authority.AggregateDigest), Session: record,
				AttemptID: "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AttemptIndex: 1,
				WorktreeDir: options.WorktreeDir, PinnedCommit: options.PinnedCommit})
			if err != nil {
				return sessionlaunch.Result{}, err
			}
			return sessionlaunch.Result{HandoffID: fixture.request.ResumeID, WBSessionID: record.WBSessionID,
				PredecessorWBSessionID: record.PredecessorWBSessionID, TargetMachine: record.Machine, PID: record.PID,
				AttemptID: "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AttemptIndex: 1, TmuxName: record.TmuxName,
				Runtime: record.Runtime, TargetWorkLogRef: ref, WorktreeDir: options.WorktreeDir,
				PinnedCommit: options.PinnedCommit, StartedAt: record.StartedAt}, nil
		},
		PrepareMember: func(_ context.Context, options worktrees.ParkedSessionWorkLogPrepareOptions) (worktrees.ParkedSessionWorkLogPrepareResult, error) {
			fixture.prepared.Add(1)
			reference, err := sessionpark.TargetWorkLogReference(options.Request, options.RequestDigest, options.Member)
			return worktrees.ParkedSessionWorkLogPrepareResult{WorkLogReference: reference}, err
		},
		CompleteMember: func(options worktrees.ParkedTargetCompletionOptions) (worktrees.LocalWorkLogEvent, error) {
			fixture.completed.Add(1)
			return worktrees.LocalWorkLogEvent{}, nil
		},
	}
}

func (fixture *receiveFixture) memberIndex(id string) int {
	fixture.t.Helper()
	for index, member := range fixture.request.Members {
		if member.MemberID == id {
			return index
		}
	}
	fixture.t.Fatalf("unknown member %q", id)
	return -1
}
