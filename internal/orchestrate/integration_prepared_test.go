package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/quality"
	"strings"
	"testing"
	"time"
)

func preparedTestRecord() LegacyProvenance {
	r := legacyTestRecord()
	r.Version = 2
	r.WB = ""
	r.PassedChecks = nil
	r.Validation = WorktreeMergeValidationIdentity{}
	r.ValidationDigest = ""
	r.Prepared = &PreparedProvenance{Status: "prepared-ci-deferred", PublishedHead: strings.Repeat("d", 40), PullRequest: "https://github.com/acme/app/pull/7", Deferral: PreparedDeferral{Route: WorktreeMergeRoutePullRequest, CandidateSHA: r.CandidateSHA, Policy: "authoritative-required-checks", RecordedAt: time.Unix(1, 0).UTC()}}
	return r
}
func TestPreparedProvenanceSeparatesCIdeferralFromCompletedValidation(t *testing.T) {
	t.Parallel()
	r := preparedTestRecord()
	body := preparedRecordPrefix + legacyJSON(r)
	parsed, err := legacyComment(body)
	if err != nil || parsed.Prepared.Status != "prepared-ci-deferred" {
		t.Fatal(parsed, err)
	}
	if _, err = legacyComment(legacyRecordPrefix + legacyJSON(r)); err == nil {
		t.Fatal("prepared evidence masqueraded as completed legacy")
	}
	leaked := strings.Replace(body, `"policy":"authoritative-required-checks"`, `"policy":"authoritative-required-checks","reason":"private-path/token-like-text"`, 1)
	if _, err := legacyComment(leaked); err == nil {
		t.Fatal("portable deferral accepted arbitrary receipt prose")
	}
	old := legacyTestRecord()
	if _, err = legacyComment(preparedRecordPrefix + legacyJSON(old)); err == nil {
		t.Fatal("completed evidence used prepared prefix")
	}
	for _, fault := range []string{"status", "version", "published", "PR", "source", "parent", "route", "time", "missing policy", "unknown policy", "passed", "validation", "wb", "identity"} {
		t.Run(fault, func(t *testing.T) {
			x := preparedTestRecord()
			switch fault {
			case "status":
				x.Prepared.Status = "complete"
			case "version":
				x.Version = 1
			case "published":
				x.Prepared.PublishedHead = "bad"
			case "PR":
				x.Prepared.PullRequest = "https://github.com/foreign/app/pull/7"
			case "source":
				x.Sources[0] = "bad"
			case "parent":
				x.Merges[0].FirstParent = x.CandidateSHA
			case "route":
				x.Prepared.Deferral.Route = WorktreeMergeRouteDirect
			case "time":
				x.Prepared.Deferral.RecordedAt = time.Time{}
			case "missing policy":
				x.Prepared.Deferral.Policy = ""
			case "unknown policy":
				x.Prepared.Deferral.Policy = "unknown"
			case "passed":
				x.PassedChecks = []string{"test"}
			case "validation":
				x.Validation.CandidateSHA = x.CandidateSHA
			case "wb":
				x.WB = "fake-completed-build"
			case "identity":
				x.Adopter.Session = ""
			}
			if validatePreparedRecord(x) == nil {
				t.Fatal("accepted", fault)
			}
		})
	}
}
func TestPreparedPortableInventoryKeepsUnreviewedMergeAndPublishedDescendant(t *testing.T) {
	t.Parallel()
	r := preparedTestRecord()
	body := preparedRecordPrefix + legacyJSON(r)
	binding := LegacyBinding{"https://github.com/acme/app/commit/" + r.CandidateSHA + "#commitcomment-10", legacyHash([]byte(body))}
	store := &legacyCommentStore{comments: map[int64]CommitComment{10: {CommitID: r.CandidateSHA, Body: body}}}
	walk := append([]integrationCommit{{SHA: r.Prepared.PublishedHead, Parents: []string{r.CandidateSHA}}}, legacyTestWalk(r)...)
	v := provenance{Unrecorded: []string{r.CandidateSHA}, Merges: legacyTestWalk(r)}
	if err := verifyLegacyProvenance(context.Background(), store, r.Repository, r.Integration, r.To, walk, &v, []LegacyBinding{binding}); err != nil || len(v.Unrecorded) != 0 || len(v.Merges) != 1 || len(v.Legacy) != 1 {
		t.Fatal(v, err)
	}
	// No lane review is fabricated: the merge remains in full delta inventory and the adopter is conflicted out of review.
	if len(v.Lanes) != 0 || !v.mergedBy(r.Adopter.Session) || !v.mergedBy(r.OriginalMerger) {
		t.Fatal(v)
	}
	for _, fault := range []string{"missing published head", "published before merge", "tampered", "duplicate", "trailer"} {
		t.Run(fault, func(t *testing.T) {
			w := append([]integrationCommit(nil), walk...)
			bindings := []LegacyBinding{binding}
			st := *store
			st.comments = map[int64]CommitComment{10: {CommitID: r.CandidateSHA, Body: body}}
			switch fault {
			case "missing published head":
				w = w[1:]
			case "published before merge":
				w[0], w[1] = w[1], w[0]
			case "tampered":
				st.comments[10] = CommitComment{CommitID: r.CandidateSHA, Body: body + " "}
			case "duplicate":
				bindings = append(bindings, binding)
			case "trailer":
				w[1].Message = "Wb-Lane: foreign"
			}
			verdict := provenance{Unrecorded: []string{r.CandidateSHA}}
			if err := verifyLegacyProvenance(context.Background(), &st, r.Repository, r.Integration, r.To, w, &verdict, bindings); err == nil {
				t.Fatal("accepted", fault)
			}
		})
	}
}
func preparedTestReceipt() (WorktreeMergeReceipt, LegacyAdoptOptions, ReviewerIdentity) {
	actor := ReviewerIdentity{Model: "gpt-6.1-sol", Harness: "codex", Session: "owner"}
	r := preparedTestRecord()
	path := "/projects/.wb/reports/worktree-merge/task.json"
	receipt := WorktreeMergeReceipt{SchemaVersion: 1, ID: "task", Lane: worktreeMergeLaneID("acme/app", "main"), Repository: "acme/app", Target: "main", Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePrepared, ReceiptPath: path, TargetSHA: r.TargetSHA, Candidate: WorktreeMergeCandidate{Task: "task", Branch: "wb/integration/main/task", Worktree: "/projects/.worktrees/task/acme/app", SHA: r.CandidateSHA}, Sources: []WorktreeMergeSource{{SHA: r.Sources[0], Branch: "feature/source", Merged: true}}, Validation: quality.VerificationReport{Revision: r.CandidateSHA, WorkspaceClean: true, Status: quality.StatusSkipped, Results: []quality.VerificationEntry{{Status: quality.StatusSkipped, Detail: "strict required checks"}}}, ValidationDeferral: &WorktreeMergeValidationDeferral{Route: r.Prepared.Deferral.Route, CandidateSHA: r.CandidateSHA, Reason: "strict required checks", RecordedAt: r.Prepared.Deferral.RecordedAt}, LaneOwner: &landinglane.Record{Owner: landinglane.Owner{WBSessionID: actor.Session}}}
	o := LegacyAdoptOptions{PromoteOptions: PromoteOptions{Repository: receipt.Repository, Integration: receipt.Candidate.Branch, To: receipt.Target, Worktree: receipt.Candidate.Worktree}, Prepared: true, Receipt: path}
	return receipt, o, actor
}
func TestPreparedReceiptRejectsCompletedDirtyForeignAndUnboundEvidence(t *testing.T) {
	t.Parallel()
	r, o, actor := preparedTestReceipt()
	if err := validatePreparedReceipt(r, r.ReceiptPath, o, actor); err != nil {
		t.Fatal(err)
	}
	actor.Session = "new-current-session"
	if err := validatePreparedReceipt(r, r.ReceiptPath, o, actor); err != nil {
		t.Fatal("historical session blocked a current adopter", err)
	}
	withRoute := r
	withRoute.Route.Route = WorktreeMergeRoutePullRequest
	if err := validatePreparedReceipt(withRoute, r.ReceiptPath, o, actor); err != nil {
		t.Fatal("explicit PR route refused", err)
	}
	for _, fault := range []string{"status", "phase", "missing historical owner", "invalid historical owner", "dirty", "missing check", "passed check", "source", "candidate", "repository", "target", "worktree", "deferral", "reason", "rebase", "direct route", "unknown route"} {
		t.Run(fault, func(t *testing.T) {
			r, o, a := preparedTestReceipt()
			switch fault {
			case "status":
				r.Status = WorktreeMergeComplete
			case "phase":
				r.Phase = WorktreeMergePhaseLand
			case "missing historical owner":
				r.LaneOwner.Owner.WBSessionID = ""
			case "invalid historical owner":
				r.LaneOwner.Owner.WBSessionID = "private/path"
			case "dirty":
				r.Validation.WorkspaceClean = false
			case "missing check":
				r.Validation.Results = nil
			case "passed check":
				r.Validation.Status = quality.StatusPassed
			case "source":
				r.Sources[0].Merged = false
			case "candidate":
				r.Candidate.SHA = "--bad"
			case "repository":
				o.Repository = "foreign/app"
			case "target":
				o.To = "other"
			case "worktree":
				o.Worktree = "/foreign"
			case "deferral":
				r.ValidationDeferral = nil
			case "reason":
				r.Validation.Results[0].Detail = "unrelated"
			case "rebase":
				r.RebatchOf = "other"
			case "direct route":
				r.Route.Route = WorktreeMergeRouteDirect
			case "unknown route":
				r.Route.Route = "unknown"
			}
			if validatePreparedReceipt(r, r.ReceiptPath, o, a) == nil {
				t.Fatal("accepted", fault)
			}
		})
	}
	if !containsExactLine("a\nb\nc", "b") || containsExactLine("abc", "b") {
		t.Fatal("ancestry requires exact commit identity")
	}
}

