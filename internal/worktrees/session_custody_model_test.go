package worktrees

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

func TestPreparedExternalTargetUsesSelectedModelThroughoutCustodyAndReceipt(t *testing.T) {
	for _, test := range []struct {
		name, requestedHarness, requestedModel, targetRuntime, targetModel string
		observedModel, earlyObserved                                       string
	}{
		{"explicit override", "", "new-model", "codex", "new-model", "", ""},
		{"same runtime default", "", "", "codex", "gpt-5", "", ""},
		{"cross runtime unspecified", "claude-code", "", "claude-code", "", "observed-model", ""},
		{"cross runtime explicit", "claude-code", "new-model", "claude-code", "new-model", "", ""},
		{"unspecified with early observation", "claude-code", "", "claude-code", "", "observed-model", "observed-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExternalTargetFixture(t)
			fixture.base.request.RequestedHarness = test.requestedHarness
			fixture.base.request.RequestedModel = test.requestedModel
			raw, err := sessionmove.EncodeRequest(fixture.base.request)
			if err != nil {
				t.Fatal(err)
			}
			fixture.digest = sessionmove.DigestBytes(raw)
			fixture.session.Runtime, fixture.session.Model = test.targetRuntime, test.targetModel
			if test.earlyObserved != "" {
				fixture.session.Model = test.earlyObserved
			}
			fixture.session.TmuxName = ""
			fixture.session.Terminal = sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(t.TempDir(), "herdr.sock"), WorkspaceID: "workspace-owned", PaneID: "pane-owned"}
			fixture.options.Request, fixture.options.RequestDigest = fixture.base.request, fixture.digest
			fixture.options.Session = fixture.session
			prepared, err := PrepareExternalSessionWorkLog(context.Background(), fixture.options)
			if err != nil {
				t.Fatalf("prepare selected target: %v", err)
			}
			claim, _, unlock, err := loadExternalTargetClaim(fixture.base.projectsRoot, fixture.base.request, fixture.digest, fixture.worktree)
			if err != nil {
				t.Fatalf("validate prepared claim and manifest: %v", err)
			}
			unlock()
			wantClaimModel, wantProvenance := test.targetModel, modelProvenanceCallerDeclared
			if wantClaimModel == "" {
				wantClaimModel, wantProvenance = "unknown", modelProvenanceUnknown
			}
			if claim.AgentRuntime != test.targetRuntime || claim.Model != wantClaimModel || claim.ModelProvenance != wantProvenance {
				t.Fatalf("claim lost selection: %#v", claim)
			}
			manifest, err := ReadManifest(fixture.worktree)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.AgentRuntime != test.targetRuntime || manifest.Model != test.targetModel {
				t.Fatalf("manifest lost selection: %#v", manifest)
			}
			receipt := fixture.receipt(t, prepared)
			receipt.Terminal = fixture.session.Terminal
			if test.observedModel != "" {
				receipt.Model = test.observedModel
			}
			if err := sessionmove.ValidateReceiptForRequest(receipt, fixture.base.request, fixture.digest); err != nil {
				t.Fatalf("selected receipt refused: %v", err)
			}
			completion := ExternalTargetCompletionOptions{ProjectsRoot: fixture.base.projectsRoot, Request: fixture.base.request, RequestDigest: fixture.digest, Receipt: receipt, WorktreeDir: fixture.worktree}
			if _, err := RecordExternalTargetCompleted(completion); err != nil {
				t.Fatalf("complete selected target custody: %v", err)
			}
			if test.targetModel != "" {
				completion.Receipt.Model = "different-known-model"
				if _, err := RecordExternalTargetCompleted(completion); err == nil {
					t.Fatal("changed known selected model accepted")
				}
			} else {
				completion.Receipt.Terminal = sessionterminal.Identity{}
				completion.Receipt.TmuxName = "wb-session-" + fixture.base.request.SuccessorWBSessionID
				if _, err := RecordExternalTargetCompleted(completion); err == nil {
					t.Fatal("legacy receipt accepted observational model enrichment")
				}
			}
		})
	}
}
