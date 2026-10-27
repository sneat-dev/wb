//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type legacyArchiveFixture struct {
	*fakeIntegrationGitHub
	records              []LegacyBinding
	creates              int
	listErr, errorCreate error
	badResponse          bool
}

func (a *legacyArchiveFixture) LegacyComments(context.Context, string, string) ([]LegacyBinding, error) {
	return a.records, a.listErr
}
func (a *legacyArchiveFixture) CreateLegacyComment(_ context.Context, repo, sha, body string) (LegacyBinding, error) {
	a.creates++
	if a.errorCreate != nil {
		return LegacyBinding{}, a.errorCreate
	}
	binding := LegacyBinding{fmt.Sprintf("https://github.com/%s/commit/%s#commitcomment-90", repo, sha), legacyHash([]byte(body))}
	a.comments[90] = CommitComment{CommitID: sha, Body: body}
	a.records = append(a.records, binding)
	if a.badResponse {
		binding.URL = "https://foreign/"
	}
	return binding, nil
}

func oldMergeFixture(t *testing.T) (*integrationFixture, *fakePromoteGitHub, WorktreeMergeReceipt, LegacyAdoptOptions) {
	t.Helper()
	f := newIntegrationFixture(t)
	_, first := f.lane(f.base, "old.go", "package a\nconst OldDefect = 1\n", "reviewer")
	_, second := f.lane(f.base, "extra.go", "package a\n", "reviewer")
	for _, source := range []string{first, second} {
		igit(t, f.work, "fetch", "--quiet", "origin", source)
		igit(t, f.work, "merge", "--no-ff", "--quiet", source, "-m", "Legacy WB merge")
	}
	candidate := igit(t, f.work, "rev-parse", "HEAD")
	igit(t, f.work, "push", "--quiet", "origin", "HEAD:public-demo")
	receipt := WorktreeMergeReceipt{SchemaVersion: 1, ID: "old-complete", Repository: "acme/app", Target: "public-demo", Phase: WorktreeMergePhaseLand, Status: WorktreeMergeComplete, Route: WorktreeMergeRouteDecision{Route: WorktreeMergeRouteDirect}, TargetSHA: f.base, PreviousTargetSHA: f.base, Candidate: WorktreeMergeCandidate{Task: "old-complete", Branch: "wb/integration/public-demo/old", SHA: candidate}, LandingSHA: candidate, Sources: []WorktreeMergeSource{{SHA: first, Merged: true}, {SHA: second, Merged: true}}, Validation: passedChecks(), ValidationIdentity: &WorktreeMergeValidationIdentity{CandidateSHA: candidate, TargetSHA: f.base, SourceSHAs: []string{first, second}, WBBuild: "old-wb", QualityPolicySHA: strings.Repeat("d", 64), WBExecutableSHA: strings.Repeat("e", 64)}}
	gh := &fakePromoteGitHub{fakeIntegrationGitHub: f.gh}
	path := filepath.Join(t.TempDir(), "receipt.json")
	raw, _ := json.Marshal(receipt)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	archive := &legacyArchiveFixture{fakeIntegrationGitHub: f.gh}
	options := LegacyAdoptOptions{PromoteOptions: f.promoteOptions(gh), Receipt: path, archive: archive, actor: ReviewerIdentity{Model: "gpt-6.1-sol", Harness: "codex", Session: "adopter"}}
	return f, gh, receipt, options
}