func TestPreparedAdoptionRejectsAbsentRootAndReceiptBeforeProviderReads(t *testing.T) {
	t.Parallel()
	for _, o := range []LegacyAdoptOptions{{Prepared: true}, {Prepared: true, ProjectsRoot: t.TempDir()}} {
		if _, err := AdoptLegacyIntegration(t.Context(), o); err == nil {
			t.Fatal("unbound input accepted")
		}
	}
}

func TestPreparedArchiveAdapterRetainsTypedNoProseRecord(t *testing.T) {
	t.Parallel()
	r := preparedTestRecord()
	body := preparedRecordPrefix + legacyJSON(r)
	url := "https://github.com/acme/app/commit/" + r.CandidateSHA + "#commitcomment-91"
	provider := []byte(legacyJSON([]map[string]string{{"html_url": url, "commit_id": r.CandidateSHA, "body": body}}))
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
		return githubobserver.Response{Body: provider}, nil
	}})
	adapter := githubIntegration{gh: func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) != 6 || args[0] != "api" || args[1] != "-X" || args[2] != "POST" || args[3] != "repos/acme/app/commits/"+r.CandidateSHA+"/comments" || args[4] != "-f" || args[5] != "body="+body {
			t.Fatal("unexpected write envelope", args)
		}
		return []byte(legacyJSON(map[string]string{"html_url": url, "commit_id": r.CandidateSHA, "body": body})), nil
	}}
	bindings, err := adapter.LegacyComments(ctx, r.Repository, r.CandidateSHA)
	if err != nil || len(bindings) != 1 || bindings[0].Digest != legacyHash([]byte(body)) {
		t.Fatal(bindings, err)
	}
	binding, err := adapter.CreateLegacyComment(ctx, r.Repository, r.CandidateSHA, body)
	if err != nil || binding != bindings[0] {
		t.Fatal(binding, err)
	}
}
