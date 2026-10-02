package worktreebranches

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInventoryServiceRefAndFleetPorts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	var calls []string
	service := InventoryService{Ports: InventoryPorts{
		Discover: func(string) ([]Repository, error) {
			return []Repository{{Slug: "other/no", Path: "/no"}, {Slug: "org/repo", Path: "/repo"}}, nil
		},
		ListInUse: func(context.Context, string, string) ([]BranchUse, error) {
			return []BranchUse{{Repository: "org/repo", Branch: "feature", Task: "task"}}, nil
		},
		Git: func(_ context.Context, path string, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			switch args[0] {
			case "fetch":
				return "", nil
			case "rev-parse":
				return "main", nil
			case "worktree":
				return "branch refs/heads/main\n", nil
			case "for-each-ref":
				if strings.Contains(args[len(args)-1], "refs/heads/") {
					return "main\x1f" + sha + "\x1f2020-01-01T00:00:00Z\nfeature\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
				}
				return "origin/feature\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
			case "show":
				return sha + "\x1fAuthor\x1fTitle\x1e", nil
			case "cherry":
				return "", nil
			}
			return "", nil
		},
		FetchTarget: func(context.Context, string, string) (string, error) { return sha, nil },
		IsAncestor:  func(context.Context, string, string, string) (bool, error) { return true, nil },
		HostName:    func() (string, error) { return " host ", nil },
	}}
	sweep := InventorySweep{ProjectsRoot: "/projects", Filter: "org/", Repository: "org/repo", Base: "main", Scope: BranchScopeAll, Now: time.Now()}
	entries, diags, paths, err := service.ClassifyFleetBranchesWithPaths(ctx, sweep)
	if err != nil || len(diags) != 0 || paths["org/repo"] != "/repo" || len(entries) != 3 {
		t.Fatalf("fleet: %#v %#v %#v %v", entries, diags, paths, err)
	}
	if entries[0].Disposition != BranchProtected || entries[1].Disposition != BranchInUse || entries[2].Disposition != BranchInUse {
		t.Fatalf("dispositions: %#v", entries)
	}
	if service.BranchEvidenceHost() != "host" {
		t.Fatal("host")
	}
	if len(calls) == 0 {
		t.Fatal("Git port unused")
	}
	_, _, _, err = service.ClassifyFleetBranchesWithPaths(ctx, InventorySweep{ProjectsRoot: "/projects", Repository: "missing/repo"})
	if err == nil {
		t.Fatal("missing repository accepted")
	}
	refs, diag := service.ListRemoteRefs(ctx, "/repo")
	if diag != "" || len(refs) != 1 || refs[0].Name != "feature" {
		t.Fatalf("remote refs: %#v %q", refs, diag)
	}
	var out bytes.Buffer
	ReportBranchProgress(&out, 1, 2, "org/repo")
	ReportBranchSummary(&out, map[string]int{BranchUnique: 1, BranchContained: 2}, time.Second)
	if !strings.Contains(out.String(), "[1/2] scanning org/repo") || !strings.Contains(out.String(), "contained=2 unique=1") {
		t.Fatal(out.String())
	}
}

func TestInventoryServiceClassificationPrecedenceAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	target := strings.Repeat("b", 40)
	ancestor := false
	ancestorErr := error(nil)
	cherry := "+ " + sha
	gitErr := error(nil)
	service := InventoryService{Ports: InventoryPorts{
		IsAncestor: func(context.Context, string, string, string) (bool, error) { return ancestor, ancestorErr },
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "cherry" {
				return cherry, gitErr
			}
			return "", nil
		},
		CommitTree: func(_ context.Context, _ string, sha string) (string, error) { return sha, nil },
	}}
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	sweep := InventorySweep{Base: "main"}
	ref := BranchRef{Name: "feature", SHA: sha}
	classify := func() BranchEntry {
		return service.ClassifyBranch(ctx, repo, sweep, ref, BranchScopeLocal, target, "main", nil, nil, nil)
	}
	if got := classify(); got.Disposition != BranchUnique {
		t.Fatalf("unique: %#v", got)
	}
	ancestor = true
	if got := classify(); got.Disposition != BranchContained {
		t.Fatalf("contained: %#v", got)
	}
	ref.Name = "main"
	if got := classify(); got.Disposition != BranchProtected {
		t.Fatalf("protected: %#v", got)
	}
	ref.Name = "feature"
	if got := service.ClassifyBranch(ctx, repo, sweep, ref, BranchScopeLocal, target, "main", map[string]string{BranchInUseKey(repo.Slug, ref.Name): "task"}, nil, nil); got.Disposition != BranchInUse || got.Task != "task" {
		t.Fatalf("claimed: %#v", got)
	}
	if got := service.ClassifyBranch(ctx, repo, sweep, ref, BranchScopeLocal, target, "main", nil, map[string]bool{ref.Name: true}, nil); got.Disposition != BranchInUse {
		t.Fatalf("checked out: %#v", got)
	}
	ancestor = false
	ancestorErr = errors.New("broken")
	if got := classify(); got.Disposition != BranchUnreadable {
		t.Fatalf("ancestor failure: %#v", got)
	}
	ancestorErr = nil
	gitErr = errors.New("broken")
	if got := classify(); got.Disposition != BranchUnreadable {
		t.Fatalf("cherry failure: %#v", got)
	}
	gitErr = nil
	cherry = ""
	if got := classify(); got.Disposition != BranchAbsorbed {
		t.Fatalf("absorbed: %#v", got)
	}
}