func TestE2ELegacyAdoptionPreservesHistoryAndFullDeltaThroughFinalFix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, gh, _, options := oldMergeFixture(t)
	head := igit(t, f.work, "rev-parse", "HEAD")
	before := mustPromote(t, LandFindings)(DeltaBatch(ctx, options.PromoteOptions))
	if len(before.Unrecorded) != 2 {
		t.Fatal(before)
	}
	dry, err := AdoptLegacyIntegration(ctx, options)
	if err != nil || dry.Applied || dry.Binding != nil || len(dry.Record.Merges) != 2 {
		t.Fatal(dry, err)
	}
	archive := options.archive.(*legacyArchiveFixture)
	if archive.creates != 0 {
		t.Fatal("dry run wrote")
	}
	options.Apply = true
	adopted, err := AdoptLegacyIntegration(ctx, options)
	if err != nil || !adopted.Applied {
		t.Fatal(adopted, err)
	}
	again, err := AdoptLegacyIntegration(ctx, options)
	if err != nil || again.Binding.URL != adopted.Binding.URL || archive.creates != 1 {
		t.Fatal(again, err, archive.creates)
	}
	nextSession := options
	nextSession.actor = ReviewerIdentity{Model: "gpt-6.1-sol", Harness: "codex", Session: "different-adopter"}
	retried, err := AdoptLegacyIntegration(ctx, nextSession)
	if err != nil || retried.Binding.URL != adopted.Binding.URL || retried.Record.Adopter != adopted.Record.Adopter || archive.creates != 1 {
		t.Fatal("different session duplicated or rewrote provenance", retried, err)
	}
	if igit(t, f.work, "rev-parse", "HEAD") != head {
		t.Fatal("adoption rewrote history")
	}
	// A later source fix is reviewed WITH the historic defect, never a fictional
	// zero-findings review of the old ancestor.
	f.advance("public-demo", "old.go", "package a\nconst Fixed = 2\n")
	promotion := options.PromoteOptions
	promotion.LegacyRecords = []string{adopted.Binding.URL + "=" + adopted.Binding.Digest}
	bad := promotion
	bad.LegacyRecords = []string{"not a binding"}
	if _, err := DeltaBatch(ctx, bad); err == nil {
		t.Fatal("delta accepted malformed adoption")
	}
	if _, err := PromoteIntegration(ctx, bad); err == nil {
		t.Fatal("promotion accepted malformed adoption")
	}
	bad = promotion
	bad.LegacyRecords = []string{adopted.Binding.URL + "=" + legacyHash([]byte("edited"))}
	if _, err := PromoteIntegration(ctx, bad); err == nil {
		t.Fatal("promotion accepted changed archive")
	}
	delta := mustPromote(t, LandSuccess)(DeltaBatch(ctx, promotion))
	if !strings.Contains(PromoteText(delta), delta.LegacyInventoryDigest) {
		t.Fatal("reviewer cannot see inventory digest")
	}
	if len(delta.Legacy) != 1 || len(delta.Lanes) != 0 || len(delta.Delta) != 3 || delta.LegacyInventoryDigest == "" {
		t.Fatal(delta)
	}
	full := ""
	for _, part := range delta.Delta {
		full += part.Diff
	}
	if !strings.Contains(full, "OldDefect") || !strings.Contains(full, "Fixed") {
		t.Fatal("legacy body or later fix hidden", full)
	}
	promoted := mustPromote(t, LandSuccess)(PromoteIntegration(ctx, promotion))
	if promoted.PullRequest == "" || !strings.Contains(gh.body, "not historical review approval") {
		t.Fatal(promoted, gh.body)
	}
	// Portable rerun knows only the remote typed record and generated inventory.
	portable := f.promoteOptions(gh)
	if got := mustPromote(t, LandSuccess)(DeltaBatch(ctx, portable)); got.LegacyInventoryDigest != delta.LegacyInventoryDigest {
		t.Fatal(got)
	}
	gh.comments[90] = CommitComment{CommitID: head, Body: gh.comments[90].Body + "edited"}
	if _, err := DeltaBatch(ctx, portable); err == nil {
		t.Fatal("edited archive accepted")
	}
}

