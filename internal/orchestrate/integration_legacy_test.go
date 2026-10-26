package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func legacyTestRecord() LegacyProvenance {
	record := LegacyProvenance{Version: 1, Repository: "acme/app", Integration: "integration", To: "main", ReceiptID: "old-wb", ReceiptDigest: legacyHash([]byte("receipt")), TargetSHA: strings.Repeat("a", 40), CandidateSHA: strings.Repeat("c", 40), Sources: []string{strings.Repeat("b", 40)}, Merges: []LegacyMerge{{strings.Repeat("c", 40), strings.Repeat("a", 40), strings.Repeat("b", 40)}}, ValidationDigest: legacyHash([]byte("validation")), WB: "0.194.1@exact", Adopter: ReviewerIdentity{Model: "gpt-6.1-sol", Harness: "codex", Session: "adopter"}, OriginalMerger: "old-merger"}
	record.Validation = WorktreeMergeValidationIdentity{CandidateSHA: record.CandidateSHA, TargetSHA: record.TargetSHA, SourceSHAs: record.Sources, WBBuild: record.WB, QualityPolicySHA: strings.Repeat("d", 64), WBExecutableSHA: strings.Repeat("e", 64), Validators: map[string]string{"go": strings.Repeat("f", 64)}}
	record.ValidationDigest = legacyHash([]byte(legacyJSON(record.Validation)))
	record.PassedChecks = []string{"lint"}
	return record
}

type legacyCommentStore struct {
	comments map[int64]CommitComment
	reads    int
	err      error
}

func (s *legacyCommentStore) CommitComment(_ context.Context, _ string, id int64) (CommitComment, error) {
	s.reads++
	if s.err != nil {
		return CommitComment{}, s.err
	}
	c, ok := s.comments[id]
	if !ok {
		return c, fmt.Errorf("missing comment")
	}
	return c, nil
}
func legacyTestBinding(record LegacyProvenance) (LegacyBinding, string) {
	body := legacyRecordPrefix + legacyJSON(record)
	return LegacyBinding{"https://github.com/acme/app/commit/" + record.CandidateSHA + "#commitcomment-10", legacyHash([]byte(body))}, body
}
func legacyTestWalk(record LegacyProvenance) []integrationCommit {
	var walk []integrationCommit
	for _, m := range record.Merges {
		walk = append(walk, integrationCommit{SHA: m.SHA, Parents: []string{m.FirstParent, m.SecondParent}})
	}
	return walk
}

