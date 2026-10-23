package worktreeclaims

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func reconciliationTestClaim(worktree string) (Claim, Projection) {
	claim := Claim{Version: 1, EffortID: "effort", RunID: "run", Task: "task",
		Repository: "acme/app", Worktree: worktree, Branch: "wb/claim", Base: "main",
		BaseSHA: strings.Repeat("a", 40), Lifecycle: "active"}
	claim.ClaimID = WorkLogClaimID(claim.EffortID, CreationResult{Repository: claim.Repository,
		WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	return claim, Projection{EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID}
}

func TestReconciliationReadClaimPortFailures(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	claim, projection := reconciliationTestClaim(worktree)
	marker := errors.New("port failure")
	openRun := func(string, string, string, bool) (*os.File, string, error) {
		file, err := os.Open(t.TempDir())
		return file, "", err
	}
	for _, tc := range []struct {
		name string
		port ReconciliationPorts
		want string
	}{
		{"missing projection reader", ReconciliationPorts{}, "required"},
		{"projection read", ReconciliationPorts{ReadProjection: func(string) (Projection, error) { return Projection{}, marker }}, "port failure"},
		{"run open", ReconciliationPorts{ReadProjection: func(string) (Projection, error) { return projection, nil }, OpenRun: func(string, string, string, bool) (*os.File, string, error) { return nil, "", marker }}, "port failure"},
		{"claim read", ReconciliationPorts{ReadProjection: func(string) (Projection, error) { return projection, nil }, OpenRun: openRun, ReadClaimAt: func(*os.File, string) (Claim, error) { return Claim{}, marker }}, "port failure"},
		{"claim shape", ReconciliationPorts{ReadProjection: func(string) (Projection, error) { return projection, nil }, OpenRun: openRun, ReadClaimAt: func(*os.File, string) (Claim, error) { bad := claim; bad.Lifecycle = "terminal"; return bad, nil }}, "does not match"},
		{"success", ReconciliationPorts{ReadProjection: func(string) (Projection, error) { return projection, nil }, OpenRun: openRun, ReadClaimAt: func(*os.File, string) (Claim, error) { return claim, nil }}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotProjection, gotClaim, err := tc.port.ReadClaim(t.TempDir(), worktree)
			if tc.want == "" {
				if err != nil || gotProjection.ClaimID != projection.ClaimID || gotClaim.ClaimID != claim.ClaimID {
					t.Fatalf("read claim = %+v %+v, %v", gotProjection, gotClaim, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("read claim error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReconciliationPrivateRecordPortFailures(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	claim, _ := reconciliationTestClaim(worktree)
	record := ReconciliationRecord{EventID: "event-1"}
	marker := errors.New("port failure")
	openRun := func(string, string, string, bool) (*os.File, string, error) {
		file, err := os.Open(t.TempDir())
		return file, "", err
	}
	for _, tc := range []struct {
		name string
		port ReconciliationPorts
		want string
	}{
		{"run", ReconciliationPorts{OpenRun: func(string, string, string, bool) (*os.File, string, error) { return nil, "", marker }}, "port failure"},
		{"collection", ReconciliationPorts{OpenRun: openRun, OpenChild: func(*os.File, string, bool) (*os.File, error) { return nil, marker }}, "port failure"},
		{"event", ReconciliationPorts{OpenRun: openRun, OpenChild: func() func(*os.File, string, bool) (*os.File, error) {
			n := 0
			return func(*os.File, string, bool) (*os.File, error) {
				n++
				if n == 2 {
					return nil, marker
				}
				return os.Open(t.TempDir())
			}
		}()}, "port failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if directory, err := tc.port.OpenEvent(t.TempDir(), claim, record.EventID, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				if directory != nil {
					_ = directory.Close()
				}
				t.Fatalf("open event error = %v", err)
			}
		})
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	child := func(*os.File, string, bool) (*os.File, error) { return os.Open(t.TempDir()) }
	ports := ReconciliationPorts{OpenRun: openRun, OpenChild: child,
		WriteJSON: func(*os.File, string, any, os.FileMode) error { return marker },
		ReadJSON:  func(*os.File, string, any) error { return marker }}
	if got, err := ports.CreateRecord(t.TempDir(), claim, record); err == nil || got != nil {
		t.Fatalf("create = %v, %v", got, err)
	}
	if _, got, err := ports.ReadRecord(t.TempDir(), claim, record.EventID); err == nil || got != nil {
		t.Fatalf("read = %v, %v", got, err)
	}
	if err := ports.WriteRecord(directory, record); !errors.Is(err, marker) {
		t.Fatalf("write = %v", err)
	}
}
