package worktreeproof

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
)

func TestObjectIDsAndParsers(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)
	for _, value := range []string{sha, sha256} {
		if !IsGitObjectID(value) || !IsGitRevisionID(value) || !HasOnlyLowerHexCharacters(value) {
			t.Fatalf("valid SHA %q rejected", value)
		}
	}
	for _, value := range []string{"", "ABC", strings.Repeat("z", 40), strings.Repeat("a", 39), strings.Repeat("a", 65)} {
		if IsGitObjectID(value) {
			t.Fatalf("invalid object ID %q accepted", value)
		}
	}
	if IsGitRevisionID("abc") || IsGitRevisionID(strings.Repeat("a", 65)) || IsGitRevisionID("Abcd") || !IsGitRevisionID("abcd") {
		t.Fatal("revision bounds or alphabet")
	}
	if branch, err := ParseRemoteDefaultBranch("noise\nref: refs/heads/main HEAD\n", func(v string) bool { return v == "main" }); err != nil || branch != "main" {
		t.Fatalf("branch %q %v", branch, err)
	}
	for _, output := range []string{"ref: refs/tags/v1 HEAD", "ref: refs/heads/main OTHER", "ref: refs/heads/main", "noise"} {
		if _, err := ParseRemoteDefaultBranch(output, func(string) bool { return true }); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
	if _, err := ParseRemoteDefaultBranch("ref: refs/heads/bad HEAD", func(string) bool { return false }); err == nil {
		t.Fatal("accepted invalid branch")
	}
	if got, err := ParseCommitFirstParent("rev", sha); err != nil || got != "" {
		t.Fatalf("root parent %q %v", got, err)
	}
	if got, err := ParseCommitFirstParent("rev", sha+" "+sha256); err != nil || got != sha256 {
		t.Fatalf("first parent %q %v", got, err)
	}
	if _, err := ParseCommitFirstParent("rev", sha+" bad"); err == nil {
		t.Fatal("invalid parent accepted")
	}
	if got, err := ParseCommitTree("rev", sha); err != nil || got != sha {
		t.Fatalf("tree %q %v", got, err)
	}
	if _, err := ParseCommitTree("rev", "bad"); err == nil {
		t.Fatal("invalid tree accepted")
	}
}