func TestLegacyReceiptProjectionRejectsInvalidIdentityAndChain(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"version", "repository", "integration", "same target", "receipt", "hash", "validation", "wb", "adopter", "base", "candidate", "empty", "length", "merge", "parent", "source", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := legacyTestRecord()
			switch kind {
			case "version":
				r.Version = 2
			case "repository":
				r.Repository = "../acme"
			case "integration":
				r.Integration = "-bad"
			case "same target":
				r.To = r.Integration
			case "receipt":
				r.ReceiptID = ""
			case "hash":
				r.ReceiptDigest = "sha256:bad"
			case "validation":
				r.ValidationDigest = "bad"
			case "wb":
				r.WB = ""
			case "adopter":
				r.Adopter.Session = ""
			case "base":
				r.TargetSHA = "bad"
			case "candidate":
				r.CandidateSHA = r.TargetSHA
			case "empty":
				r.Sources = nil
			case "length":
				r.Merges = nil
			case "merge":
				r.Merges[0].SHA = "bad"
			case "parent":
				r.Merges[0].FirstParent = r.CandidateSHA
			case "source":
				r.Merges[0].SecondParent = r.TargetSHA
			case "duplicate":
				r.Merges = append(r.Merges, r.Merges[0])
				r.Sources = append(r.Sources, r.Sources[0])
			}
			if validateLegacyRecord(r) == nil {
				t.Fatal(kind, "accepted")
			}
		})
	}
	if err := validateLegacyRecord(legacyTestRecord()); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyProvenancePortableVerificationKeepsFullUnreviewedDiff(t *testing.T) {
	t.Parallel()
	r := legacyTestRecord()
	binding, body := legacyTestBinding(r)
	store := &legacyCommentStore{comments: map[int64]CommitComment{10: {Body: body, CommitID: r.CandidateSHA}}}
	v := provenance{Unrecorded: []string{r.CandidateSHA, "unknown"}}
	if err := verifyLegacyProvenance(context.Background(), store, r.Repository, r.Integration, r.To, legacyTestWalk(r), &v, []LegacyBinding{binding}); err != nil {
		t.Fatal(err)
	}
	if len(v.Legacy) != 1 || len(v.Lanes) != 0 || len(v.Unrecorded) != 1 || v.Unrecorded[0] != "unknown" || !v.mergedBy("adopter") || !v.mergedBy("old-merger") {
		t.Fatal(v)
	}
	digest := legacyInventoryDigest(v.LegacyBindings)
	if !legacyReviewBinds("Reviewed-Legacy-Provenance: "+digest, digest) || legacyReviewBinds("Reviewed-Legacy-Provenance: wrong", digest) || legacyReviewBinds("no attestation", digest) || legacyReviewBinds("Reviewed-Legacy-Provenance: "+digest+"\nReviewed-Legacy-Provenance: "+digest, digest) {
		t.Fatal("inventory review binding")
	}
	inventory := legacyInventoryPrefix + legacyJSON(v.LegacyBindings) + " -->"
	got, err := parseLegacyInventory(inventory)
	if err != nil || legacyJSON(got) != legacyJSON(v.LegacyBindings) {
		t.Fatal(got, err)
	}
	for _, bad := range []string{inventory + inventory, legacyInventoryPrefix + "[", legacyInventoryPrefix + "bad -->", legacyInventoryPrefix + "[] -->", legacyInventoryPrefix + " [ ] -->"} {
		if _, err := parseLegacyInventory(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	if got, err := parseLegacyInventory("ordinary PR"); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

func TestLegacyProvenanceRejectsForeignChangedMissingAndDuplicateBindings(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"url", "foreign repo", "short sha", "bad id", "bad digest", "outage", "deleted", "changed", "foreign commit", "wrong record repo", "wrong integration", "wrong target", "wrong candidate", "missing graph", "octopus", "first parent", "second parent", "duplicate", "not record", "invalid json", "noncanonical", "invalid record", "recorded lane"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := legacyTestRecord()
			binding, body := legacyTestBinding(r)
			store := &legacyCommentStore{comments: map[int64]CommitComment{10: {Body: body, CommitID: r.CandidateSHA}}}
			walk := legacyTestWalk(r)
			bindings := []LegacyBinding{binding}
			switch kind {
			case "url":
				bindings[0].URL = "file:///tmp/receipt"
			case "foreign repo":
				bindings[0].URL = strings.Replace(binding.URL, "acme/app", "evil/app", 1)
			case "short sha":
				bindings[0].URL = strings.Replace(binding.URL, r.CandidateSHA, r.CandidateSHA[:7], 1)
			case "bad id":
				bindings[0].URL = strings.Replace(binding.URL, "-10", "-99999999999999999999999999999999", 1)
			case "bad digest":
				bindings[0].Digest = "bad"
			case "outage":
				store.err = errors.New("outage")
			case "deleted":
				delete(store.comments, 10)
			case "changed":
				store.comments[10] = CommitComment{Body: body + "edit", CommitID: r.CandidateSHA}
			case "foreign commit":
				store.comments[10] = CommitComment{Body: body, CommitID: r.TargetSHA}
			case "wrong record repo":
				r.Repository = "other/app"
			case "wrong integration":
				r.Integration = "other"
			case "wrong target":
				r.To = "other"
			case "wrong candidate":
				r.CandidateSHA = r.TargetSHA
			case "missing graph":
				walk = nil
			case "octopus":
				walk[0].Parents = append(walk[0].Parents, r.CandidateSHA)
			case "first parent":
				walk[0].Parents[0] = r.CandidateSHA
			case "second parent":
				walk[0].Parents[1] = r.CandidateSHA
			case "duplicate":
				bindings = append(bindings, binding)
			case "not record":
				body = "ordinary comment"
			case "invalid json":
				body = legacyRecordPrefix + "{"
			case "noncanonical":
				body = legacyRecordPrefix + " " + legacyJSON(r)
			case "recorded lane":
				walk[0].Message = "Wb-Lane: original@" + r.Sources[0]
			case "invalid record":
				r.Version = 2
			}
			if strings.HasPrefix(kind, "wrong ") || kind == "invalid record" {
				body = legacyRecordPrefix + legacyJSON(r)
			}
			if strings.HasPrefix(kind, "wrong ") || kind == "not record" || kind == "invalid json" || kind == "noncanonical" || kind == "invalid record" {
				store.comments[10] = CommitComment{Body: body, CommitID: legacyTestRecord().CandidateSHA}
				bindings[0].Digest = legacyHash([]byte(body))
			}
			v := provenance{Unrecorded: []string{legacyTestRecord().CandidateSHA}}
			if err := verifyLegacyProvenance(context.Background(), store, "acme/app", "integration", "main", walk, &v, bindings); err == nil {
				t.Fatal("accepted", kind)
			}
			if (kind == "url" || kind == "foreign repo" || kind == "short sha" || kind == "bad id" || kind == "bad digest") && store.reads != 0 {
				t.Fatal("read foreign endpoint before validation")
			}
		})
	}
}

