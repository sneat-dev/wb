//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestE2EParkCaptureRefusesTamperedOrUnboundLatestOwnerWithoutMutation(t *testing.T) {
	for _, kind := range []string{"agent-mismatch", "conflicting-bindings", "invalid-binding-type", "newer-unbound-owner", "foreign-bound-owner"} {
		t.Run(kind, func(t *testing.T) {
			fixture, worktree, source := newSessionCheckpointFixture(t, "park-owner-"+kind)
			branch := preparePushedParkedWorktree(t, fixture, worktree)
			guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
			if err != nil {
				t.Fatal(err)
			}
			owner := OwnerRegistration{PID: source.PID, Agent: source.Runtime + "/" + source.WBSessionID, At: time.Now().UTC()}
			extra := map[string]any{"owner_wb_session_id": source.WBSessionID}
			switch kind {
			case "agent-mismatch":
				owner.Agent = source.Runtime + "/foreign"
			case "conflicting-bindings":
				extra["successor_wb_session_id"] = "wbs-other"
			case "invalid-binding-type":
				extra["owner_wb_session_id"] = 12
			case "newer-unbound-owner":
				extra = nil
				owner.Agent = source.Runtime + "/" + source.NativeHarnessID
			case "foreign-bound-owner":
				owner.Agent = source.Runtime + "/wbs-foreign"
				extra["owner_wb_session_id"] = "wbs-foreign"
			}
			if _, _, err := appendLocalEventWithoutCustody(worktree, LocalWorkLogEvent{Type: LocalEventOwner, Message: "adversarial owner fixture", Owner: &owner, Extra: extra}); err != nil {
				t.Fatal(err)
			}
			eventsPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
			before, err := os.ReadFile(eventsPath)
			if err != nil {
				t.Fatal(err)
			}
			persisted := false
			err = CaptureParkedSessionAggregate(context.Background(), fixture.projectsRoot, []ListResult{{Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree, WorktreesRoot: guard.WorktreesRoot, Branch: branch}}, source, func([]sessionpark.Worktree) error { persisted = true; return nil })
			if err == nil || persisted {
				t.Fatalf("invalid latest owner admitted: %v", err)
			}
			after, err := os.ReadFile(eventsPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal changed journal: %v", err)
			}
		})
	}
}