func TestE2ELegacyAdoptionRejectsCorruptAndUnpublishedReceipt(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"json", "schema", "repository", "target", "status", "phase", "route", "base", "candidate", "validation", "skipped", "source identity", "source flag", "duplicate source", "unpublished"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			_, _, original, options := oldMergeFixture(t)
			r := original
			r.Sources = append([]WorktreeMergeSource(nil), original.Sources...)
			identity := *original.ValidationIdentity
			r.ValidationIdentity = &identity
			switch kind {
			case "schema":
				r.SchemaVersion = 2
			case "repository":
				r.Repository = "other/app"
			case "target":
				r.Target = "other"
			case "status":
				r.Status = WorktreeMergePrepared
			case "phase":
				r.Phase = WorktreeMergePhasePrepare
			case "route":
				r.Route.Route = WorktreeMergeRoutePullRequest
			case "base":
				r.PreviousTargetSHA = strings.Repeat("f", 40)
			case "candidate":
				r.Candidate.SHA = r.TargetSHA
			case "validation":
				r.ValidationIdentity = nil
			case "skipped":
				r.Validation.Results = nil
			case "source identity":
				r.ValidationIdentity.SourceSHAs = nil
			case "source flag":
				r.Sources[0].Merged = false
			case "duplicate source":
				r.Sources[1] = r.Sources[0]
				r.ValidationIdentity.SourceSHAs = []string{r.Sources[0].SHA, r.Sources[0].SHA}
			case "unpublished":
				r.Candidate.SHA = strings.Repeat("f", 40)
				r.LandingSHA = r.Candidate.SHA
				r.ValidationIdentity.CandidateSHA = r.Candidate.SHA
			}
			raw, _ := json.Marshal(r)
			if kind == "json" {
				raw = []byte("{")
			}
			path := filepath.Join(t.TempDir(), "receipt.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			bad := options
			bad.Receipt = path
			bad.Apply = true
			if _, err := AdoptLegacyIntegration(context.Background(), bad); err == nil {
				t.Fatal("accepted", kind)
			}
			if options.archive.(*legacyArchiveFixture).creates != 0 {
				t.Fatal("invalid receipt published")
			}
		})
	}
	_, _, _, options := oldMergeFixture(t)
	missing := options
	missing.Receipt = filepath.Join(t.TempDir(), "missing")
	if _, err := AdoptLegacyIntegration(context.Background(), missing); err == nil {
		t.Fatal("missing receipt")
	}
	foreign := options
	foreign.Repository = "../foreign"
	if _, err := AdoptLegacyIntegration(context.Background(), foreign); err == nil {
		t.Fatal("invalid repository")
	}
	options.Apply = true
	archive := options.archive.(*legacyArchiveFixture)
	archive.listErr = errors.New("outage")
	if _, err := AdoptLegacyIntegration(context.Background(), options); err == nil {
		t.Fatal("list outage")
	}
	archive.listErr = nil
	archive.errorCreate = errors.New("unknown POST")
	if _, err := AdoptLegacyIntegration(context.Background(), options); err == nil {
		t.Fatal("unknown write")
	}
	archive.errorCreate = nil
	archive.badResponse = true
	if _, err := AdoptLegacyIntegration(context.Background(), options); err == nil {
		t.Fatal("bad response")
	}
	archive.badResponse = false
	if _, err := AdoptLegacyIntegration(context.Background(), options); err != nil {
		t.Fatal("cannot recover committed unknown archive", err)
	}
}

func TestE2ELegacyInventoryInputsNeverReplaceStoredBindingSilently(t *testing.T) {
	t.Parallel()
	r := legacyTestRecord()
	binding, _ := legacyTestBinding(r)
	for _, kind := range []string{"new", "same", "conflict", "bad shape", "bad URL", "bad stored", "outage"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			gh := &fakePromoteGitHub{fakeIntegrationGitHub: &fakeIntegrationGitHub{comments: map[int64]CommitComment{}}}
			o := PromoteOptions{Repository: r.Repository, Integration: r.Integration, To: r.To, GitHub: gh, LegacyRecords: []string{binding.URL + "=" + binding.Digest}}
			switch kind {
			case "same", "conflict":
				gh.body = legacyInventoryPrefix + legacyJSON([]LegacyBinding{binding}) + " -->"
				if kind == "conflict" {
					o.LegacyRecords = []string{binding.URL + "=" + legacyHash([]byte("changed"))}
				}
			case "bad shape":
				o.LegacyRecords = []string{"wrong"}
			case "bad URL":
				o.LegacyRecords = []string{"file:///bad=" + binding.Digest}
			case "bad stored":
				gh.body = legacyInventoryPrefix + "bad -->"
			case "outage":
				gh.failAt = 1
			}
			got, err := legacyRecordBindings(context.Background(), o)
			if kind == "new" || kind == "same" {
				if err != nil || len(got) != 1 || got[0] != binding {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("accepted", kind)
			}
		})
	}
}

type legacyProofFaultRunner struct {
	runner.Runner
	fault   string
	parents string
}

func (r legacyProofFaultRunner) Run(ctx context.Context, dir, name string, args ...string) (runner.Result, error) {
	if len(args) > 0 {
		if args[0] == "merge-base" && r.fault == "ancestry error" {
			return runner.Result{ExitCode: 2}, errors.New("object store unavailable")
		}
		if args[0] == "merge-base" && r.fault == "unpublished" {
			return runner.Result{ExitCode: 1}, errors.New("not ancestor")
		}
		if args[0] == "rev-list" && r.fault == "list error" {
			return runner.Result{ExitCode: 2}, errors.New("cannot list chain")
		}
		if args[0] == "show" {
			switch r.fault {
			case "parent read error":
				return runner.Result{ExitCode: 2}, errors.New("cannot read parents")
			case "hidden fix":
				return runner.Result{Stdout: r.parents + "\x00fix"}, nil
			case "recorded lane":
				return runner.Result{Stdout: r.parents + " " + strings.Repeat("f", 40) + "\x00Wb-Lane: lane@" + strings.Repeat("f", 40)}, nil
			}
		}
	}
	return r.Runner.Run(ctx, dir, name, args...)
}

