package wbupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAfterUpdateDelegatesVerifiedRequestsInOrder(t *testing.T) {
	t.Parallel()
	var requests []ChildRequest
	var out, diagnostic bytes.Buffer
	timeout := 2 * time.Second
	service := Service{HandoffTimeout: func() time.Duration { return timeout }, RunChild: func(ctx context.Context, request ChildRequest) (ChildResult, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("child has no deadline")
		}
		requests = append(requests, request)
		if request.MergeOutput {
			return ChildResult{Combined: []byte("daemon receipt\n")}, nil
		}
		return ChildResult{Stdout: []byte("synced skills\n"), Stderr: []byte("not success output")}, nil
	}}
	update := successfulSelfUpdate("/verified/provider/wb")
	update.Outcome.Target = "0.96.4"
	var parent context.Context
	if err := service.AfterUpdate(parent, update, Output{Out: &out, Err: &diagnostic}); err != nil {
		t.Fatal(err)
	}
	want := []ChildRequest{{Path: update.Executable.Path, Args: []string{"daemon", "restart", "--if-running", "--format", "json"}, MergeOutput: true}, {Path: update.Executable.Path, Args: []string{"skills", "sync"}}}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%+v want=%+v", requests, want)
	}
	if out.String() != "Verified installed wb version: 0.96.4 (was 0.96.2).\nsynced skills\n" || diagnostic.String() != "daemon receipt\n" {
		t.Fatalf("out=%q diagnostic=%q", out.String(), diagnostic.String())
	}
}

func TestDaemonFailuresKeepCombinedDiagnosticAndDoNotBlockSkills(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "execution", true: "deadline"}[expired], func(t *testing.T) {
			t.Parallel()
			var diagnostic bytes.Buffer
			calls := 0
			service := Service{HandoffTimeout: func() time.Duration {
				if expired {
					return 0
				}
				return time.Second
			}, RunChild: func(ctx context.Context, r ChildRequest) (ChildResult, error) {
				calls++
				if r.MergeOutput {
					return ChildResult{Combined: []byte("out\nerr\nout2\n")}, errors.New("child failed")
				}
				return ChildResult{}, nil
			}}
			if err := service.AfterUpdate(context.Background(), successfulSelfUpdate("verified"), Output{Out: io.Discard, Err: &diagnostic}); err != nil {
				t.Fatal(err)
			}
			phrase := "reported a failure"
			if expired {
				phrase = "did not finish within"
			}
			if calls != 2 || !strings.Contains(diagnostic.String(), phrase) || !strings.HasSuffix(diagnostic.String(), "out\nerr\nout2\n") {
				t.Fatalf("calls=%d diagnostic=%s", calls, diagnostic.String())
			}
		})
	}
}

func TestSkillsEmptyVersionAndSplitFailure(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	update := successfulSelfUpdate("verified")
	update.Outcome.Result.Latest = ""
	service := Service{RunChild: func(_ context.Context, r ChildRequest) (ChildResult, error) {
		if r.MergeOutput {
			t.Fatal("skills merged streams")
		}
		return ChildResult{Stdout: []byte("do not include"), Stderr: []byte("failure details")}, errors.New("exit1")
	}}
	err := service.SyncSkills(Output{Out: &out, Err: io.Discard}, nil, update)
	if err == nil || !strings.Contains(err.Error(), "failure details") || strings.Contains(err.Error(), "do not include") || out.Len() != 0 {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

func TestConfigRejectsUnregisteredCatalogIdentity(t *testing.T) {
	t.Parallel()
	if failure := panicForCatalog("unregistered-maintenance-test"); failure == nil {
		t.Fatal("missing catalog id did not panic")
	}
}

func TestChildCaptureModesPreserveMergedOrderAndSplitStreams(t *testing.T) {
	t.Parallel()
	// Parent-owned immutable executable; each parallel child has separate process state.
	binary := fakeSelfUpdateBinary(t, `printf 'first\n'; printf 'second\n' >&2; printf 'third\n'`)
	for _, merged := range []bool{false, true} {
		t.Run(map[bool]string{false: "split", true: "merged"}[merged], func(t *testing.T) {
			t.Parallel()
			result, err := RunChild(context.Background(), ChildRequest{Path: binary, MergeOutput: merged})
			if err != nil {
				t.Fatal(err)
			}
			if merged {
				if string(result.Combined) != "first\nsecond\nthird\n" || len(result.Stdout)+len(result.Stderr) != 0 {
					t.Fatalf("result=%+v", result)
				}
			} else if string(result.Stdout) != "first\nthird\n" || string(result.Stderr) != "second\n" || len(result.Combined) != 0 {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestChildCancellationNeverExecutesProvider(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunChild(ctx, ChildRequest{Path: "/must-not-start/wb"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func panicForCatalog(id string) (failure any) {
	defer func() { failure = recover() }()
	ConfigFor(id, "1.2.3")
	return nil
}