func TestLegacyPromotionLandingRequiresInventoryAndCurrentHeadReview(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"valid", "missing inventory review", "wrong inventory review", "foreign archive", "malformed inventory", "unknown merge", "adopter review", "old merger review", "changed head"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := legacyTestRecord()
			binding, body := legacyTestBinding(r)
			world := newPromoteWorld()
			world.review = "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + r.CandidateSHA + "\nReviewed-Legacy-Provenance: " + legacyInventoryDigest([]LegacyBinding{binding}) + "\n"
			view := promotionView()
			view.Head.SHA = r.CandidateSHA
			view.Head.Ref = r.Integration
			view.Body = promoteMarker + "\n" + legacyInventoryPrefix + legacyJSON([]LegacyBinding{binding}) + " -->"
			ports := world.ports()
			ports.contains = func(_ context.Context, _, ancestor, _ string) (bool, string) {
				return ancestor == "main" || ancestor == r.TargetSHA, ""
			}
			ports.review = &legacyCommentStore{comments: map[int64]CommitComment{10: {Body: body, CommitID: r.CandidateSHA}}}
			commits := []SourceCommit{{SHA: r.CandidateSHA, Parents: []string{r.TargetSHA, r.Sources[0]}}}
			switch kind {
			case "missing inventory review":
				world.review = strings.Split(world.review, "Reviewed-Legacy")[0]
			case "wrong inventory review":
				world.review = strings.Replace(world.review, legacyInventoryDigest([]LegacyBinding{binding}), "wrong", 1)
			case "foreign archive":
				ports.review = &legacyCommentStore{err: errors.New("404")}
			case "malformed inventory":
				view.Body = promoteMarker + legacyInventoryPrefix + "bad -->"
			case "unknown merge":
				view.Body = promoteMarker
			case "adopter review":
				world.review = strings.Replace(world.review, "delta-reviewer", "adopter", 1)
			case "old merger review":
				world.review = strings.Replace(world.review, "delta-reviewer", "old-merger", 1)
			case "changed head":
				world.review = strings.Replace(world.review, r.CandidateSHA, r.TargetSHA, 1)
			}
			options := PullRequestLandOptions{Repository: r.Repository, ApprovedBy: promoteApproval, promotePorts: ports}
			receipt, refusal, err := promotionLandingGate(context.Background(), &options, view, "7", commits)
			if kind == "valid" {
				if err != nil || refusal != nil || receipt == nil || len(receipt.Legacy) != 1 || !options.NoAutoMerge || !options.NoUpdateBranch {
					t.Fatal(receipt, refusal, err)
				}
			} else if err == nil && refusal == nil {
				t.Fatal("unreviewed legacy promotion accepted", kind)
			}
		})
	}
}

func TestLegacyArchiveAdapterUsesOnlyValidatedRepositoryCommitEndpoints(t *testing.T) {
	t.Parallel()
	r := legacyTestRecord()
	binding, body := legacyTestBinding(r)
	for _, kind := range []string{"list", "list invalid repo", "list invalid commit", "list outage", "list malformed", "list foreign url", "list foreign commit", "list ordinary", "list empty inventory", "list null page", "create", "create invalid endpoint", "create invalid body", "create foreign body", "create outage", "create malformed", "create foreign commit", "create changed body"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := 0
			repo, sha := r.Repository, r.CandidateSHA
			writeBody := body
			adapter := githubIntegration{gh: func(_ context.Context, args ...string) ([]byte, error) {
				calls++
				if kind == "list outage" || kind == "create outage" {
					return nil, errors.New("provider outage")
				}
				if kind == "list empty inventory" {
					return []byte("[]"), nil
				}
				if kind == "list null page" {
					return []byte("[null]"), nil
				}
				if kind == "list malformed" || kind == "create malformed" {
					return []byte("{"), nil
				}
				url, commit, text := binding.URL, sha, body
				switch kind {
				case "list foreign url":
					url = "file:///receipt"
				case "list foreign commit", "create foreign commit":
					commit = r.TargetSHA
				case "list ordinary":
					text = "ordinary comment"
				case "create changed body":
					text += "edit"
				}
				value := map[string]string{"html_url": url, "commit_id": commit, "body": text}
				if args[1] == "--paginate" {
					if !strings.Contains(strings.Join(args, " "), "--slurp") || !strings.Contains(args[len(args)-1], "per_page=100") {
						t.Fatal(args)
					}
					return []byte(legacyJSON([][]map[string]string{{value}, {}})), nil
				}
				if args[1] != "-X" || args[2] != "POST" || !strings.HasPrefix(args[3], "repos/acme/app/commits/") {
					t.Fatal(args)
				}
				return []byte(legacyJSON(value)), nil
			}}
			switch kind {
			case "list invalid repo", "create invalid endpoint":
				repo = "../evil"
			case "list invalid commit":
				sha = "bad"
			case "create invalid body":
				writeBody = "not record"
			case "create foreign body":
				altered := r
				altered.Repository = "other/app"
				writeBody = legacyRecordPrefix + legacyJSON(altered)
			}
			if strings.HasPrefix(kind, "list") {
				got, err := adapter.LegacyComments(context.Background(), repo, sha)
				if kind == "list" {
					if err != nil || len(got) != 1 || got[0] != binding {
						t.Fatal(got, err)
					}
				} else if kind == "list ordinary" {
					if err != nil || len(got) != 0 {
						t.Fatal(got, err)
					}
				} else if err == nil {
					t.Fatal("accepted", kind)
				}
			} else {
				got, err := adapter.CreateLegacyComment(context.Background(), repo, sha, writeBody)
				if kind == "create" {
					if err != nil || got != binding {
						t.Fatal(got, err)
					}
				} else if err == nil {
					t.Fatal("accepted", kind)
				}
			}
			if (strings.Contains(kind, "invalid") || kind == "create foreign body") && calls != 0 {
				t.Fatal("read before endpoint/body validation", kind)
			}
		})
	}
}

