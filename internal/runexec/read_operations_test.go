package runexec

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"github.com/sneat-dev/wb/internal/runlog"
	"os"
	"testing"
	"time"
)

func TestHistoryPropagatesDirectoryAndReadErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("read failed")
	ops := HistoryOperations{Getwd: func() (string, error) { return "", sentinel }}
	if _, err := ops.Run(context.Background(), HistoryRequest{Days: 2}); err != sentinel {
		t.Fatal(err)
	}
	ops.Getwd = func() (string, error) { return "/repo", nil }
	ops.Read = func(root string) ([]runlog.Event, string, error) {
		if root != "/repo" {
			t.Fatal(root)
		}
		return nil, "", sentinel
	}
	if _, err := ops.Run(context.Background(), HistoryRequest{Days: 2}); err != sentinel {
		t.Fatal(err)
	}
}
func TestSubmissionResolvesDirectoryAndPassesExplicitIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("cwd failed")
	ops := SubmissionOperations{Getwd: func() (string, error) { return "", sentinel }}
	if _, err := ops.Run(context.Background(), SubmitRequest{}); err != sentinel {
		t.Fatal(err)
	}
	args := []string{"go", "test"}
	ctx := context.WithValue(context.Background(), testContextKey{}, true)
	ops.Getwd = func() (string, error) { return "/repo", nil }
	ops.Send = func(got context.Context, r SubmitRequest, s Submission) (operationreceipt.Receipt, error) {
		if got != ctx || r.ProjectsRoot != "/projects" || s.WorkingDirectory != "/repo" || s.WorkerID != "worker" || s.IdempotencyKey != "key" || len(s.Argv) != 2 {
			t.Fatal(s)
		}
		s.Argv[0] = "changed"
		return operationreceipt.Receipt{OperationID: "actual"}, sentinel
	}
	result, err := ops.Run(ctx, SubmitRequest{ProjectsRoot: "/projects", Argv: args, WorkerID: "worker", IdempotencyKey: "key"})
	if err != sentinel || result.OperationID != "actual" || args[0] != "go" {
		t.Fatalf("result=%+v err=%v argv=%v", result, err, args)
	}
}
func TestLoadHintLooksUpOnlyEnabledFloorAndFallsBackOnFailure(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("load unavailable")
	for _, tc := range []struct {
		floor  float64
		reason string
		err    error
		want   string
		calls  int
	}{{0, "disabled", nil, "capacity contention", 0}, {0, "", nil, "capacity contention", 0}, {4, "", sentinel, "capacity contention", 1}, {4, "", nil, "host load 5.0>4.0", 1}} {
		calls := 0
		result := loadHint("config", func(path string) (float64, string) {
			if path != "config" {
				t.Fatal(path)
			}
			return tc.floor, tc.reason
		}, func() (float64, error) { calls++; return 5, tc.err })
		if result != tc.want || calls != tc.calls {
			t.Fatalf("hint=%q calls=%d", result, calls)
		}
	}
}
func TestRealDefaultReadOperationsAndConstructors(t *testing.T) {
	t.Parallel()
	send := func(context.Context, SubmitRequest, Submission) (operationreceipt.Receipt, error) {
		return operationreceipt.Receipt{OperationID: "actual"}, nil
	}
	if _, err := NewSubmission(send).Run(context.Background(), SubmitRequest{}); err != nil {
		t.Fatal(err)
	}
	// The checkout can itself be managed; compare with the actual read source,
	// rather than assuming the operating harness's directory is unmanaged.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, path, readErr := runlog.ReadCurrent(cwd)
	result, historyErr := History(context.Background(), HistoryRequest{Days: 1})
	if (readErr != nil) != (historyErr != nil) {
		t.Fatalf("source error=%v history error=%v", readErr, historyErr)
	}
	if historyErr == nil && result.Path != path {
		t.Fatal(result)
	}

	root := t.TempDir()
	if listing := Queue(QueueRequest{ProjectsRoot: root}); listing.Budget < 1 {
		t.Fatal(listing)
	}
	if hint := LoadHint(""); hint != "capacity contention" {
		t.Fatal(hint)
	}
	if _, err := Changed(context.Background(), ChangedRequest{Target: "not-a-ref"}); err == nil {
		t.Fatal("nonrepository target must fail")
	}
	if Heartbeat != 10*time.Second || AdmissionGrace != 200*time.Millisecond {
		t.Fatal("production cadence changed")
	}
}