func TestInventoryServicePullRequestEvidenceAndRetiredNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	calls := 0
	service := InventoryService{Ports: InventoryPorts{
		Discover: func(string) ([]Repository, error) { return []Repository{repo}, nil },
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if args[0] == "for-each-ref" {
				return "retired/old\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
			}
			if args[0] == "show" {
				return sha + "\x1fAuthor\x1fTitle\x1e", nil
			}
			return "", nil
		},
		PullRequestAPI: func(_ context.Context, _ string, endpoint string) ([]byte, error) {
			calls++
			if strings.Contains(endpoint, "head=") {
				return []byte(`[{"number":4,"state":"open","head":{"ref":"feature","repo":{"full_name":"org/repo"}},"base":{"ref":"main","repo":{"full_name":"org/repo"}}}]`), nil
			}
			return []byte("[]"), nil
		},
		HostName: func() (string, error) { return "host", nil },
	}}
	evidence := service.ExactBranchPullRequests(ctx, repo, "feature")
	if evidence.Err != nil || len(evidence.Requests) != 1 || evidence.OpenHead == nil || evidence.OpenHead.Number != 4 || calls != 2 {
		t.Fatalf("PR: %#v calls=%d", evidence, calls)
	}
	entry := BranchEntry{}
	cache := map[string]PullRequestEvidence{}
	service.DecorateRemoteBranchPullRequests(ctx, repo, BranchRef{Name: "feature"}, &entry, cache, true)
	service.DecorateRemoteBranchPullRequests(ctx, repo, BranchRef{Name: "feature"}, &entry, cache, true)
	if calls != 4 || !entry.PullRequestQueried {
		t.Fatalf("cache: %#v calls=%d", entry, calls)
	}
	sweep := InventorySweep{ProjectsRoot: "/projects", Base: "main", Scope: BranchScopeLocal, Only: BranchRetired, Now: time.Now()}
	result, err := service.InventoryRetiredNamespace(ctx, sweep, time.Now())
	if err != nil || result.RetiredBranches != 1 || result.RetiredRefs[BranchScopeLocal] != 1 || len(result.Entries) != 2 || result.Entries[0].Author != "Author" {
		t.Fatalf("retired: %#v %v", result, err)
	}
}

func TestInventoryRemoteTagMetadataAndCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	stage := ""
	removed := false
	calls := []string{}
	service := InventoryService{Ports: InventoryPorts{
		Discover: func(string) ([]Repository, error) { return []Repository{repo}, nil },
		Git: func(_ context.Context, path string, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			if stage == "fail-fetch" && args[0] == "fetch" {
				return "", errors.New("offline")
			}
			switch args[0] {
			case "ls-remote":
				return sha + "\trefs/tags/retired/old\n", nil
			case "remote":
				return "/origin", nil
			case "rev-parse":
				return sha, nil
			case "show":
				return "2020-01-01T00:00:00Z\x1fAuthor\x1fTitle", nil
			case "for-each-ref":
				return "origin/retired/old\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
			}
			return "", nil
		},
		TempDir:   func() (string, error) { return "/metadata", nil },
		RemoveDir: func(string) error { removed = true; return nil },
		HostName:  func() (string, error) { return "", errors.New("unavailable") },
	}}
	refs, diag := service.ListRetiredTags(ctx, repo.Path, true, true)
	if diag != "" || len(refs) != 1 || refs[0].UnknownDate || refs[0].Author != "Author" || !removed {
		t.Fatalf("tags: %#v %q removed=%v", refs, diag, removed)
	}
	if service.BranchEvidenceHost() != "unknown" {
		t.Fatal("missing host must be unknown")
	}
	sweep := InventorySweep{ProjectsRoot: "/projects", Base: "main", Scope: BranchScopeRemote, Only: BranchRetired, Now: time.Now()}
	inventory, err := service.InventoryRetiredNamespace(ctx, sweep, time.Now())
	if err != nil || inventory.RetiredRemoteUnavailable || inventory.RetiredRefs[BranchScopeRemote] != 1 || inventory.RetiredTags[BranchScopeRemote] != 1 {
		t.Fatalf("remote inventory: %#v %v", inventory, err)
	}
	counts, names, tagCounts, tagNames, unavailable, diagnostics := service.CountRetiredBranches(ctx, sweep)
	if unavailable || len(diagnostics) != 0 || counts[BranchScopeRemote] != 1 || names != 1 || tagCounts[BranchScopeRemote] != 1 || tagNames != 1 {
		t.Fatalf("counts: %#v %d %#v %d %v %#v", counts, names, tagCounts, tagNames, unavailable, diagnostics)
	}
	stage = "fail-fetch"
	failed, err := service.InventoryRetiredNamespace(ctx, sweep, time.Now())
	if err != nil || !failed.RetiredRemoteUnavailable || len(failed.Diagnostics) < 2 {
		t.Fatalf("remote failure: %#v %v", failed, err)
	}
	if len(calls) == 0 {
		t.Fatal("Git port unused")
	}
}

func TestInventoryPortsFailuresAndSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	mode := "discover"
	service := InventoryService{Ports: InventoryPorts{
		Discover: func(string) ([]Repository, error) {
			if mode == "discover" {
				return nil, errors.New("scan failed")
			}
			return []Repository{{Slug: "org/repo", Path: "/repo"}}, nil
		},
		ListInUse: func(context.Context, string, string) ([]BranchUse, error) {
			return nil, errors.New("claims unavailable")
		},
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if mode == "git-fail" {
				return "", errors.New("git failed")
			}
			if args[0] == "for-each-ref" {
				return "HEAD\x1f" + sha + "\x1f\norigin/feature\x1f" + sha + "\x1f2020-01-01T00:00:00Z\ninvalid\n", nil
			}
			if args[0] == "ls-remote" {
				return "bad\trefs/tags/retired/bad\n" + sha + "\trefs/tags/retired/ok\n", nil
			}
			return "", nil
		},
		FetchTarget: func(context.Context, string, string) (string, error) { return "", errors.New("offline") },
	}}
	if _, err := service.DiscoverBranchRepositories("/projects", ""); err == nil {
		t.Fatal("discovery failure lost")
	}
	sweep := InventorySweep{ProjectsRoot: "/projects", Repository: "org/repo", Base: "main", Scope: BranchScopeRemote}
	if _, err := service.InventoryRetiredNamespace(ctx, sweep, time.Now()); err == nil {
		t.Fatal("retired discovery failure lost")
	}
	if _, _, _, _, _, diags := service.CountRetiredBranches(ctx, sweep); len(diags) != 1 {
		t.Fatalf("count discovery: %#v", diags)
	}
	mode = "ok"
	sweep.Repository = "missing/repo"
	if _, err := service.InventoryRetiredNamespace(ctx, sweep, time.Now()); err == nil {
		t.Fatal("retired selection failure lost")
	}
	if _, _, _, _, _, diags := service.CountRetiredBranches(ctx, sweep); len(diags) != 1 {
		t.Fatalf("count selection: %#v", diags)
	}
	sweep.Repository = "org/repo"
	if _, diag := service.BranchInUseIndex(ctx, "/projects", ""); !strings.Contains(diag, "claims unavailable") {
		t.Fatalf("claims: %q", diag)
	}
	rows, diag := service.InspectRepositoryBranches(ctx, Repository{Slug: "org/repo", Path: "/repo"}, sweep, nil)
	if diag == "" || len(rows) != 1 || rows[0].Disposition != BranchUnreadable {
		t.Fatalf("target failure: %#v %q", rows, diag)
	}
	refs, diag := service.ListRefs(ctx, "/repo", "refs/remotes/origin/", "origin/")
	if diag != "" || len(refs) != 1 || refs[0].Name != "feature" {
		t.Fatalf("parsed refs: %#v %q", refs, diag)
	}
	tags, diag := service.ListRetiredTags(ctx, "/repo", true, false)
	if diag != "" || len(tags) != 1 || tags[0].Name != "retired/ok" || !tags[0].UnknownDate {
		t.Fatalf("remote tags: %#v %q", tags, diag)
	}
	mode = "git-fail"
	if _, diag := service.ListRetiredRemoteRefs(ctx, "/repo"); !strings.Contains(diag, "fetch --prune origin retired namespace") {
		t.Fatalf("retired fetch: %q", diag)
	}
	if _, diag := service.ListRetiredTags(ctx, "/repo", true, false); !strings.Contains(diag, "ls-remote retired tags") {
		t.Fatalf("tag failure: %q", diag)
	}
	if _, diag := service.ListLocalRefs(ctx, "/repo"); !strings.Contains(diag, "enumerate refs/heads") {
		t.Fatalf("ref failure: %q", diag)
	}
	if _, diag := service.CheckedOutLocalBranches(ctx, "/repo"); !strings.Contains(diag, "enumerate linked worktrees") {
		t.Fatalf("worktrees: %q", diag)
	}
}

