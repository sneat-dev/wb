package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuarantineValidationRejectsDuplicateAndNormalizesRequests(t *testing.T) {
	sha := strings.Repeat("a", 40)
	requests, err := validateQuarantineRequests([]BranchQuarantineRequest{
		{Repository: " zeta/app ", Ref: " feature/b ", SHA: " " + sha + " ", Reason: " old "},
		{Repository: " acme/app ", Ref: " feature/a ", SHA: sha, Reason: " done "},
	})
	if err != nil || len(requests) != 2 || requests[0].Repository != "acme/app" || requests[0].Ref != "feature/a" || requests[1].SHA != sha || requests[1].Reason != "old" {
		t.Fatalf("normalized requests = (%#v, %v)", requests, err)
	}
	_, err = validateQuarantineRequests([]BranchQuarantineRequest{
		{Repository: "acme/app", Ref: "feature/a", Reason: "one"},
		{Repository: " acme/app ", Ref: " feature/a ", Reason: "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate request error = %v", err)
	}
	for _, tc := range []struct {
		name    string
		request BranchQuarantineRequest
		want    string
	}{
		{"repository", BranchQuarantineRequest{Repository: "acme", Ref: "feature/a", Reason: "old"}, "repository"},
		{"ref", BranchQuarantineRequest{Repository: "acme/app", Ref: "bad ref", Reason: "old"}, "ref"},
		{"sha", BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/a", SHA: "bad", Reason: "old"}, "SHA"},
		{"reason", BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/a"}, "reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateQuarantineRequests([]BranchQuarantineRequest{tc.request})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestQuarantineRepositoryDiscoveryAndDigestFailures(t *testing.T) {
	fixture := newGitFixture(t)
	paths, err := quarantineRepositoryPaths(fixture.projectsRoot, []BranchQuarantineRequest{{Repository: "acme/app"}})
	if err != nil || paths["acme/app"] != fixture.canonical {
		t.Fatalf("discovered path = (%#v, %v)", paths, err)
	}
	if _, err := quarantineRepositoryPaths(fixture.projectsRoot, []BranchQuarantineRequest{{Repository: "other/missing"}}); err == nil || !strings.Contains(err.Error(), "not discovered") {
		t.Fatalf("missing repository error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := QuarantineManifestDigest(missing); !os.IsNotExist(err) {
		t.Fatalf("missing digest input error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := QuarantineManifestDigest(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("digest a = (%q, %v)", first, err)
	}
	if err := os.WriteFile(path, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := QuarantineManifestDigest(path)
	if err != nil || first == second {
		t.Fatalf("changed content digest = (%q, %v), first %q", second, err, first)
	}
}

func TestQuarantinePlanRejectsMovedProtectedAndCheckedOutRefs(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	missing := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "missing", Reason: "old"}, now)
	if missing.Outcome != "refused" || !strings.Contains(missing.Error, "source ref unavailable") {
		t.Fatalf("missing ref plan = %#v", missing)
	}
	mainSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	moved := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "main", SHA: strings.Repeat("a", 40), Reason: "old"}, now)
	if moved.Outcome != "refused" || !strings.Contains(moved.Error, "source moved") {
		t.Fatalf("stale SHA plan = %#v", moved)
	}
	protected := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "main", SHA: mainSHA, Reason: "old"}, now)
	if protected.Outcome != "refused" || !strings.Contains(protected.Error, "protected") {
		t.Fatalf("protected plan = %#v", protected)
	}
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/in-use")
	checked := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/in-use", Reason: "old"}, now)
	if checked.Outcome != "refused" || !strings.Contains(checked.Error, "protected") {
		t.Fatalf("current branch plan = %#v", checked)
	}
}

func TestQuarantineApplyRejectsChangesBeforeCAS(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	gitTest(t, fixture.canonical, "branch", "feature/old")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	for _, tc := range []struct{ name, ref, sha, want string }{
		{"missing", "feature/missing", head, "source disappeared"},
		{"moved", "feature/old", strings.Repeat("a", 40), "source moved"},
		{"protected", "main", head, "protected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := BranchQuarantineResult{BranchQuarantineRequest: BranchQuarantineRequest{Repository: "acme/app", Ref: tc.ref, SHA: tc.sha}, Destination: "retired/test", Outcome: "planned"}
			applyBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, &result)
			if result.Outcome != "failed" || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("apply result = %#v, want %q", result, tc.want)
			}
		})
	}
}