func TestE2ELegacyAdoptionFailsClosedAtEveryGraphProofBoundary(t *testing.T) {
	t.Parallel()
	_, gh, original, base := oldMergeFixture(t)
	// Each case uses read-only graph operations on one isolated fixture and has
	// an independent receipt file. No real provider calls or global seams.
	for _, kind := range []string{"default actor", "open error", "checkout refusal", "invalid hash", "ancestry error", "unpublished", "list error", "parent read error", "hidden fix", "recorded lane", "invalid validation", "original merger"} {
		r := original
		identity := *r.ValidationIdentity
		r.ValidationIdentity = &identity
		options := base
		switch kind {
		case "default actor":
			options.actor = ReviewerIdentity{}
			r.SchemaVersion = 0
		case "open error":
			gh.failAt = gh.calls + 1
		case "checkout refusal":
			options.Worktree = ""
		case "invalid hash":
			r.TargetSHA = "invalid"
			r.PreviousTargetSHA = r.TargetSHA
			r.ValidationIdentity.TargetSHA = r.TargetSHA
		case "invalid validation":
			r.ValidationIdentity.WBExecutableSHA = "invalid"
		case "original merger":
			r.LaneOwner = &landinglane.Record{Owner: landinglane.Owner{WBSessionID: "original-wb-merger"}}
		default:
			options.run = legacyProofFaultRunner{Runner: base.run, fault: kind, parents: r.TargetSHA}
		}
		path := filepath.Join(t.TempDir(), "receipt.json")
		raw, _ := json.Marshal(r)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		options.Receipt = path
		got, err := AdoptLegacyIntegration(context.Background(), options)
		if kind == "original merger" {
			if err != nil || got.Record.OriginalMerger != "original-wb-merger" {
				t.Fatal(kind, got, err)
			}
		} else if err == nil {
			t.Fatal("missing graph barrier", kind)
		}
		gh.failAt = 0
	}
}

func TestE2ELegacyAdoptionRejectsChangedExistingArchiveBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	_, _, _, options := oldMergeFixture(t)
	options.Apply = true
	adopted, err := AdoptLegacyIntegration(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	archive := options.archive.(*legacyArchiveFixture)
	original := archive.comments[90]
	for _, kind := range []string{"foreign URL", "missing comment", "changed comment", "invalid record"} {
		archive.records = []LegacyBinding{*adopted.Binding}
		archive.comments[90] = original
		switch kind {
		case "foreign URL":
			archive.records[0].URL = "file:///private/receipt"
		case "missing comment":
			delete(archive.comments, 90)
		case "changed comment":
			archive.comments[90] = CommitComment{CommitID: original.CommitID, Body: original.Body + "changed"}
		case "invalid record":
			archive.comments[90] = CommitComment{CommitID: original.CommitID, Body: "ordinary comment"}
			archive.records[0].Digest = legacyHash([]byte("ordinary comment"))
		}
		if _, err := AdoptLegacyIntegration(context.Background(), options); err == nil {
			t.Fatal("accepted changed existing record", kind)
		}
		if archive.creates != 1 {
			t.Fatal("mutated archive after failed proof", kind)
		}
	}
}

func TestE2ELegacyAdoptionRejectsUnsafeIdentitiesBeforeAnyPublication(t *testing.T) {
	t.Parallel()
	_, _, original, base := oldMergeFixture(t)
	archive := base.archive.(*legacyArchiveFixture)
	for _, field := range []string{"model", "harness", "session", "original", "partial adopter"} {
		r := original
		options := base
		options.Apply = true
		switch field {
		case "model":
			options.actor.Model = "/Users/private/model"
		case "harness":
			options.actor.Harness = "native\nprivate"
		case "session":
			options.actor.Session = strings.Repeat("a", 129)
		case "original":
			r.LaneOwner = &landinglane.Record{Owner: landinglane.Owner{WBSessionID: "/Users/private/receipt\nnot-session"}}
		case "partial adopter":
			options.actor = ReviewerIdentity{Model: "gpt-6.1-sol"}
		}
		raw, _ := json.Marshal(r)
		path := filepath.Join(t.TempDir(), "receipt.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		options.Receipt = path
		options.checkout = func(string) (string, error) { t.Fatal("identity rejected too late", field); return "", nil }
		if _, err := AdoptLegacyIntegration(context.Background(), options); err == nil {
			t.Fatal("unsafe identity accepted", field)
		}
		if archive.creates != 0 {
			t.Fatal("unsafe identity was published", field)
		}
	}
}