func TestInventoryPullRequestErrorShapes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	mode := ""
	calls := 0
	service := InventoryService{Ports: InventoryPorts{PullRequestAPI: func(_ context.Context, _ string, endpoint string) ([]byte, error) {
		calls++
		if mode == "api" {
			return nil, errors.New("offline")
		}
		if mode == "base" && strings.Contains(endpoint, "base=") {
			return nil, errors.New("base offline")
		}
		switch mode {
		case "empty":
			return nil, nil
		case "null":
			return []byte("null"), nil
		case "malformed":
			return []byte("["), nil
		}
		if strings.Contains(endpoint, "base=") {
			return []byte(`[{"number":3,"state":"open","head":{"ref":"other","repo":{"full_name":"org/repo"}},"base":{"ref":"feature","repo":{"full_name":"org/repo"}}}]`), nil
		}
		return []byte(`[{"number":2,"state":"open","head":{"ref":"feature","repo":{"full_name":"org/repo"}},"base":{"ref":"main","repo":{"full_name":"org/repo"}}}]`), nil
	}}}
	if e := service.OpenBranchPullRequests(ctx, Repository{Slug: "bad"}, "feature"); e.Err == nil {
		t.Fatal("bad repository accepted")
	}
	if e := service.OpenBranchPullRequests(ctx, repo, ""); e.Err == nil {
		t.Fatal("empty branch accepted")
	}
	for _, tc := range []struct{ mode, want string }{{"api", "query head"}, {"base", "query base"}, {"empty", "empty response"}, {"null", "got null"}, {"malformed", "decode"}} {
		mode = tc.mode
		e := service.OpenBranchPullRequests(ctx, repo, "feature")
		if e.Err == nil || !strings.Contains(e.Err.Error(), tc.want) {
			t.Fatalf("%s: %#v", mode, e)
		}
	}
	mode = ""
	e := service.OpenBranchPullRequests(ctx, repo, "feature")
	if e.Err != nil || len(e.Requests) != 2 || e.OpenHead == nil || e.OpenBase == nil || calls < 2 {
		t.Fatalf("open evidence: %#v calls=%d", e, calls)
	}
	entry := BranchEntry{}
	cache := map[string]PullRequestEvidence{}
	service.DecorateRemoteBranchPullRequests(ctx, repo, BranchRef{Name: "feature"}, &entry, cache, false)
	if !entry.PullRequestQueried || entry.OpenBasePullRequest == nil {
		t.Fatalf("decoration: %#v", entry)
	}
	cache["feature"] = PullRequestEvidence{Err: errors.New("offline")}
	service.DecorateRemoteBranchPullRequests(ctx, repo, BranchRef{Name: "feature"}, &entry, cache, false)
	if !entry.PullRequestQueryFailed || entry.PullRequestQueryError != "offline" {
		t.Fatalf("cached failure: %#v", entry)
	}
}

