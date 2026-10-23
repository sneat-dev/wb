package streams

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type stCovGitTagsErr struct {
	*fakeGit
	err error
}

func (git stCovGitTagsErr) Tags(context.Context, string, string) ([]string, error) {
	return nil, git.err
}

type stCovGitLogErr struct {
	*fakeGit
	err error
}

func (git stCovGitLogErr) LogSubjects(context.Context, string, string, string) ([]string, error) {
	return nil, git.err
}

type stCovHubBranchErr struct {
	*fakeHub
	err error
}

func (hub stCovHubBranchErr) PullRequestForBranch(context.Context, string, string) (PullRequest, bool, error) {
	return PullRequest{}, false, hub.err
}

func TestStatusReportsAMissingStream(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Status(context.Background(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status of a missing stream = %v, want ErrNotFound", err)
	}
}

func TestStatusReportsAnUnreadableEventLog(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{Name: "logs", Phase: PhaseOpen}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(engine.Store.EventLog("logs").Path, 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Status(context.Background(), "logs")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Unknowns) == 0 || !strings.Contains(strings.Join(status.Unknowns, "; "), "historical publication attempts") {
		t.Fatalf("unknowns = %v, want the unreadable event log reported", status.Unknowns)
	}
}

func TestStatusReportsAStaleCloneAndReadFailures(t *testing.T) {
	t.Parallel()
	engine, git, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	const repository = "acme/app"
	if _, err := engine.Store.Create(Stream{
		Name: "degraded", Phase: PhaseOpen,
		Members: []Member{{
			Repository: repository, Role: RoleLibrary, Worktree: worktree,
			Branch: "stream/degraded", Base: "main",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	git.fetchErr[worktree] = errors.New("origin unreachable")
	hub.targetingErr[worktree+" stream/degraded"] = errors.New("gh list failed")

	status, err := engine.Status(context.Background(), "degraded")
	if err != nil {
		t.Fatal(err)
	}
	unknowns := strings.Join(status.Unknowns, "; ")
	if !strings.Contains(unknowns, "could not re-read origin") {
		t.Errorf("unknowns = %q, want the stale-clone warning", unknowns)
	}
	if !strings.Contains(unknowns, "open agent pull requests") {
		t.Errorf("unknowns = %q, want the agent-pull-request read failure", unknowns)
	}
}

func TestStatusReportsAMemberPullRequestReadFailure(t *testing.T) {
	t.Parallel()
	engine, _, hub, _ := newTestEngine(t)
	worktree := t.TempDir()
	engine.GitHub = stCovHubBranchErr{fakeHub: hub, err: errors.New("gh view failed")}
	if _, err := engine.Store.Create(Stream{
		Name: "unreadable-pr", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary, Worktree: worktree,
			Branch: "stream/unreadable-pr", Base: "main",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Status(context.Background(), "unreadable-pr")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(status.Unknowns, "; "), "member pull request") {
		t.Fatalf("unknowns = %v, want the member pull-request read failure", status.Unknowns)
	}
}

func TestStatusReportsALinkedConsumerGap(t *testing.T) {
	t.Parallel()
	engine, _, _, _ := newTestEngine(t)
	if _, err := engine.Store.Create(Stream{
		Name: "alternate", Phase: PhaseOpen,
		LinkedConsumers: []LinkedConsumerBinding{{
			Repository: "acme/alternate", Worktree: "/wt/alternate",
			Links: []Link{{
				Library: "/wt/library", LibraryRepository: "acme/library",
				Mechanism: MechanismPnpmLink, Identity: "@acme/core", PreviousVersion: "1.0.0",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := engine.Status(context.Background(), "alternate")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.LinkedConsumers) != 1 {
		t.Fatalf("linked consumers = %#v, want the admitted alternate worktree", status.LinkedConsumers)
	}
	gap := status.LinkedConsumers[0]
	if gap.Repository != "acme/alternate" || gap.Identity != "@acme/core" || gap.PreviousVersion != "1.0.0" || gap.Mechanism != MechanismPnpmLink {
		t.Fatalf("linked consumer = %#v", gap)
	}
}

func TestStatusReportsUnreadableUnabsorbedCommits(t *testing.T) {
	t.Parallel()
	engine, git, _, _ := newTestEngine(t)
	worktree := t.TempDir()
	if _, err := engine.Store.Create(Stream{
		Name: "backlog-error", Phase: PhaseOpen,
		Members: []Member{{
			Repository: "acme/app", Role: RoleLibrary, Worktree: worktree,
			Branch: "stream/backlog-error", Base: "main",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	git.notInErr[worktree+" stream/backlog-error origin/main"] = errors.New("cherry failed")

	status, err := engine.Status(context.Background(), "backlog-error")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(status.Unknowns, "; "), "unabsorbed commits") {
		t.Fatalf("unknowns = %v, want the unabsorbed-commit read failure", status.Unknowns)
	}
}

func TestCollapsePatchIdenticalFallsBackToTheSHA(t *testing.T) {
	t.Parallel()
	collapsed := collapsePatchIdentical([]Commit{
		{SHA: "1111111111111111111111111111111111111111"},
		{SHA: "2222222222222222222222222222222222222222", Subject: "a subject"},
	})
	if len(collapsed) != 2 {
		t.Fatalf("collapsed = %v, want both commits named", collapsed)
	}
	if collapsed[0] != "1111111111111111111111111111111111111111" {
		t.Fatalf("unnamed commit = %q, want its SHA as the label", collapsed[0])
	}
	if collapsed[1] != "a subject" {
		t.Fatalf("named commit = %q", collapsed[1])
	}
}

func stCovIdentityLibrary(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	writeFiles(t, root, map[string]string{
		"backend/go.mod":         "module github.com/acme/library/backend\n\ngo 1.27\n",
		"libs/core/package.json": `{"name":"@acme/core","version":"0.5.0"}`,
	})
	return root
}

func TestLibraryGapsReportsEveryUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("no library member", func(t *testing.T) {
		t.Parallel()
		engine, _, _, _ := newTestEngine(t)
		status := Status{}
		engine.libraryGaps(ctx, Stream{}, &status)
		if !strings.Contains(strings.Join(status.Unknowns, "; "), "no library member") {
			t.Fatalf("unknowns = %v", status.Unknowns)
		}
	})

	t.Run("unreadable tags", func(t *testing.T) {
		t.Parallel()
		engine, git, _, _ := newTestEngine(t)
		library := stCovIdentityLibrary(t)
		engine.Git = stCovGitTagsErr{fakeGit: git, err: errors.New("tags unreadable")}
		status := Status{}
		engine.libraryGaps(ctx, Stream{Members: []Member{{
			Repository: "acme/library", Role: RoleLibrary, Worktree: library, Base: "main",
		}}}, &status)
		if !strings.Contains(strings.Join(status.Unknowns, "; "), "tags:") {
			t.Fatalf("unknowns = %v", status.Unknowns)
		}
	})

	t.Run("unreadable merged-untagged log", func(t *testing.T) {
		t.Parallel()
		engine, git, _, _ := newTestEngine(t)
		library := stCovIdentityLibrary(t)
		git.tags[library] = []string{"backend/v0.5.0"}
		engine.Git = stCovGitLogErr{fakeGit: git, err: errors.New("log unreadable")}
		status := Status{}
		engine.libraryGaps(ctx, Stream{Members: []Member{{
			Repository: "acme/library", Role: RoleLibrary, Worktree: library, Base: "main",
		}}}, &status)
		if !strings.Contains(strings.Join(status.Unknowns, "; "), "merged-but-untagged") {
			t.Fatalf("unknowns = %v", status.Unknowns)
		}
	})

	t.Run("no version tag", func(t *testing.T) {
		t.Parallel()
		engine, git, _, _ := newTestEngine(t)
		library := stCovIdentityLibrary(t)
		git.tags[library] = []string{"not-a-version"}
		status := Status{}
		engine.libraryGaps(ctx, Stream{Members: []Member{{
			Repository: "acme/library", Role: RoleLibrary, Worktree: library, Base: "main",
		}}}, &status)
		if !strings.Contains(strings.Join(status.Unknowns, "; "), "carries no version tag") {
			t.Fatalf("unknowns = %v", status.Unknowns)
		}
	})
}

// Every consumer still below the published version is reported, named per
// repository and per identity, with the declarations WB could not read
// surfaced rather than silently omitted.
func TestLibraryGapsReportsEveryConsumerBehind(t *testing.T) {
	t.Parallel()
	engine, git, _, _ := newTestEngine(t)
	library := stCovIdentityLibrary(t)
	behind := t.TempDir()
	writeFiles(t, behind, map[string]string{
		"backend/go.mod": "module github.com/acme/consumer/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
		"package.json":   `{"name":"consumer","dependencies":{"@acme/core":"0.1.0"}}`,
	})
	current := t.TempDir()
	writeFiles(t, current, map[string]string{
		"backend/go.mod": "module github.com/acme/current/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.5.0\n",
		"package.json":   `{"name":"current","dependencies":{"@acme/core":"0.5.0"}}`,
	})
	otherBehind := t.TempDir()
	writeFiles(t, otherBehind, map[string]string{
		"backend/go.mod": "module github.com/acme/other/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.3.0\n",
	})
	broken := t.TempDir()
	writeFiles(t, broken, map[string]string{"package.json": "{not json"})

	git.tags[library] = []string{"backend/v0.5.0"}
	status := Status{}
	engine.libraryGaps(context.Background(), Stream{
		Name: "gaps",
		Members: []Member{
			{Repository: "acme/library", Role: RoleLibrary, Worktree: library, Base: "main"},
			{Repository: "acme/behind", Role: RoleConsumer, Worktree: behind},
			{Repository: "acme/other-behind", Role: RoleConsumer, Worktree: otherBehind},
			{Repository: "acme/current", Role: RoleConsumer, Worktree: current},
			{Repository: "acme/broken", Role: RoleConsumer, Worktree: broken},
		},
	}, &status)

	if len(status.ConsumersBehind) != 3 {
		t.Fatalf("consumers behind = %#v, want the three declarations below the published version", status.ConsumersBehind)
	}
	// Sorted by repository then identity; the consumer already at the
	// published version contributes none.
	var repositories []string
	for _, row := range status.ConsumersBehind {
		repositories = append(repositories, row.Repository)
		if row.Published != "v0.5.0" {
			t.Fatalf("behind row = %#v", row)
		}
	}
	if repositories[0] != "acme/behind" || repositories[1] != "acme/behind" || repositories[2] != "acme/other-behind" {
		t.Fatalf("behind repositories = %v, want repository order", repositories)
	}
	if status.ConsumersBehind[0].Identity != "@acme/core" || status.ConsumersBehind[1].Identity != "github.com/acme/library/backend" {
		t.Fatalf("behind identities = %#v, want the consumer's two declarations sorted", status.ConsumersBehind[:2])
	}
	if !strings.Contains(strings.Join(status.Unknowns, "; "), "acme/broken: declared versions") {
		t.Fatalf("unknowns = %v, want the unreadable consumer manifested", status.Unknowns)
	}
}

func TestLibraryGapsReportsAMergedUntaggedCommit(t *testing.T) {
	t.Parallel()
	engine, git, _, _ := newTestEngine(t)
	library := stCovIdentityLibrary(t)
	git.tags[library] = []string{"backend/v0.5.0"}
	git.log[library+" backend/v0.5.0..origin/main"] = []string{"feat: merged but untagged"}
	status := Status{}
	engine.libraryGaps(context.Background(), Stream{
		Members: []Member{{Repository: "acme/library", Role: RoleLibrary, Worktree: library, Base: "main"}},
	}, &status)
	if status.MergedUntagged == nil || len(status.MergedUntagged.Commits) != 1 {
		t.Fatalf("merged-untagged = %#v", status.MergedUntagged)
	}
	if status.MergedUntagged.LatestTag != "backend/v0.5.0" {
		t.Fatalf("latest tag = %q", status.MergedUntagged.LatestTag)
	}
}

func TestLibraryTagPatternAndTagVersionEdgeCases(t *testing.T) {
	t.Parallel()
	if got := libraryTagPattern([]Identity{{Ecosystem: EcosystemNpm, Name: "@acme/core"}}); got != "v*" {
		t.Fatalf("pattern for an npm-only library = %q, want v*", got)
	}
	if got := libraryTagPattern([]Identity{{Ecosystem: EcosystemGo, Name: "example.test/root", Directory: "."}}); got != "v*" {
		t.Fatalf("pattern for a root Go module = %q, want v*", got)
	}
	if got := libraryTagPattern([]Identity{{Ecosystem: EcosystemGo, Name: "example.test/backend", Directory: "backend"}}); got != "backend/v*" {
		t.Fatalf("pattern for a nested Go module = %q", got)
	}
	if got := newestTag(nil); got != "" {
		t.Fatalf("newestTag(nil) = %q, want empty", got)
	}
	if got := tagVersion("not-a-version"); got != "" {
		t.Fatalf("tagVersion without a v prefix = %q, want empty", got)
	}
	if got := tagVersion("backend/v0.5.0"); got != "v0.5.0" {
		t.Fatalf("tagVersion = %q", got)
	}
	if got := tagVersion(""); got != "" {
		t.Fatalf("tagVersion of an empty tag = %q", got)
	}
}

func TestVersionComparisonTreatsAnUnreadablePublishedVersionAsCurrent(t *testing.T) {
	t.Parallel()
	if !versionAtLeast("1.2.3", "not-a-version") {
		t.Fatal("an unreadable published version was reported as behind")
	}
	if _, ok := semverParts("1.x.3"); ok {
		t.Fatal("semverParts accepted a non-numeric component")
	}
	if _, ok := semverParts("1.2"); ok {
		t.Fatal("semverParts accepted a two-component version")
	}
}