func TestCommitQueries(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	parent := strings.Repeat("b", 40)
	calls := 0
	query := func(_ context.Context, repo string, args ...string) (string, error) {
		calls++
		if repo != "repo" {
			t.Fatalf("repo %q", repo)
		}
		switch args[0] {
		case "rev-list":
			if strings.Join(args, " ") != "rev-list --parents -n 1 --end-of-options rev" {
				t.Fatalf("parents args %q", args)
			}
			return sha + " " + parent, nil
		case "rev-parse":
			if args[1] != "rev^{tree}" {
				t.Fatalf("tree args %q", args)
			}
			return parent, nil
		}
		t.Fatalf("unexpected args %q", args)
		return "", nil
	}
	if got, err := CommitFirstParent(context.Background(), "repo", "rev", query); err != nil || got != parent {
		t.Fatalf("parent %q %v", got, err)
	}
	if got, err := CommitTree(context.Background(), "repo", "rev", query); err != nil || got != parent {
		t.Fatalf("tree %q %v", got, err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
	fail := func(context.Context, string, ...string) (string, error) { return "", errors.New("offline") }
	if _, err := CommitFirstParent(context.Background(), "repo", "rev", fail); err == nil || !strings.Contains(err.Error(), "resolve parents") {
		t.Fatalf("parent failure %v", err)
	}
	if _, err := CommitTree(context.Background(), "repo", "rev", fail); err == nil || !strings.Contains(err.Error(), "resolve tree") {
		t.Fatalf("tree failure %v", err)
	}
}

func TestAncestryMemoAndFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	called := 0
	run := func(context.Context, string, string, string) (int, error) { called++; return 0, nil }
	if yes, err := IsAncestor(ctx, "r", "a", "a", run, nil, nil); !yes || err != nil || called != 0 {
		t.Fatal("self ancestry did Git work")
	}
	stored := map[string]bool{}
	lookup := func(key string) (bool, bool) { v, ok := stored[key]; return v, ok }
	store := func(key string, v bool) { stored[key] = v }
	if yes, err := IsAncestor(ctx, "r", "a", "b", run, lookup, store); !yes || err != nil {
		t.Fatalf("ancestry %v %v", yes, err)
	}
	if yes, err := IsAncestor(ctx, "r", "a", "b", run, lookup, store); !yes || err != nil || called != 1 {
		t.Fatal("positive memo miss")
	}
	run = func(context.Context, string, string, string) (int, error) {
		called++
		return 1, errors.New("not ancestor")
	}
	if yes, err := IsAncestor(ctx, "r", "b", "a", run, lookup, store); yes || err != nil {
		t.Fatalf("negative verdict %v %v", yes, err)
	}
	if yes, err := IsAncestor(ctx, "r", "b", "a", run, lookup, store); yes || err != nil || called != 2 {
		t.Fatal("negative memo miss")
	}
	run = func(context.Context, string, string, string) (int, error) { return 2, errors.New("fatal") }
	if _, err := IsAncestor(ctx, "r", "x", "y", run, nil, nil); err == nil || !strings.Contains(err.Error(), "check whether") {
		t.Fatalf("fatal %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := IsAncestor(canceled, "r", "x", "y", func(context.Context, string, string, string) (int, error) { return 1, errors.New("cancel") }, nil, nil); err == nil {
		t.Fatal("canceled query accepted")
	}
}

func TestAgeAndOwner(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	created := now.Add(-2 * time.Hour)
	fields := &AgeFields{}
	read := func(string) (time.Time, error) { return created, nil }
	stat := func(string) (time.Time, error) { t.Fatal("stat after manifest"); return time.Time{}, nil }
	ApplyWorktreeAge(fields, "/wt", []string{"old", " current "}, "orphaned", time.Hour, func() time.Time { return now }, read, stat)
	if fields.Owner != "current" || fields.CreatedAt != created || fields.AgeSeconds != 7200 || fields.TTLSeconds != 3600 || !fields.Expired {
		t.Fatalf("fields %#v", fields)
	}
	fields = &AgeFields{}
	ApplyWorktreeAge(fields, "/wt", nil, "orphaned", 0, func() time.Time { return now },
		func(string) (time.Time, error) { return time.Time{}, errors.New("no manifest") },
		func(string) (time.Time, error) { return now.Add(time.Hour), nil })
	if fields.Owner != "orphaned" || fields.AgeSeconds != 0 || fields.Expired || fields.TTLSeconds != 0 {
		t.Fatalf("fallback %#v", fields)
	}
	fields = &AgeFields{}
	ApplyWorktreeAge(fields, "/wt", nil, "", time.Hour, func() time.Time { t.Fatal("clock on missing age"); return now },
		func(string) (time.Time, error) { return time.Time{}, nil },
		func(string) (time.Time, error) { return time.Time{}, errors.New("missing") })
	if !fields.CreatedAt.IsZero() {
		t.Fatalf("missing age %#v", fields)
	}
	if got := WorktreeOwnerName([]string{"", "  "}, "idle"); got != "idle" {
		t.Fatalf("owner %q", got)
	}
}

func TestReceiptIdentitiesAndPullRequest(t *testing.T) {
	t.Parallel()
	absorbed := AbsorbedConflictReceipt{ReceiptPath: "/receipt", ID: "id", Status: "landed", Lane: "lane", Repository: "org/repo", Target: "main", TargetSHA: "target"}
	absorbed.Candidate.Task = "candidate"
	absorbed.Candidate.SHA = "sha"
	absorbed.Sources = append(absorbed.Sources, struct {
		Task     string `json:"task"`
		Worktree string `json:"worktree"`
		Branch   string `json:"branch"`
		SHA      string `json:"sha"`
	}{Task: "source", SHA: "source-sha"})
	identity := absorbed.Identity()
	if identity.Path != "/receipt" || identity.Candidate.Task != "candidate" || len(identity.Sources) != 1 || identity.Sources[0].SHA != "source-sha" {
		t.Fatalf("absorbed identity %#v", identity)
	}
	retired := RetiredPrepareCandidateReceipt{ReceiptPath: "/retired", ID: "id", TargetSHA: "target", Candidate: retiredcandidateack.Source{Task: "candidate", SHA: "old"}}
	retired.Sources = []retiredcandidateack.Source{{Task: "source"}}
	retiredIdentity := retired.Identity()
	if retiredIdentity.Candidate.SHA != "target" || retired.Candidate.SHA != "old" || len(retiredIdentity.Sources) != 1 {
		t.Fatalf("retired identity %#v", retiredIdentity)
	}
	merged := time.Now()
	pr := &PullRequest{Number: 1, Repository: "org/repo", Base: "main", BaseSHA: "base", HeadSHA: "head", MergeSHA: "merge", Merged: &merged}
	copyPR := *pr
	if !SameAbsorbedPullRequest(nil, nil) || SameAbsorbedPullRequest(pr, nil) || !SameAbsorbedPullRequest(pr, &copyPR) {
		t.Fatal("same PR base cases")
	}
	copyPR.MergeSHA = "changed"
	if SameAbsorbedPullRequest(pr, &copyPR) {
		t.Fatal("changed merge accepted")
	}
	copyPR = *pr
	copyPR.Merged = nil
	if SameAbsorbedPullRequest(pr, &copyPR) {
		t.Fatal("missing merge time accepted")
	}
}