// heartbeatWriter is a progress writer that is safe to read while the
// inspection still writes to it, and that says when the first write arrived.
type heartbeatWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	once   sync.Once
	wrote  chan struct{}
}

func (w *heartbeatWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once.Do(func() { close(w.wrote) })
	return w.buffer.Write(data)
}

func (w *heartbeatWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestInventoryInspectionHeartbeatAndFailurePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	service := InventoryService{}
	inspect := func(context.Context, Repository, InventorySweep, map[string]string) ([]BranchEntry, string) {
		return []BranchEntry{{Branch: "feature"}}, "done"
	}
	rows, diag := service.InspectRepositoryBranchesWithHeartbeat(ctx, repo, InventorySweep{}, nil, 1, 1, time.Millisecond, inspect)
	if diag != "done" || len(rows) != 1 {
		t.Fatalf("direct: %#v %q", rows, diag)
	}
	// The slow inspection ends only once a heartbeat has been written, so a
	// loaded machine cannot finish it before the first tick.
	progress := &heartbeatWriter{wrote: make(chan struct{})}
	gate := make(chan struct{})
	slow := func(context.Context, Repository, InventorySweep, map[string]string) ([]BranchEntry, string) {
		<-gate
		return []BranchEntry{{Branch: "slow"}}, "done"
	}
	go func() { <-progress.wrote; close(gate) }()
	rows, diag = service.InspectRepositoryBranchesWithHeartbeat(ctx, repo, InventorySweep{Progress: progress}, nil, 1, 2, time.Millisecond, slow)
	if diag != "done" || len(rows) != 1 || !strings.Contains(progress.String(), "still scanning org/repo") {
		t.Fatalf("heartbeat: %#v %q %q", rows, diag, progress.String())
	}
	stage := ""
	service.Ports = InventoryPorts{
		FetchTarget: func(context.Context, string, string) (string, error) { return sha, nil },
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if stage == "git-error" && (args[0] == "for-each-ref" || args[0] == "worktree" || args[0] == "fetch") {
				return "", errors.New("unreadable")
			}
			switch args[0] {
			case "rev-parse":
				return "main", nil
			case "for-each-ref":
				if strings.Contains(args[len(args)-1], "heads") {
					return "retired/old\x1f" + sha + "\x1f2020-01-01T00:00:00Z\nmain\x1f" + sha + "\x1f2020-01-01T00:00:00Z\nfeature\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
				}
				return "origin/retired/old\x1f" + sha + "\x1f2020-01-01T00:00:00Z\norigin/feature\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
			case "show":
				return sha + "\x1fAuthor\x1fTitle\x1e", nil
			}
			return "", nil
		},
		IsAncestor:     func(context.Context, string, string, string) (bool, error) { return true, nil },
		PullRequestAPI: func(context.Context, string, string) ([]byte, error) { return []byte("[]"), nil },
	}
	sweep := InventorySweep{Base: "main", Scope: BranchScopeAll, Name: "feature", WithPRs: true}
	rows, diag = service.InspectRepositoryBranches(ctx, repo, sweep, nil)
	if diag != "" || len(rows) != 2 || !rows[1].PullRequestQueried {
		t.Fatalf("selected: %#v %q", rows, diag)
	}
	sweep.Name = ""
	sweep.WithPRs = false
	rows, diag = service.InspectRepositoryBranches(ctx, repo, sweep, nil)
	if diag != "" || len(rows) != 3 {
		t.Fatalf("retired excluded: %#v %q", rows, diag)
	}
	sweep.IncludeRetired = true
	sweep.WithPRs = true
	rows, diag = service.InspectRepositoryBranches(ctx, repo, sweep, nil)
	if diag != "" || len(rows) != 5 {
		t.Fatalf("retired included: %#v %q", rows, diag)
	}
	stage = "git-error"
	rows, diag = service.InspectRepositoryBranches(ctx, repo, sweep, nil)
	if diag == "" || len(rows) != 0 {
		t.Fatalf("git failure: %#v %q", rows, diag)
	}
}

