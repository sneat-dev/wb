package worktrees

import (
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestZeroCoverageHelpersBatch2ExternalTargetReceivedEvent(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.September, 29, 12, 0, 0, 123, time.FixedZone("test", 90*60))
	request := sessionmove.Request{HandoffID: "handoff-1", PredecessorWBSessionID: "source-1"}
	digest := sessionmove.Digest("sha256:" + strings.Repeat("a", 64))
	event := externalTargetReceivedEvent(request, digest, "worklog:effort/run/claim", at)
	if event.ID != externalLocalEventID("target-received", digest, "") || event.Type != LocalEventHandoff ||
		event.Message != "external session handoff received" || event.Result != "received" || !event.At.Equal(at.UTC()) {
		t.Fatalf("received event = %#v", event)
	}
	if event.Extra["handoff_id"] != request.HandoffID || event.Extra["endpoint"] != "target" ||
		event.Extra["target_work_log_reference"] != "worklog:effort/run/claim" {
		t.Fatalf("received event extra = %#v", event.Extra)
	}
}

func TestZeroCoverageHelpersBatch2TerminalWrappers(t *testing.T) {
	t.Parallel()

	testWrapper := func(name string, write func(string, *os.File, workLogClaim) (time.Time, error)) workLogTerminalRecord {
		t.Helper()
		home := filepath.Join(t.TempDir(), ".wb")
		claim := workLogClaim{
			Version: 1, EffortID: "effort-" + name, RunID: "run", ClaimID: "claim-" + name,
			Repository: "acme/app", Branch: "wb/coverage", Base: "main", BaseSHA: strings.Repeat("b", 40),
			Lifecycle: "active", RecordedAt: time.Now().UTC(),
		}
		runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
		if err != nil {
			t.Fatalf("open %s run: %v", name, err)
		}
		defer func() { _ = runDir.Close() }()
		sealedAt, err := write(home, runDir, claim)
		if err != nil || sealedAt.IsZero() {
			t.Fatalf("write %s terminal = %s, %v", name, sealedAt, err)
		}
		terminal, err := readWorkLogTerminalAt(runDir, claim.ClaimID)
		if err != nil {
			t.Fatalf("read %s terminal: %v", name, err)
		}
		return terminal
	}

	plain := testWrapper("plain", func(home string, runDir *os.File, claim workLogClaim) (time.Time, error) {
		return sealWorkLogTerminal(home, runDir, worktreeclaims.TerminalSealRequest{Claim: claim, FinalCommit: strings.Repeat("c", 40), Disposition: "landed"})
	})
	if plain.DirtyCapture != nil || plain.Supersession != nil {
		t.Fatalf("plain terminal carried optional evidence: %#v", plain)
	}

	dirty := &DirtyWorktreeEvidence{SHA256: strings.Repeat("d", 64), Bytes: 12, Files: 2}
	dirtyTerminal := testWrapper("dirty", func(home string, runDir *os.File, claim workLogClaim) (time.Time, error) {
		return sealWorkLogTerminal(home, runDir, worktreeclaims.TerminalSealRequest{Claim: claim, FinalCommit: strings.Repeat("e", 40), Disposition: "discarded", Evidence: worktreeclaims.TerminalEvidence{DirtyCapture: dirty}})
	})
	if !worktreeclaims.SameDirtyWorktreeEvidence(dirtyTerminal.DirtyCapture, dirty) {
		t.Fatalf("dirty terminal evidence = %#v", dirtyTerminal.DirtyCapture)
	}

	supersession := &SupersessionReceipt{Version: 1, Repository: "acme/app", Task: "coverage", Branch: "wb/coverage"}
	supersededTerminal := testWrapper("superseded", func(home string, runDir *os.File, claim workLogClaim) (time.Time, error) {
		return sealWorkLogTerminal(home, runDir, worktreeclaims.TerminalSealRequest{Claim: claim, FinalCommit: strings.Repeat("f", 40), Disposition: "superseded", Evidence: worktreeclaims.TerminalEvidence{Supersession: supersession}})
	})
	if !worktreeproof.SameSupersessionReceipt(supersededTerminal.Supersession, supersession) {
		t.Fatalf("superseded terminal evidence = %#v", supersededTerminal.Supersession)
	}
}

func TestZeroCoverageHelpersBatch2CanonicalRepositoryPathForURL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got, err := CanonicalRepositoryPathForURL(root, "acme/app", "https://github.com/acme/app.git")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "github.com", "acme", "app")
	if got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
	if _, err := CanonicalRepositoryPathForURL("", "acme/app", "https://github.com/acme/app.git"); err == nil {
		t.Fatal("empty projects root was accepted")
	}
	if _, err := CanonicalRepositoryPathForURL(root, "bad", "https://github.com/acme/app.git"); err == nil {
		t.Fatal("invalid repository coordinate was accepted")
	}
}