func TestLegacyValidationProjectionRejectsMissingAndUnprintableEvidence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"validator name", "validator hash", "check name", "candidate end", "validation binding"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := legacyTestRecord()
			switch kind {
			case "validator name":
				r.Validation.Validators = map[string]string{"private path\n": strings.Repeat("a", 64)}
			case "validator hash":
				r.Validation.Validators = map[string]string{"go": "invalid"}
			case "check name":
				r.PassedChecks = []string{"private\ntext"}
			case "candidate end":
				r.Merges[0].SHA = strings.Repeat("f", 40)
			case "validation binding":
				r.Validation.TargetSHA = r.CandidateSHA
			}
			r.ValidationDigest = legacyHash([]byte(legacyJSON(r.Validation)))
			if validateLegacyRecord(r) == nil {
				t.Fatal("accepted", kind)
			}
		})
	}
	options := LegacyAdoptOptions{}
	if options.resolveLegacyArchive() == nil {
		t.Fatal("default archive is not configured")
	}
}

func TestLegacyLandingCanonicalizesTwoPortableReceiptBindings(t *testing.T) {
	t.Parallel()
	first := legacyTestRecord()
	second := legacyTestRecord()
	second.ReceiptID = "second"
	second.TargetSHA = first.CandidateSHA
	second.CandidateSHA = strings.Repeat("1", 40)
	second.Sources = []string{strings.Repeat("2", 40)}
	second.Merges = []LegacyMerge{{second.CandidateSHA, second.TargetSHA, second.Sources[0]}}
	second.Validation.TargetSHA = second.TargetSHA
	second.Validation.CandidateSHA = second.CandidateSHA
	second.Validation.SourceSHAs = second.Sources
	second.ValidationDigest = legacyHash([]byte(legacyJSON(second.Validation)))
	b1, body1 := legacyTestBinding(first)
	b2, body2 := legacyTestBinding(second)
	b2.URL = strings.Replace(b2.URL, "-10", "-11", 1)
	bindings := []LegacyBinding{b2, b1}
	canonical := []LegacyBinding{b2, b1} // second candidate starts1, so this is URL order.
	world := newPromoteWorld()
	world.review = "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + second.CandidateSHA + "\nReviewed-Legacy-Provenance: " + legacyInventoryDigest(canonical)
	view := promotionView()
	view.Head.SHA = second.CandidateSHA
	view.Head.Ref = first.Integration
	view.Body = promoteMarker + legacyInventoryPrefix + legacyJSON(bindings) + " -->"
	ports := world.ports()
	ports.contains = func(_ context.Context, _, ancestor, _ string) (bool, string) {
		return ancestor == "main" || ancestor == first.TargetSHA, ""
	}
	ports.review = &legacyCommentStore{comments: map[int64]CommitComment{10: {Body: body1, CommitID: first.CandidateSHA}, 11: {Body: body2, CommitID: second.CandidateSHA}}}
	options := PullRequestLandOptions{Repository: first.Repository, ApprovedBy: promoteApproval, promotePorts: ports}
	commits := []SourceCommit{{SHA: second.CandidateSHA, Parents: []string{second.TargetSHA, second.Sources[0]}}, {SHA: first.CandidateSHA, Parents: []string{first.TargetSHA, first.Sources[0]}}}
	receipt, refusal, err := promotionLandingGate(context.Background(), &options, view, "7", commits)
	if err != nil || refusal != nil || receipt == nil || len(receipt.Legacy) != 2 {
		t.Fatal(receipt, refusal, err)
	}
}