func TestInventoryPullRequestStatesAndIdentities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	head := `[{"number":4,"state":"open","head":{"ref":"feature","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}},{"number":8,"state":"open","head":{"ref":"feature","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}},{"number":2,"state":"closed","merged_at":"2020-01-01T00:00:00Z","head":{"ref":"feature","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}},{"number":7,"state":"unknown","head":{"ref":"feature","repo":{"full_name":"org/repo"}},"base":{"ref":"main"}}]`
	base := `[{"number":5,"state":"open","head":{"ref":"other"},"base":{"ref":"feature","repo":{"full_name":"org/repo"}}},{"number":9,"state":"open","head":{"ref":"other"},"base":{"ref":"feature","repo":{"full_name":"org/repo"}}}]`
	service := InventoryService{Ports: InventoryPorts{PullRequestAPI: func(_ context.Context, _ string, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "head=") {
			return []byte(head), nil
		}
		return []byte(base), nil
	}}}
	e := service.ExactBranchPullRequests(ctx, repo, "feature")
	if e.Err != nil || len(e.Requests) != 5 || e.OpenHead == nil || e.OpenHead.Number != 8 || e.OpenBase == nil || e.OpenBase.Number != 9 || e.Requests[0].Number != 5 || e.Requests[3].Role != "head" {
		t.Fatalf("states: %#v", e)
	}
	head = `[{"number":1,"state":"open","head":{"ref":"feature"}}]`
	if e = service.ExactBranchPullRequests(ctx, repo, "feature"); e.Err == nil || !strings.Contains(e.Err.Error(), "no repository identity") {
		t.Fatalf("head identity: %#v", e)
	}
	head = `[]`
	base = `[{"number":1,"state":"open","base":{"ref":"feature"}}]`
	if e = service.ExactBranchPullRequests(ctx, repo, "feature"); e.Err == nil || !strings.Contains(e.Err.Error(), "no repository identity") {
		t.Fatalf("base identity: %#v", e)
	}
}

func TestInventoryRetiredCountAndMetadataFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	step := ""
	removed := 0
	service := InventoryService{Ports: InventoryPorts{
		Discover: func(string) ([]Repository, error) { return []Repository{repo}, nil },
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			if step == args[0] {
				return "", errors.New("failed")
			}
			switch args[0] {
			case "for-each-ref":
				return "retired/old\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
			case "ls-remote":
				return sha + "\trefs/tags/retired/old\n", nil
			case "remote":
				return "/origin", nil
			case "rev-parse":
				if step == "drift" {
					return strings.Repeat("b", 40), nil
				}
				return sha, nil
			case "show":
				if step == "bad-metadata" {
					return "bad", nil
				}
				if step == "bad-date" {
					return "bad\x1fA\x1fT", nil
				}
				return "2020-01-01T00:00:00Z\x1fA\x1fT", nil
			}
			return "", nil
		},
		TempDir: func() (string, error) {
			if step == "temp" {
				return "", errors.New("failed")
			}
			return "/temp", nil
		},
		RemoveDir: func(string) error { removed++; return nil },
		HostName:  func() (string, error) { return "host", nil },
	}}
	for _, tc := range []struct{ step, want string }{{"remote", "resolve origin"}, {"temp", "create retired tag metadata"}, {"init", "initialize retired tag metadata"}, {"fetch", "fetch retired tag metadata"}, {"drift", "changed during metadata fetch"}, {"rev-parse", "changed during metadata fetch"}, {"show", "read retired tag commit metadata"}, {"bad-metadata", "invalid retired tag commit metadata"}, {"bad-date", "parse retired tag commit date"}} {
		step = tc.step
		_, diag := service.ListRetiredTags(ctx, "/repo", true, true)
		if !strings.Contains(diag, tc.want) {
			t.Fatalf("%s: %q", step, diag)
		}
	}
	if removed == 0 {
		t.Fatal("metadata temporary directory not removed")
	}
	step = ""
	sweep := InventorySweep{ProjectsRoot: "/projects", Base: "main", Scope: BranchScopeLocal, Only: BranchRetired, Now: time.Now()}
	counts, names, tags, tagNames, unavailable, diags := service.CountRetiredBranches(ctx, sweep)
	if unavailable || len(diags) != 0 || counts[BranchScopeLocal] != 1 || names != 1 || tags[BranchScopeLocal] != 1 || tagNames != 1 {
		t.Fatalf("local count: %#v %d %#v %d %v %#v", counts, names, tags, tagNames, unavailable, diags)
	}
	sweep.Only = BranchUnique
	inventory, err := service.InventoryRetiredNamespace(ctx, sweep, time.Now())
	if err != nil || len(inventory.Entries) != 0 || !strings.Contains(strings.Join(inventory.Diagnostics, " "), "cannot match") {
		t.Fatalf("only conflict: %#v %v", inventory, err)
	}
	step = "for-each-ref"
	sweep.Only = BranchRetired
	inventory, err = service.InventoryRetiredNamespace(ctx, sweep, time.Now())
	if err != nil || len(inventory.Diagnostics) < 3 {
		t.Fatalf("local ref/tag errors: %#v %v", inventory, err)
	}
	_, _, _, _, _, diags = service.CountRetiredBranches(ctx, sweep)
	if len(diags) < 2 {
		t.Fatalf("count local errors: %#v", diags)
	}
	step = "for-each-ref"
	sweep.Scope = BranchScopeRemote
	_, _, _, _, unavailable, diags = service.CountRetiredBranches(ctx, sweep)
	if !unavailable || len(diags) == 0 {
		t.Fatalf("count remote refs error: %v %#v", unavailable, diags)
	}
	step = "ls-remote"
	sweep.Scope = BranchScopeRemote
	inventory, err = service.InventoryRetiredNamespace(ctx, sweep, time.Now())
	if err != nil || !inventory.RetiredRemoteUnavailable {
		t.Fatalf("remote tag error: %#v %v", inventory, err)
	}
	_, _, _, _, unavailable, diags = service.CountRetiredBranches(ctx, sweep)
	if !unavailable || len(diags) == 0 {
		t.Fatalf("count remote tag error: %v %#v", unavailable, diags)
	}
}

func TestInventoryRemainingFleetAndRefBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sha := strings.Repeat("a", 40)
	repo := Repository{Slug: "org/repo", Path: "/repo"}
	service := InventoryService{Ports: InventoryPorts{
		Discover:    func(string) ([]Repository, error) { return nil, errors.New("scan failed") },
		ListInUse:   func(context.Context, string, string) ([]BranchUse, error) { return nil, errors.New("inventory failed") },
		FetchTarget: func(context.Context, string, string) (string, error) { return "", errors.New("target failed") },
		Git:         func(context.Context, string, ...string) (string, error) { return "", errors.New("show failed") },
		HostName:    func() (string, error) { return "host", nil },
	}}
	sweep := InventorySweep{ProjectsRoot: "/projects", Base: "main", Scope: BranchScopeLocal}
	if _, _, err := service.ClassifyFleetBranches(ctx, sweep); err == nil {
		t.Fatal("fleet discovery failure lost")
	}
	if _, _, _, err := service.ClassifyFleetBranchesWithPaths(ctx, sweep); err == nil {
		t.Fatal("fleet paths discovery failure lost")
	}
	service.Ports.Discover = func(string) ([]Repository, error) { return []Repository{repo}, nil }
	rows, diags, err := service.ClassifyFleetBranches(ctx, sweep)
	if err != nil || len(rows) != 1 || len(diags) != 2 || !strings.Contains(strings.Join(diags, " "), "inventory failed") || !strings.Contains(strings.Join(diags, " "), "target failed") {
		t.Fatalf("fleet diagnostics: %#v %#v %v", rows, diags, err)
	}
	entries := []BranchEntry{{SHA: ""}, {SHA: sha}}
	service.DecorateBranchCommits(ctx, repo.Path, entries)
	if entries[1].Author != "" {
		t.Fatalf("metadata failure mutated entry: %#v", entries)
	}
	service.DecorateBranchCommits(ctx, repo.Path, []BranchEntry{{SHA: ""}})
	names := map[string]bool{}
	selected := []BranchEntry{}
	count := service.AppendRetiredRefEntries(ctx, &selected, names, repo, InventorySweep{Base: "main"}, []BranchRef{{Name: "feature", SHA: sha}}, BranchScopeLocal, "branch")
	if count != 0 || len(selected) != 0 {
		t.Fatalf("active ref appended: %d %#v", count, selected)
	}
	service.Ports.Git = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "for-each-ref" {
			return "HEAD\x1f" + sha + "\x1f2020-01-01T00:00:00Z\n", nil
		}
		return "", nil
	}
	refs, diag := service.ListRefs(ctx, repo.Path, "refs/heads/", "")
	if diag != "" || len(refs) != 0 {
		t.Fatalf("HEAD included: %#v %q", refs, diag)
	}
}
