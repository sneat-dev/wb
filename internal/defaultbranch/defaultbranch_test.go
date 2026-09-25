package defaultbranch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

// errBoomForCmdWB mirrors cmd/wb/verify_receipt_test.go's package-wide
// sentinel of the same name (this package needs its own copy).
var errBoomForCmdWB = errors.New("boom")

type defaultBranchFailWriter struct {
	writes int
	failAt int
}

func (writer *defaultBranchFailWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errors.New("output unavailable")
	}
	return len(value), nil
}

func TestDefaultBranchPagesMigrationAcceptsOnlyVerifiedLegacySources(t *testing.T) {
	for _, sourcePath := range []string{"/", "/docs"} {
		t.Run("migrates "+sourcePath, func(t *testing.T) {
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead, defaultBranchExecute = defaultBranchPagesFixture(t, sourcePath, "legacy", "master", false, false)
			planned := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, true)
			if planned.Disposition != "drift" || planned.PagesBefore == nil || planned.PagesBefore.Path != sourcePath {
				t.Fatalf("plan = %#v", planned)
			}
			renamed := engine.applyDefaultBranchWithCheckpoint(context.Background(), planned, func(Repository) error { return nil })
			result := engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), renamed, func(Repository) error { return nil })
			if result.Disposition != "compliant" || !defaultBranchPagesTerminal(result) || result.PagesAfter.Path != sourcePath {
				t.Fatalf("result = %#v", result)
			}
		})
	}
	for name, test := range map[string]struct{ buildType, branch string }{
		"workflow":       {"workflow", "master"},
		"foreign source": {"legacy", "release"},
		"unknown path":   {"legacy", "master"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			path := "/"
			if name == "unknown path" {
				path = "/site"
			}
			defaultBranchRead, defaultBranchExecute = defaultBranchPagesFixture(t, path, test.buildType, test.branch, false, false)
			result := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, true)
			if result.Disposition != "blocked" || !strings.Contains(result.Error, "pages source") {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestDefaultBranchPagesMigrationFailsClosedAfterPlanChangesOrPostWriteMismatch(t *testing.T) {
	for name, test := range map[string]struct{ changedBeforeWrite, postWriteMismatch bool }{
		"changed source":      {changedBeforeWrite: true},
		"post-write mismatch": {postWriteMismatch: true},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead, defaultBranchExecute = defaultBranchPagesFixture(t, "/", "legacy", "master", test.changedBeforeWrite, test.postWriteMismatch)
			planned := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, true)
			renamed := engine.applyDefaultBranchWithCheckpoint(context.Background(), planned, func(Repository) error { return nil })
			result := engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), renamed, func(Repository) error { return nil })
			if result.Disposition != "error" || defaultBranchPagesTerminal(result) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestDefaultBranchPagesRecognizesVerifiedAutomaticTransition(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pages":
			return []byte(`{"build_type":"legacy","source":{"branch":"main","path":"/"}}`), nil
		case "repos/acme/app/git/ref/heads/master":
			return nil, errors.New("HTTP 404")
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutated := false
	defaultBranchExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	repo := Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "same", Disposition: "compliant", PagesBefore: &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"}, PagesPhase: "prepared"}
	got := engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, func(Repository) error { return nil })
	if got.Disposition != "compliant" || got.PagesPhase != "verified" || got.PagesAfter == nil || mutated {
		t.Fatalf("got=%#v mutated=%t", got, mutated)
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pages":
			return []byte(`{"build_type":"legacy","source":{"branch":"main","path":"/"}}`), nil
		case "repos/acme/app/git/ref/heads/master":
			return []byte(`{"ref":"refs/heads/master"}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	got = engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, nil)
	if got.Disposition != "error" || !strings.Contains(got.Error, "old master ref still exists") || got.PagesPhase == "verified" || mutated {
		t.Fatalf("present old ref accepted: %#v mutated=%t", got, mutated)
	}
	repo.ObservedDefault = "release"
	repo.PagesBefore.Branch = "release"
	got = engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, nil)
	if got.Disposition != "error" || got.PagesPhase == "verified" || mutated {
		t.Fatalf("foreign source accepted as automatic transition: %#v mutated=%t", got, mutated)
	}
}

func TestDefaultBranchPagesAutomaticResumeRequiresExactReceiptAndRemoteProof(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	sha := "04188cc2ba6c039f3b6eb65b429c3b7e1810f2bd"
	previous := Repository{
		Repository: "RxStore/rxstore.github.io", RepositoryID: 209892346,
		ObservedDefault: "master", VerifiedDefault: "main", Desired: "main",
		OldHead: sha, NewHead: sha, Disposition: "error",
		Error:          "Pages source changed after planning; WB will not overwrite it",
		RenameAccepted: true, PagesBefore: &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"},
		PagesPhase: "prepared", Actions: []string{"renamed master to main", "verified default branch and head"},
	}
	prior := &Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{previous}}
	current := Repository{Repository: previous.Repository, RepositoryID: previous.RepositoryID, ObservedDefault: "main", Desired: "main", OldHead: sha, Disposition: "compliant"}
	reads := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		reads++
		switch endpoint {
		case "repos/RxStore/rxstore.github.io/git/ref/heads/master":
			return nil, errors.New("HTTP 404")
		case "repos/RxStore/rxstore.github.io/pages":
			return []byte(`{"build_type":"legacy","source":{"branch":"main","path":"/"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	if source, head, reason := engine.defaultBranchPagesAutomaticResume(context.Background(), prior, current); source != "master" || head != sha || reason != "" || reads != 2 {
		t.Fatalf("automatic resume = %q %q %q reads=%d", source, head, reason, reads)
	}
	for name, mutate := range map[string]func(*Repository, *Repository){
		"forged actions":          func(p, _ *Repository) { p.Actions[0] = "set default to main" },
		"different repository ID": func(_, c *Repository) { c.RepositoryID++ },
		"changed target head":     func(_, c *Repository) { c.OldHead = strings.Repeat("a", 40) },
		"Pages write accepted":    func(p, _ *Repository) { p.PagesAccepted = true },
		"target preexisted":       func(p, _ *Repository) { p.TargetExists = true },
		"unsupported Pages path":  func(p, _ *Repository) { p.PagesBefore.Path = "/site" },
		"different destination": func(p, c *Repository) {
			p.Desired, p.VerifiedDefault, p.Actions[0] = "trunk", "trunk", "renamed master to trunk"
			c.Desired, c.ObservedDefault = "trunk", "trunk"
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyPrevious := previous
			copyPrevious.Actions = append([]string(nil), previous.Actions...)
			copyPages := *previous.PagesBefore
			copyPrevious.PagesBefore = &copyPages
			copyCurrent := current
			mutate(&copyPrevious, &copyCurrent)
			reads = 0
			receipt := &Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{copyPrevious}}
			if source, head, reason := engine.defaultBranchPagesAutomaticResume(context.Background(), receipt, copyCurrent); source != "" || head != "" || reason == "" || reads != 0 {
				t.Fatalf("forged automatic resume = %q %q %q reads=%d", source, head, reason, reads)
			}
		})
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.HasSuffix(endpoint, "/git/ref/heads/master") {
			return nil, errors.New("HTTP 404")
		}
		return []byte(`{"build_type":"legacy","source":{"branch":"main","path":"/docs"}}`), nil
	}
	if source, head, reason := engine.defaultBranchPagesAutomaticResume(context.Background(), prior, current); source != "" || head != "" || !strings.Contains(reason, "Pages source") {
		t.Fatalf("changed Pages path accepted: %q %q %q", source, head, reason)
	}
}

func TestDefaultBranchPagesMigrationReportsUnfinishedWhenDefaultAlreadyMatches(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pages":
			return []byte(`{"build_type":"legacy","source":{"branch":"master","path":"/"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	result := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, true)
	if result.Disposition != "drift" || result.PagesBefore == nil || result.PagesPhase != "unfinished" || !strings.Contains(result.Error, "unfinished") {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunDefaultBranchRepairsUnfinishedPagesWithoutRenamingDefault(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	projectsRoot := t.TempDir()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return config }
	source, mutations, renames := "master", 0, 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pages":
			return []byte(`{"build_type":"legacy","source":{"branch":"` + source + `","path":"/docs"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		got := strings.Join(args, " ")
		if strings.Contains(got, "/rename") {
			renames++
			return githubobserver.CommandResponse{Err: errors.New("rename must not run")}
		}
		if got != "api --method PUT repos/acme/app/pages -f source[branch]=main -f source[path]=/docs" {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + got)}
		}
		source = "main"
		return githubobserver.CommandResponse{}
	}
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, MigratePagesSource: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	got := report.Repositories[0]
	if mutations != 1 || renames != 0 || got.Disposition != "compliant" || got.PagesPhase != "verified" || len(got.CanonicalClones) != 0 {
		t.Fatalf("report=%#v mutations=%d renames=%d", got, mutations, renames)
	}
}

// A dry run (no --apply) still persists its plan under --report-dir, so an
// operator can inspect exactly what apply would do before running it for
// real; it must never invoke a mutation.
func TestRunDefaultBranchDryRunStillPersistsReportUnderReportDir(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	projectsRoot := t.TempDir()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return config }
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pages":
			return []byte(`{"build_type":"legacy","source":{"branch":"main","path":"/docs"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutations := 0
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{Err: errors.New("dry run must never mutate")}
	}
	reportDir := t.TempDir()
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: reportDir}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 0 {
		t.Fatalf("dry run invoked %d mutation(s), want 0", mutations)
	}
	if report.ReportPath == "" {
		t.Fatal("dry run report path is empty; --report-dir must still be honoured")
	}
	stored, err := os.ReadFile(report.ReportPath)
	if err != nil {
		t.Fatalf("dry run report was not persisted under --report-dir: %v", err)
	}
	if !strings.Contains(string(stored), "acme/app") {
		t.Fatalf("persisted dry-run report = %q, want it to name the repository", stored)
	}
	if !strings.HasPrefix(report.ReportPath, reportDir) {
		t.Fatalf("report path %q was not written under --report-dir %q", report.ReportPath, reportDir)
	}
}

func TestUnfinishedPagesRepairBlocksWhenDefaultChanges(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"trunk"}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutated := false
	defaultBranchExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	repo := Repository{Repository: "acme/app", ObservedDefault: "main", Desired: "main", OldHead: "same", Disposition: "drift", PagesBefore: &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"}, PagesPhase: "unfinished"}
	result := engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, func(Repository) error { return nil })
	if result.Disposition != "error" || mutated {
		t.Fatalf("result=%#v mutated=%t", result, mutated)
	}
}

func TestUnfinishedPagesRepairBlocksWhenDefaultHeadChanges(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"advanced"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutated := false
	defaultBranchExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	repo := Repository{Repository: "acme/app", ObservedDefault: "main", Desired: "main", OldHead: "planned", Disposition: "drift", PagesBefore: &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"}, PagesPhase: "unfinished"}
	result := engine.applyDefaultBranchPagesWithCheckpoint(context.Background(), repo, func(Repository) error { return nil })
	if result.Disposition != "error" || !strings.Contains(result.Error, "head changed") || mutated {
		t.Fatalf("result=%#v mutated=%t", result, mutated)
	}
}

func TestRunDefaultBranchBlocksUnsupportedUnfinishedPagesRepair(t *testing.T) {
	for name, test := range map[string]struct{ buildType, sourcePath, sourceBranch string }{
		"workflow Pages":   {"workflow", "/", "master"},
		"unsupported path": {"legacy", "/site", "master"},
		"foreign source":   {"legacy", "/", "release"},
	} {
		t.Run(name, func(t *testing.T) {
			originalConfigPath := ConfigPath
			t.Cleanup(func() { ConfigPath = originalConfigPath })
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			projectsRoot := t.TempDir()
			config := filepath.Join(t.TempDir(), "wb.yaml")
			if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ConfigPath = func() string { return config }
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app":
					return []byte(`{"id":1,"default_branch":"main"}`), nil
				case "repos/acme/app/branches/main":
					return []byte(`{"commit":{"sha":"same"}}`), nil
				case "repos/acme/app/pages":
					return []byte(`{"build_type":"` + test.buildType + `","source":{"branch":"` + test.sourceBranch + `","path":"` + test.sourcePath + `"}}`), nil
				default:
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			mutated := false
			defaultBranchExecute = func(context.Context, ...string) githubobserver.CommandResponse {
				mutated = true
				return githubobserver.CommandResponse{}
			}
			report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, MigratePagesSource: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if got := report.Repositories[0]; got.Disposition != "blocked" || !strings.Contains(got.Error, "outside the supported") || mutated {
				t.Fatalf("report=%#v mutated=%t", got, mutated)
			}
		})
	}
}

// defaultBranchPagesFixture returns the (Read, Execute) closure pair a
// caller assigns to its own local defaultBranchRead/defaultBranchExecute
// vars (already declared by its var (...) block above, and wired into its
// engine's GitHub fake through the trampoline closures there).
func defaultBranchPagesFixture(t *testing.T, sourcePath, buildType, initialSource string, changedBeforeWrite, postWriteMismatch bool) (
	func(ctx context.Context, endpoint string) ([]byte, error),
	func(ctx context.Context, args ...string) githubobserver.CommandResponse,
) {
	t.Helper()
	observedDefault, source, reads, mutations := "master", initialSource, 0, 0
	read := func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":1,"default_branch":"` + observedDefault + `"}`), nil
		case "repos/acme/app/branches/master", "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		case "repos/acme/app/branches/master/protection":
			return nil, errors.New("HTTP 404")
		case "repos/acme/app/rules/branches/master?per_page=100":
			return []byte(`[]`), nil
		case "repos/acme/app/pages":
			reads++
			if changedBeforeWrite && reads > 1 {
				return []byte(`{"build_type":"legacy","source":{"branch":"master","path":"/docs"}}`), nil
			}
			return []byte(`{"build_type":"` + buildType + `","source":{"branch":"` + source + `","path":"` + sourcePath + `"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	execute := func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		got := strings.Join(args, " ")
		if mutations == 1 && (got == "api --method POST repos/acme/app/branches/master/rename -f new_name=main" || got == "api --method PATCH repos/acme/app -f default_branch=main") {
			observedDefault = "main"
			return githubobserver.CommandResponse{}
		}
		if mutations == 2 && got == "api --method PUT repos/acme/app/pages -f source[branch]=main -f source[path]="+sourcePath {
			if !postWriteMismatch {
				source = "main"
			}
			return githubobserver.CommandResponse{}
		}
		return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + got)}
	}
	return read, execute
}

func TestInspectDefaultBranchRefusesArchivedAndDifferentTarget(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/old":
			return []byte(`{"default_branch":"master","archived":true}`), nil
		case "repos/acme/old/branches/master":
			return []byte(`{"commit":{"sha":"old"}}`), nil
		case "repos/acme/diverged":
			return []byte(`{"default_branch":"master"}`), nil
		case "repos/acme/diverged/branches/master":
			return []byte(`{"commit":{"sha":"old"}}`), nil
		case "repos/acme/diverged/branches/main":
			return []byte(`{"commit":{"sha":"new"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	archived := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/old"), "main", false, false)
	if archived.Disposition != "blocked" || !strings.Contains(archived.Error, "archived") {
		t.Fatalf("archived = %#v", archived)
	}
	diverged := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/diverged"), "main", false, false)
	if diverged.Disposition != "blocked" || !strings.Contains(diverged.Error, "different SHA") {
		t.Fatalf("diverged = %#v", diverged)
	}
}

func TestRunDefaultBranchSameSHAChangesOnlyDefaultAfterFreshProof(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	projectsRoot := t.TempDir()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return config }
	observedDefault := "master"
	mutations := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"` + observedDefault + `"}`), nil
		case "repos/acme/app/branches/master", "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
			return []byte(`[]`), nil
		case "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("gh: Not Found (HTTP 404)")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if strings.Join(args, " ") != "api --method PATCH repos/acme/app -f default_branch=main" {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		observedDefault = "main"
		return githubobserver.CommandResponse{}
	}
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 1 || report.Repositories[0].Disposition != "compliant" || report.Summary.Applied != 1 {
		t.Fatalf("report = %#v mutations=%d", report, mutations)
	}
	if report.Repositories[0].ObservedDefault != "master" || report.Repositories[0].VerifiedDefault != "main" {
		t.Fatalf("post-apply observation lost the planned source: %#v", report.Repositories[0])
	}
	persisted, err := os.ReadFile(report.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var persistedReport Report
	if err := json.Unmarshal(persisted, &persistedReport); err != nil {
		t.Fatal(err)
	}
	if persistedReport.Summary.Applied != 1 || persistedReport.Repositories[0].ObservedDefault != "master" {
		t.Fatalf("final persisted report = %#v", persistedReport)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"mode":"apply"`, `"desired":""`, `"inspected":1`, `"applied":1`, `"actions":[`} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("report JSON omits %s: %s", field, payload)
		}
	}
}

func TestApplyDefaultBranchRenamesAndProvesResult(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	observedDefault := "master"
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"` + observedDefault + `"}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"source"}}`), nil
		case "repos/acme/app/branches/main":
			if observedDefault == "master" {
				return nil, errors.New("HTTP 404")
			}
			return []byte(`{"commit":{"sha":"source"}}`), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		if got := strings.Join(args, " "); got != "api --method POST repos/acme/app/branches/master/rename -f new_name=main" {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + got)}
		}
		observedDefault = "main"
		return githubobserver.CommandResponse{}
	}
	planned := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, false)
	if planned.Disposition != "drift" || planned.OldHead != "source" {
		t.Fatalf("plan = %#v", planned)
	}
	result := engine.applyDefaultBranchWithCheckpoint(context.Background(), planned, nil)
	if result.Disposition != "compliant" || result.VerifiedDefault != "main" || result.NewHead != "source" {
		t.Fatalf("rename proof = %#v", result)
	}
	if got := strings.Join(result.Actions, "\n"); got != "renamed master to main\nverified default branch and head" {
		t.Fatalf("actions = %q", got)
	}
}

func TestApplyDefaultBranchWaitsForDelayedRenameVisibilityWithoutRetrying(t *testing.T) {
	var (
		defaultBranchRead       func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute    func(ctx context.Context, args ...string) githubobserver.CommandResponse
		defaultBranchRenameWait func(ctx context.Context, d time.Duration) error
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{waitFunc: func(ctx context.Context, d time.Duration) error { return defaultBranchRenameWait(ctx, d) }},
	}
	state, mutations, waits := 0, 0, 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			if state == 2 {
				return []byte(`{"default_branch":"main"}`), nil
			}
			return []byte(`{"default_branch":"master"}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"source"}}`), nil
		case "repos/acme/app/branches/main":
			if state == 0 {
				return nil, errors.New("HTTP 404")
			}
			return []byte(`{"commit":{"sha":"source"}}`), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if got := strings.Join(args, " "); got != "api --method POST repos/acme/app/branches/master/rename -f new_name=main" {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + got)}
		}
		state = 1
		return githubobserver.CommandResponse{}
	}
	defaultBranchRenameWait = func(context.Context, time.Duration) error {
		waits++
		state = 2
		return nil
	}
	planned := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, false)
	result := engine.applyDefaultBranchWithCheckpoint(context.Background(), planned, nil)
	if result.Disposition != "compliant" || result.VerifiedDefault != "main" || mutations != 1 || waits != 1 {
		t.Fatalf("delayed visibility result=%#v mutations=%d waits=%d", result, mutations, waits)
	}
}

func TestDefaultBranchRenameVisibilityTimeoutDoesNotSleepThroughItsDeadline(t *testing.T) {
	var (
		defaultBranchRenameNow  func() time.Time
		defaultBranchRenameWait func(ctx context.Context, d time.Duration) error
		defaultBranchRead       func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{nowFunc: func() time.Time { return defaultBranchRenameNow() }, waitFunc: func(ctx context.Context, d time.Duration) error { return defaultBranchRenameWait(ctx, d) }},
	}
	clock := time.Now()
	calls := 0
	defaultBranchRenameNow = func() time.Time {
		calls++
		if calls == 1 {
			return clock
		}
		return clock.Add(30 * time.Second)
	}
	waits := 0
	defaultBranchRenameWait = func(context.Context, time.Duration) error { waits++; return nil }
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"master"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"source"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	planned := Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "source"}
	result, err := engine.waitForDefaultBranchRename(context.Background(), planned)
	if err != nil || result.Disposition != "drift" || result.ObservedDefault != "master" || result.OldHead != "source" || waits != 0 {
		t.Fatalf("timeout result=%#v err=%v waits=%d", result, err, waits)
	}
}

func TestApplyDefaultBranchCheckpointsProtectBothMutationBoundaries(t *testing.T) {
	for name, failAfterResponse := range map[string]bool{"before response": false, "after response": true} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			mutated := false
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app":
					if mutated {
						return []byte(`{"default_branch":"main"}`), nil
					}
					return []byte(`{"default_branch":"master"}`), nil
				case "repos/acme/app/branches/master", "repos/acme/app/branches/main":
					if endpoint == "repos/acme/app/branches/main" && !mutated {
						return nil, errors.New("HTTP 404")
					}
					return []byte(`{"commit":{"sha":"source"}}`), nil
				case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
					return []byte(`[]`), nil
				default:
					if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
						return nil, errors.New("HTTP 404")
					}
					if strings.Contains(endpoint, "/rules/branches/") {
						return []byte(`[]`), nil
					}
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			executions := 0
			defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
				executions++
				mutated = true
				return githubobserver.CommandResponse{}
			}
			result := engine.applyDefaultBranchWithCheckpoint(context.Background(), Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "source"}, func(repository Repository) error {
				if repository.RenameAccepted == failAfterResponse {
					return errors.New("receipt unavailable")
				}
				return nil
			})
			if result.Disposition != "error" || !strings.Contains(result.Error, "persist") || (!failAfterResponse && executions != 0) || (failAfterResponse && executions != 1) {
				t.Fatalf("result=%#v executions=%d", result, executions)
			}
		})
	}
}

func TestReadDefaultBranchRenameVisibilityTreatsOnlyTransientTargetAbsenceAsPending(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	planned := Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "source"}
	for name, test := range map[string]struct {
		metadata    string
		metadataErr error
		target      []byte
		targetErr   error
		want        string
	}{
		"transient target absence": {metadata: `{"default_branch":"master"}`, want: "pending"},
		"target head changed":      {metadata: `{"default_branch":"main"}`, target: []byte(`{"commit":{"sha":"changed"}}`), want: "blocked"},
		"default changed":          {metadata: `{"default_branch":"trunk"}`, target: []byte(`{"commit":{"sha":"source"}}`), want: "blocked"},
		"metadata unavailable":     {metadataErr: errors.New("HTTP 503"), want: "error"},
		"metadata malformed":       {metadata: `{}`, want: "error"},
		"target read unavailable":  {metadata: `{"default_branch":"master"}`, targetErr: errors.New("HTTP 500"), want: "error"},
	} {
		t.Run(name, func(t *testing.T) {
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app":
					if test.metadataErr != nil {
						return nil, test.metadataErr
					}
					return []byte(test.metadata), nil
				case "repos/acme/app/branches/main":
					if test.targetErr != nil {
						return nil, test.targetErr
					}
					if test.target == nil {
						return nil, errors.New("HTTP 404")
					}
					return test.target, nil
				default:
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			if result := engine.readDefaultBranchRenameVisibility(context.Background(), planned, time.Now().Add(time.Second)); result.Disposition != test.want {
				t.Fatalf("visibility = %#v", result)
			}
		})
	}
}

func TestApplyDefaultBranchRefusesFreshPlanDrift(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"master"}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"advanced"}}`), nil
		case "repos/acme/app/branches/main":
			return nil, errors.New("HTTP 404")
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutated := false
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutated = true
		return githubobserver.CommandResponse{}
	}
	result := engine.applyDefaultBranchWithCheckpoint(context.Background(), Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "planned"}, nil)
	if result.Disposition != "blocked" || !strings.Contains(result.Error, "changed after planning") || mutated {
		t.Fatalf("fresh plan drift = %#v mutated=%t", result, mutated)
	}
}

func TestDefaultBranchSafetyFailsClosedForWorkflowAndRulesFailures(t *testing.T) {
	for name, configure := range map[string]func(string) ([]byte, error){
		"workflow source reference": func(endpoint string) ([]byte, error) {
			switch endpoint {
			case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
				return []byte(`[]`), nil
			case "repos/acme/app/contents/.github/workflows?ref=master":
				return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
			case "repos/acme/app/git/blobs/blob":
				return []byte(`{"content":"b25cbiAgcHVzaDpcbiAgICBicmFuY2hlczogW21hc3Rlcl0=","encoding":"base64"}`), nil
			default:
				return nil, errors.New("unexpected endpoint " + endpoint)
			}
		},
		"malformed effective rules": func(endpoint string) ([]byte, error) {
			switch endpoint {
			case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
				return []byte(`[]`), nil
			case "repos/acme/app/rules/branches/master?per_page=100":
				return []byte(`{}`), nil
			default:
				if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
					return nil, errors.New("HTTP 404")
				}
				return nil, errors.New("unexpected endpoint " + endpoint)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
			)
			engine := &Engine{
				Git:       &fakeGit{},
				GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) { return configure(endpoint) }
			result := Repository{Repository: "acme/app"}
			if err := engine.defaultBranchSafetyWithOptions(context.Background(), &result, defaultBranchRepoMetadata{}, "master", false); err == nil {
				t.Fatalf("unsafe %s was accepted", name)
			}
		})
	}
}

func TestDefaultBranchSafetyBlocksActiveBranchDependencies(t *testing.T) {
	for mode, want := range map[string]string{
		"open pull request":  "open pull request",
		"workflow encoding":  "unsupported content encoding",
		"classic protection": "pages, classic protection",
		"effective rules":    "pages, classic protection",
	} {
		t.Run(mode, func(t *testing.T) {
			var (
				defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
			)
			engine := &Engine{
				Git:       &fakeGit{},
				GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
					if mode == "open pull request" {
						return []byte(`[{"number":1}]`), nil
					}
					return []byte(`[]`), nil
				case "repos/acme/app/contents/.github/workflows?ref=master":
					if mode == "workflow encoding" {
						return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
					}
					return []byte(`[]`), nil
				case "repos/acme/app/git/blobs/blob":
					return []byte(`{"content":"master","encoding":"utf-8"}`), nil
				case "repos/acme/app/branches/master/protection":
					if mode == "classic protection" {
						return []byte(`{"required_status_checks":{}}`), nil
					}
					return nil, errors.New("HTTP 404")
				case "repos/acme/app/rules/branches/master?per_page=100":
					if mode == "effective rules" {
						return []byte(`[{"type":"pull_request"}]`), nil
					}
					return []byte(`[]`), nil
				default:
					if strings.HasSuffix(endpoint, "/pages") {
						return nil, errors.New("HTTP 404")
					}
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			result := Repository{Repository: "acme/app"}
			if err := engine.defaultBranchSafetyWithOptions(context.Background(), &result, defaultBranchRepoMetadata{}, "master", false); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("active dependency was accepted: %v", err)
			}
		})
	}
}

func TestInspectDefaultBranchPreservesTargetAndForkSafetyBoundaries(t *testing.T) {
	for name, test := range map[string]struct {
		metadata  string
		responses map[string][]byte
		want      string
	}{
		"pages after same SHA target": {
			metadata: `{"default_branch":"master"}`,
			responses: map[string][]byte{
				"repos/acme/app/branches/master":                       []byte(`{"commit":{"sha":"same"}}`),
				"repos/acme/app/branches/main":                         []byte(`{"commit":{"sha":"same"}}`),
				"repos/acme/app/pulls?state=open&head=acme%3Amaster":   []byte(`[]`),
				"repos/acme/app/contents/.github/workflows?ref=master": []byte(`[]`),
				"repos/acme/app/pages":                                 []byte(`{"source":{"branch":"master"}}`),
			},
			want: "pages, classic protection",
		},
		"fork lacks parent metadata": {
			metadata: `{"default_branch":"master","fork":true}`,
			responses: map[string][]byte{
				"repos/acme/app/branches/master": []byte(`{"commit":{"sha":"same"}}`),
			},
			want: "fork parent metadata is missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
			)
			engine := &Engine{
				Git:       &fakeGit{},
				GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				if endpoint == "repos/acme/app" {
					return []byte(test.metadata), nil
				}
				if value, ok := test.responses[endpoint]; ok {
					return value, nil
				}
				if endpoint == "repos/acme/app/branches/main" {
					return nil, errors.New("HTTP 404")
				}
				if strings.HasSuffix(endpoint, "/protection") {
					return nil, errors.New("HTTP 404")
				}
				if strings.Contains(endpoint, "/rules/branches/") {
					return []byte(`[]`), nil
				}
				return nil, errors.New("unexpected endpoint " + endpoint)
			}
			result := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, false)
			if result.Disposition != "blocked" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("unsafe repository state = %#v", result)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalRefusesStaleRemoteHead(t *testing.T) {
	var (
		defaultBranchGit func(ctx context.Context, dir string, args ...string) (string, error)
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	var calls []string
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H":
			return "", nil
		case "worktree list --porcelain":
			return "worktree /canonical\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "rev-parse origin/main":
			return "advanced", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repository := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: "/canonical", Disposition: "blocked"}
	if err := engine.reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "planned", func() error { return nil }); err == nil || !strings.Contains(err.Error(), "not planned source") {
		t.Fatalf("stale remote was accepted: %v", err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "branch -m") {
		t.Fatalf("stale clone was renamed: %v", calls)
	}
}

func TestReconcileDefaultBranchCanonicalPreservesUnsafeLocalStates(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "dirty", values: map[string]string{"status --porcelain": " M README.md"}, want: "local changes present"},
		{name: "linked worktree", values: map[string]string{"worktree list --porcelain": "worktree /canonical\nbranch refs/heads/master\nworktree /other\nbranch refs/heads/topic"}, want: "linked worktree exists"},
		{name: "different checkout", values: map[string]string{"branch --show-current": "topic"}, want: "only the old default"},
		{name: "destination exists", values: map[string]string{"for-each-ref --format=%(refname:strip=2) refs/heads": "master\nmain"}, want: "destination branch"},
		{name: "local source diverged", values: map[string]string{"rev-parse master": "different"}, want: "not contained in origin/main"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				defaultBranchGit        func(ctx context.Context, dir string, args ...string) (string, error)
				defaultBranchIsAncestor func(ctx context.Context, dir, ancestor, descendant string) (bool, error)
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}, isAncestorFunc: func(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
					return defaultBranchIsAncestor(ctx, dir, ancestor, descendant)
				}},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchIsAncestor = func(_ context.Context, _ string, _, _ string) (bool, error) { return false, nil }
			var calls []string
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				call := strings.Join(args, " ")
				calls = append(calls, call)
				if value, ok := test.values[call]; ok {
					return value, nil
				}
				switch call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log origin/main..master --not --remotes --format=%H":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return "master", nil
				case "rev-parse origin/main", "rev-parse master", "rev-parse main", "rev-parse HEAD":
					return "same", nil
				case "for-each-ref --format=%(refname:strip=2) refs/heads":
					return "master", nil
				default:
					return "", errors.New("unexpected mutation " + call)
				}
			}
			repository := Repository{Repository: "acme/app", Desired: "main"}
			entry := Canonical{Path: "/canonical", Disposition: "blocked"}
			err := engine.reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error { return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe state result = %v", err)
			}
			if strings.Contains(strings.Join(calls, "\n"), "branch -m") {
				t.Fatalf("unsafe clone was renamed: %v", calls)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalRecordsRenameAndTrackingOutcomes(t *testing.T) {
	for name, upstreamError := range map[string]bool{"rename and track": false, "tracking failure": true} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
				defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
				defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
					return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
				}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
					return defaultBranchAttachHead(ctx, dir, destination)
				}},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { return nil }
			defaultBranchAttachHead = func(_ context.Context, _, _ string) error { return nil }
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				switch call := strings.Join(args, " "); call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H", "branch -m master main":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return "master", nil
				case "rev-parse origin/main", "rev-parse master", "rev-parse main", "rev-parse HEAD":
					return "same", nil
				case "for-each-ref --format=%(refname:strip=2) refs/heads":
					return "master", nil
				case "branch --set-upstream-to=origin/main main":
					if upstreamError {
						return "", errors.New("upstream rejected")
					}
					return "", nil
				default:
					return "", errors.New("unexpected git " + call)
				}
			}
			repository := Repository{Repository: "acme/app", Desired: "main"}
			entry := Canonical{Path: "/canonical", Disposition: "blocked"}
			checkpoints := 0
			err := engine.reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error { checkpoints++; return nil })
			if upstreamError {
				if err == nil || entry.Disposition != "error" || len(entry.Actions) != 1 || checkpoints != 3 {
					t.Fatalf("partial reconciliation = %#v checkpoints=%d err=%v", entry, checkpoints, err)
				}
				return
			}
			if err != nil || entry.Disposition != "compliant" || len(entry.Actions) != 2 || checkpoints != 3 {
				t.Fatalf("reconciliation = %#v checkpoints=%d err=%v", entry, checkpoints, err)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalFastForwardsOnlyContainedSource(t *testing.T) {
	var (
		defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
		defaultBranchIsAncestor       func(ctx context.Context, dir, ancestor, descendant string) (bool, error)
		defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
		defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}, isAncestorFunc: func(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
			return defaultBranchIsAncestor(ctx, dir, ancestor, descendant)
		}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
			return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
		}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
			return defaultBranchAttachHead(ctx, dir, destination)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { return nil }
	defaultBranchAttachHead = func(_ context.Context, _, _ string) error { return nil }
	localHead, remoteHead := "old", "new"
	var calls []string
	defaultBranchIsAncestor = func(_ context.Context, _ string, ancestor, descendant string) (bool, error) {
		if ancestor != "master" || descendant != "origin/main" {
			t.Fatalf("ancestry check = %s %s", ancestor, descendant)
		}
		return true, nil
	}
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "branch -m master main", "branch --set-upstream-to=origin/main main":
			return "", nil
		case "worktree list --porcelain":
			return "worktree /canonical\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		case "rev-parse master", "rev-parse main", "rev-parse HEAD":
			return localHead, nil
		case "rev-parse origin/main":
			return remoteHead, nil
		case "merge --ff-only origin/main":
			if localHead != "old" {
				return "", errors.New("fast-forward was repeated")
			}
			localHead = remoteHead
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repo := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: "/canonical", Disposition: "blocked"}
	var receipts []Canonical
	if err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "new", func() error {
		receipts = append(receipts, entry)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if entry.Disposition != "compliant" || strings.Join(entry.Actions, " | ") != "fast-forwarded local master to origin/main | renamed local master to main | set upstream to origin/main" {
		t.Fatalf("entry = %#v", entry)
	}
	if len(receipts) != 5 || !strings.Contains(strings.Join(receipts[0].Actions, " "), "old") || !strings.Contains(strings.Join(receipts[0].Actions, " "), "new") || !strings.Contains(strings.Join(receipts[1].Actions, " "), "fast-forwarded") || !strings.Contains(strings.Join(receipts[2].Actions, " "), "planned rename") || !strings.Contains(strings.Join(receipts[3].Actions, " "), "HEAD detached") {
		t.Fatalf("receipts = %#v", receipts)
	}
	if strings.Contains(strings.Join(calls, "\n"), "log --branches --not --remotes") || !strings.Contains(strings.Join(calls, "\n"), "merge --ff-only origin/main") {
		t.Fatalf("calls = %v", calls)
	}
}

func TestReconcileDefaultBranchCanonicalSeparatesUnpublishedAndKnownRemoteDivergence(t *testing.T) {
	for name, unpublished := range map[string]string{
		"unpublished":      "local-only",
		"known divergence": "",
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchGit        func(ctx context.Context, dir string, args ...string) (string, error)
				defaultBranchIsAncestor func(ctx context.Context, dir, ancestor, descendant string) (bool, error)
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}, isAncestorFunc: func(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
					return defaultBranchIsAncestor(ctx, dir, ancestor, descendant)
				}},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			var calls []string
			defaultBranchIsAncestor = func(_ context.Context, _ string, _, _ string) (bool, error) { return false, nil }
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				call := strings.Join(args, " ")
				calls = append(calls, call)
				switch call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return "master", nil
				case "for-each-ref --format=%(refname:strip=2) refs/heads":
					return "master", nil
				case "rev-parse master":
					return "local", nil
				case "rev-parse origin/main":
					return "remote", nil
				case "log origin/main..master --not --remotes --format=%H":
					return unpublished, nil
				default:
					return "", errors.New("unexpected git " + call)
				}
			}
			repo := Repository{Repository: "acme/app", Desired: "main"}
			entry := Canonical{Path: "/canonical", Disposition: "blocked"}
			err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "remote", func() error { return nil })
			if err == nil || !strings.Contains(err.Error(), map[bool]string{true: "has unpublished commits", false: "not contained"}[unpublished != ""]) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(strings.Join(calls, "\n"), "merge --ff-only") || strings.Contains(strings.Join(calls, "\n"), "branch -m") {
				t.Fatalf("diverged clone mutated: %v", calls)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalReportsSourceClassificationFailures(t *testing.T) {
	for name, test := range map[string]struct{ ancestryErr, logErr, want string }{
		"ancestry":          {ancestryErr: "merge-base unavailable", want: "classify local master"},
		"unpublished query": {logErr: "log unavailable", want: "inspect local master unpublished"},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchGit        func(ctx context.Context, dir string, args ...string) (string, error)
				defaultBranchIsAncestor func(ctx context.Context, dir, ancestor, descendant string) (bool, error)
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}, isAncestorFunc: func(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
					return defaultBranchIsAncestor(ctx, dir, ancestor, descendant)
				}},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchIsAncestor = func(_ context.Context, _ string, _, _ string) (bool, error) {
				return false, errors.New(test.ancestryErr)
			}
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				switch call := strings.Join(args, " "); call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return "master", nil
				case "for-each-ref --format=%(refname:strip=2) refs/heads":
					return "master", nil
				case "rev-parse origin/main":
					return "remote", nil
				case "rev-parse master":
					return "local", nil
				case "log origin/main..master --not --remotes --format=%H":
					return "", errors.New(test.logErr)
				default:
					return "", errors.New("unexpected git " + call)
				}
			}
			if test.ancestryErr == "" {
				defaultBranchIsAncestor = func(_ context.Context, _ string, _, _ string) (bool, error) { return false, nil }
			}
			repo := Repository{Repository: "acme/app", Desired: "main"}
			err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &Canonical{Path: "/canonical", Disposition: "blocked"}, "master", "remote", func() error { return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalRefusesRefMovementBeforeRename(t *testing.T) {
	var (
		defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
		defaultBranchIsAncestor       func(ctx context.Context, dir, ancestor, descendant string) (bool, error)
		defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
		defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}, isAncestorFunc: func(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
			return defaultBranchIsAncestor(ctx, dir, ancestor, descendant)
		}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
			return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
		}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
			return defaultBranchAttachHead(ctx, dir, destination)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	atomicRenameCalls := 0
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, source, destination, expected string) error {
		atomicRenameCalls++
		if source != "master" || destination != "main" || expected != "new" {
			t.Fatalf("atomic rename = %s %s %s", source, destination, expected)
		}
		return errors.New("cannot lock ref 'refs/heads/master': is at moved but expected new")
	}
	localHead, remoteHead := "old", "new"
	defaultBranchIsAncestor = func(_ context.Context, _ string, _, _ string) (bool, error) { return true, nil }
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch call := strings.Join(args, " "); call {
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain":
			return "", nil
		case "worktree list --porcelain":
			return "worktree /canonical\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		case "rev-parse master":
			return localHead, nil
		case "rev-parse origin/main":
			return remoteHead, nil
		case "merge --ff-only origin/main":
			localHead = "new"
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repo := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: "/canonical", Disposition: "blocked"}
	err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "new", func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "expected new") || atomicRenameCalls != 1 {
		t.Fatalf("moved refs were accepted: %v", err)
	}
}

func TestReconcileDefaultBranchCanonicalRecoversDetachedAtomicRename(t *testing.T) {
	var (
		defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
		defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
		defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
		defaultBranchRefExists        func(ctx context.Context, dir, ref string) (bool, error)
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
			return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
		}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
			return defaultBranchAttachHead(ctx, dir, destination)
		}, refExistsFunc: func(ctx context.Context, dir, ref string) (bool, error) { return defaultBranchRefExists(ctx, dir, ref) }},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	current, sourceExists := "master", true
	attachFails := true
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { current, sourceExists = "", false; return nil }
	defaultBranchAttachHead = func(_ context.Context, _, _ string) error {
		if attachFails {
			return errors.New("attach interrupted")
		}
		current = "main"
		return nil
	}
	defaultBranchRefExists = func(_ context.Context, _ string, ref string) (bool, error) {
		return (ref == "refs/heads/master" && sourceExists) || (ref == "refs/heads/main" && !sourceExists), nil
	}
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch call := strings.Join(args, " "); call {
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "branch --set-upstream-to=origin/main main":
			return "", nil
		case "worktree list --porcelain":
			return "worktree /canonical\nbranch refs/heads/master", nil
		case "branch --show-current":
			return current, nil
		case "rev-parse origin/main", "rev-parse master", "rev-parse main", "rev-parse HEAD":
			return "same", nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repo := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: "/canonical", Disposition: "blocked"}
	var receipts []Canonical
	checkpoint := func() error { receipts = append(receipts, entry); return nil }
	err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "same", checkpoint)
	if err == nil || !strings.Contains(err.Error(), "attach HEAD") || len(receipts) < 2 || !strings.Contains(strings.Join(receipts[len(receipts)-1].Actions, " "), "HEAD detached") {
		t.Fatalf("attach interruption = %v receipts=%#v", err, receipts)
	}
	attachFails = false
	entry = Canonical{Path: "/canonical", Disposition: "blocked"}
	if err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "same", checkpoint); err != nil || entry.Disposition != "compliant" || !strings.Contains(strings.Join(entry.Actions, " "), "recovered detached HEAD") {
		t.Fatalf("detached retry = %#v err=%v", entry, err)
	}
}

func TestReconcileDefaultBranchCanonicalRestoresDetachedFailedAtomicRename(t *testing.T) {
	for name, sourceHead := range map[string]string{"restore": "same", "moved source refuses": "moved"} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchGit        func(ctx context.Context, dir string, args ...string) (string, error)
				defaultBranchAttachHead func(ctx context.Context, dir, destination string) error
				defaultBranchRefExists  func(ctx context.Context, dir, ref string) (bool, error)
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
					return defaultBranchAttachHead(ctx, dir, destination)
				}, refExistsFunc: func(ctx context.Context, dir, ref string) (bool, error) { return defaultBranchRefExists(ctx, dir, ref) }},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			current, attached := "", false
			defaultBranchAttachHead = func(_ context.Context, _, destination string) error {
				attached = true
				current = destination
				return nil
			}
			defaultBranchRefExists = func(_ context.Context, _ string, ref string) (bool, error) { return ref == "refs/heads/master", nil }
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				switch call := strings.Join(args, " "); call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return current, nil
				case "rev-parse origin/main", "rev-parse HEAD":
					return "same", nil
				case "rev-parse master":
					return sourceHead, nil
				default:
					return "", errors.New("unexpected git " + call)
				}
			}
			repo := Repository{Repository: "acme/app", Desired: "main"}
			entry := Canonical{Path: "/canonical", Disposition: "blocked"}
			checkpoints := 0
			err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "same", func() error { checkpoints++; return nil })
			if sourceHead == "same" {
				if err != nil || !attached || current != "master" || checkpoints != 2 || !strings.Contains(strings.Join(entry.Actions, " "), "restored HEAD") {
					t.Fatalf("restoration = %#v err=%v checkpoints=%d", entry, err, checkpoints)
				}
			} else if err == nil || attached {
				t.Fatalf("moved source accepted: err=%v attached=%t", err, attached)
			}
		})
	}
}

func TestVerifyDefaultBranchAttachmentRefusesMovementDuringAttach(t *testing.T) {
	var (
		defaultBranchGit func(ctx context.Context, dir string, args ...string) (string, error)
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "rev-parse HEAD":
			return "moved", nil
		case "rev-parse main", "rev-parse master", "rev-parse origin/main":
			return "same", nil
		case "status --porcelain":
			return "", nil
		default:
			return "", errors.New("unexpected git")
		}
	}
	if err := engine.verifyDefaultBranchAttachment(context.Background(), "/canonical", "main", "main", "same"); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("moved attachment accepted: %v", err)
	}
	if err := engine.verifyDefaultBranchAttachment(context.Background(), "/canonical", "master", "main", "same"); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("moved master attachment accepted: %v", err)
	}
}

func TestVerifyDefaultBranchAttachmentReportsReadFailures(t *testing.T) {
	for _, failed := range []string{"rev-parse HEAD", "rev-parse main", "rev-parse origin/main", "status --porcelain"} {
		t.Run(failed, func(t *testing.T) {
			var (
				defaultBranchGit func(ctx context.Context, dir string, args ...string) (string, error)
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				call := strings.Join(args, " ")
				if call == failed {
					return "", errors.New("read failed")
				}
				return "same", nil
			}
			if err := engine.verifyDefaultBranchAttachment(context.Background(), "/canonical", "main", "main", "same"); err == nil || !strings.Contains(err.Error(), "read failed") {
				t.Fatalf("%s was accepted: %v", failed, err)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalBlocksMovementDuringNormalAttach(t *testing.T) {
	var (
		defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
		defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
		defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
			return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
		}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
			return defaultBranchAttachHead(ctx, dir, destination)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	moved, upstream := false, false
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { return nil }
	defaultBranchAttachHead = func(_ context.Context, _, _ string) error { moved = true; return nil }
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch call := strings.Join(args, " "); call {
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain":
			return "", nil
		case "worktree list --porcelain":
			return "worktree /canonical\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		case "rev-parse origin/main", "rev-parse master":
			return "same", nil
		case "rev-parse main", "rev-parse HEAD":
			if moved {
				return "moved", nil
			}
			return "same", nil
		case "branch --set-upstream-to=origin/main main":
			upstream = true
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repo := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: "/canonical", Disposition: "blocked"}
	var receipts []Canonical
	err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "same", func() error { receipts = append(receipts, entry); return nil })
	if err == nil || entry.Disposition != "blocked" || upstream || !strings.Contains(strings.Join(entry.Actions, " "), "attachment verification failed") || !strings.Contains(strings.Join(receipts[len(receipts)-1].Actions, " "), "attachment verification failed") {
		t.Fatalf("attach movement = %#v err=%v upstream=%t receipts=%#v", entry, err, upstream, receipts)
	}
}

func TestReconcileDefaultBranchCanonicalFailsClosedOnGitAndReceiptErrors(t *testing.T) {
	tests := []struct {
		name           string
		failGitCall    string
		failCheckpoint int
		want           string
	}{
		{name: "fetch", failGitCall: "fetch --prune origin", want: "refresh origin before"},
		{name: "remote head", failGitCall: "remote set-head origin --auto", want: "refresh origin/HEAD"},
		{name: "status", failGitCall: "status --porcelain", want: "inspect local changes"},
		{name: "worktree inspection", failGitCall: "worktree list --porcelain", want: "inspect linked worktrees"},
		{name: "current branch", failGitCall: "branch --show-current", want: "inspect checked-out"},
		{name: "remote ref", failGitCall: "rev-parse origin/main", want: "resolve refreshed"},
		{name: "branch inventory", failGitCall: "for-each-ref --format=%(refname:strip=2) refs/heads", want: "inspect local branch names"},
		{name: "local ref", failGitCall: "rev-parse master", want: "resolve local master"},
		{name: "durable plan", failCheckpoint: 1, want: "persist local-reconciliation plan"},
		{name: "atomic receipt", failCheckpoint: 2, want: "persist atomic local rename receipt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
				defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
				defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
			)
			engine := &Engine{
				Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					return defaultBranchGit(ctx, dir, args...)
				}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
					return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
				}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
					return defaultBranchAttachHead(ctx, dir, destination)
				}},
				GitHub:    &fakeGitHub{},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { return nil }
			defaultBranchAttachHead = func(_ context.Context, _, _ string) error { return nil }
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				call := strings.Join(args, " ")
				if call == test.failGitCall {
					return "", errors.New("forced git failure")
				}
				switch call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H", "branch -m master main", "branch --set-upstream-to=origin/main main":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return "master", nil
				case "rev-parse origin/main", "rev-parse master":
					return "same", nil
				case "for-each-ref --format=%(refname:strip=2) refs/heads":
					return "master", nil
				default:
					return "", errors.New("unexpected git " + call)
				}
			}
			checkpoints := 0
			repository := Repository{Repository: "acme/app", Desired: "main"}
			entry := Canonical{Path: "/canonical", Disposition: "blocked"}
			err := engine.reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error {
				checkpoints++
				if checkpoints == test.failCheckpoint {
					return errors.New("receipt unavailable")
				}
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("failure was not preserved: %v", err)
			}
		})
	}
}

func TestApplyDefaultBranchFailsClosedForInvalidAndUnprovenOutcomes(t *testing.T) {
	engine := &Engine{Git: &fakeGit{}, GitHub: &fakeGitHub{}, Discovery: &fakeDiscovery{}, Clock: &fakeClock{}}
	if result := engine.applyDefaultBranchWithCheckpoint(context.Background(), Repository{Repository: "not-a-slug"}, nil); result.Disposition != "error" || !strings.Contains(result.Error, "invalid repository") {
		t.Fatalf("invalid repository = %#v", result)
	}
	for name, test := range map[string]struct {
		mutate func(*string)
		want   string
	}{
		"mutation rejected":  {want: "mutation rejected"},
		"post proof differs": {mutate: func(observed *string) { *observed = "main" }, want: "branch head changed while waiting"},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			observedDefault := "master"
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app":
					return []byte(`{"default_branch":"` + observedDefault + `"}`), nil
				case "repos/acme/app/branches/master":
					return []byte(`{"commit":{"sha":"planned"}}`), nil
				case "repos/acme/app/branches/main":
					if observedDefault == "master" {
						return nil, errors.New("HTTP 404")
					}
					return []byte(`{"commit":{"sha":"changed"}}`), nil
				case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
					return []byte(`[]`), nil
				default:
					if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
						return nil, errors.New("HTTP 404")
					}
					if strings.Contains(endpoint, "/rules/branches/") {
						return []byte(`[]`), nil
					}
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
				if test.mutate == nil {
					return githubobserver.CommandResponse{Err: errors.New("mutation rejected")}
				}
				test.mutate(&observedDefault)
				return githubobserver.CommandResponse{}
			}
			result := engine.applyDefaultBranchWithCheckpoint(context.Background(), Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "planned"}, nil)
			if result.Disposition != "error" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("unproven mutation = %#v", result)
			}
		})
	}
}

func TestDefaultBranchReportPathAndValidationGuardrails(t *testing.T) {
	projectsRoot := t.TempDir()
	path, err := defaultBranchReportPath(projectsRoot, "")
	if err != nil || !strings.Contains(path, "reports/default-branch/default-branch-") {
		t.Fatalf("default report path = %q err=%v", path, err)
	}
	if err := persistDefaultBranchReport(Report{ReportPath: filepath.Join(t.TempDir(), "missing", "report.json")}); err == nil {
		t.Fatal("persist accepted a missing report directory")
	}
	for _, branch := range []string{"", "@", "-main", "main/", "main..old", "main@{old}", "main lock", "main/.lock", "main\x00"} {
		if validDefaultBranch(branch) {
			t.Fatalf("invalid branch was accepted: %q", branch)
		}
	}
	if !validDefaultBranch("release/2026.09") {
		t.Fatal("valid branch was rejected")
	}
}

func TestInspectDefaultBranchFailsClosedOnUntrustedObservations(t *testing.T) {
	engine := &Engine{Git: &fakeGit{}, GitHub: &fakeGitHub{}, Discovery: &fakeDiscovery{}, Clock: &fakeClock{}}
	tests := []struct {
		name string
		want string
		read func(string) ([]byte, error)
	}{
		{name: "repository read", want: "transport failed", read: func(string) ([]byte, error) { return nil, errors.New("transport failed") }},
		{name: "malformed metadata", want: "decode repository metadata", read: func(string) ([]byte, error) { return []byte(`{`), nil }},
		{name: "invalid remote branch", want: "invalid default branch", read: func(string) ([]byte, error) { return []byte(`{"default_branch":"../master"}`), nil }},
		{name: "malformed source ref", want: "decode branch master", read: func(endpoint string) ([]byte, error) {
			if endpoint == "repos/acme/app" {
				return []byte(`{"default_branch":"master"}`), nil
			}
			return []byte(`{"commit":{}}`), nil
		}},
		{name: "target read failure", want: "HTTP 500", read: func(endpoint string) ([]byte, error) {
			switch endpoint {
			case "repos/acme/app":
				return []byte(`{"default_branch":"master"}`), nil
			case "repos/acme/app/branches/master":
				return []byte(`{"commit":{"sha":"source"}}`), nil
			default:
				return nil, errors.New("HTTP 500")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
			)
			engine := &Engine{
				Git:       &fakeGit{},
				GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) { return test.read(endpoint) }
			result := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, false)
			if result.Disposition != "error" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("untrusted observation = %#v", result)
			}
		})
	}
	if result := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "bad ref", false, false); result.Disposition != "blocked" || !strings.Contains(result.Error, "invalid desired branch") {
		t.Fatalf("invalid desired branch = %#v", result)
	}
}

func TestDefaultBranchReportPersistsAndPrintsCloneFindings(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := Report{ReportPath: path, Desired: "main", Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", Disposition: "blocked", Error: "workflow review required",
		CanonicalClones: []Canonical{{Path: "/projects/acme/app", Disposition: "blocked", Error: "local changes present"}},
	}}}
	summarizeDefaultBranch(&report)
	if err := persistDefaultBranchReport(report); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(persisted), "workflow review required") {
		t.Fatalf("persisted report = %q err=%v", persisted, err)
	}
	var output bytes.Buffer
	if err := Print(&output, report); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "acme/app: blocked") || !strings.Contains(got, "/projects/acme/app: blocked") {
		t.Fatalf("printed report = %q", got)
	}
	for failAt := 1; failAt <= 5; failAt++ {
		writer := &defaultBranchFailWriter{failAt: failAt}
		if err := Print(writer, report); err == nil {
			t.Fatalf("output failure at write %d was ignored", failAt)
		}
	}
}

// The following tests exercise persistDefaultBranchReportInjected's
// filewrite.Injector-reachable error branches (task-9 PR-2): the happy
// path is already covered above, but reaching a create, chmod, write,
// sync, close, rename, or directory-sync failure deterministically needs
// the injector.

func TestPersistDefaultBranchReportInjectedHonoursAnInjectedChmodFailure(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomForCmdWB}
	if err := persistDefaultBranchReportInjected(Report{ReportPath: path}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("persistDefaultBranchReportInjected error = %v", err)
	}
}

func TestPersistDefaultBranchReportInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForCmdWB}
	if err := persistDefaultBranchReportInjected(Report{ReportPath: path}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("persistDefaultBranchReportInjected error = %v", err)
	}
}

func TestPersistDefaultBranchReportInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomForCmdWB}
	if err := persistDefaultBranchReportInjected(Report{ReportPath: path}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("persistDefaultBranchReportInjected error = %v", err)
	}
}

func TestPersistDefaultBranchReportInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForCmdWB}
	if err := persistDefaultBranchReportInjected(Report{ReportPath: path}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("persistDefaultBranchReportInjected error = %v", err)
	}
	assertNoLeftoverDefaultBranchReportTempFile(t, filepath.Dir(path))
}

func TestPersistDefaultBranchReportInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomForCmdWB}
	if err := persistDefaultBranchReportInjected(Report{ReportPath: path}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("persistDefaultBranchReportInjected error = %v", err)
	}
	assertNoLeftoverDefaultBranchReportTempFile(t, filepath.Dir(path))
}

// assertNoLeftoverDefaultBranchReportTempFile asserts
// persistDefaultBranchReportInjected's defer os.Remove(temporaryName) ran:
// no ".default-branch-*.json" staging file survives a close or rename
// failure (task-9 PR-2 review, B2 mutation evidence: deleting that defer
// survived every test that only asserted the returned error).
func assertNoLeftoverDefaultBranchReportTempFile(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".default-branch-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover default branch report temp file(s) after failure: %v", matches)
	}
}

func TestPersistDefaultBranchReportInjectedHonoursAnInjectedDirSyncFailure(t *testing.T) {
	path, err := defaultBranchReportPath("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: errBoomForCmdWB}
	if err := persistDefaultBranchReportInjected(Report{ReportPath: path}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("persistDefaultBranchReportInjected error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report was not published before the injected directory sync failure: %v", err)
	}
}

func TestWorkflowReferencesDefaultBranchCoversQuotedAndMultilineReferences(t *testing.T) {
	for name, contents := range map[string]string{
		"inline second branch":    "on:\n  push:\n    branches: [dev, master]\n",
		"multiline second branch": "on:\n  push:\n    branches:\n      - dev\n      - 'master'\n",
		"ref scalar":              "with:\n  ref: \"master\"\n",
		"raw ref URL":             "curl https://github.example/repo/raw/refs/heads/master/file\n",
		"action ref":              "uses: acme/action@master\n",
	} {
		t.Run(name, func(t *testing.T) {
			if !workflowReferencesDefaultBranch(contents, "master") {
				t.Fatalf("reference was missed: %q", contents)
			}
		})
	}
	if workflowReferencesDefaultBranch("branches: [main]\n", "master") {
		t.Fatal("unrelated branch was reported")
	}
	if workflowReferencesRepositoryDefaultBranch("run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n", "master", "sneat-co/mockingo") {
		t.Fatal("external raw GitHub content URL was reported")
	}
	if !workflowReferencesRepositoryDefaultBranch("run: curl https://raw.githubusercontent.com/sneat-co/mockingo/master/install.sh\n", "master", "sneat-co/mockingo") {
		t.Fatal("same-repository raw GitHub content URL was missed")
	}
}

func TestRewriteWorkflowBranchTriggersIsNarrowAndBytePreserving(t *testing.T) {
	before := "# keep this comment\non:\n  push:\n    branches:\n      - master\n      - release\n  pull_request:\n    branches:\n      - master\nname: unchanged\n"
	after, changed := rewriteWorkflowBranchTriggers(before, "master", "main")
	want := "# keep this comment\non:\n  push:\n    branches:\n      - main\n      - release\n  pull_request:\n    branches:\n      - main\nname: unchanged\n"
	if !changed || after != want {
		t.Fatalf("changed=%t\\nafter=%q\\nwant=%q", changed, after, want)
	}
	crlf := "on:\r\n  push:\r\n    branches:\r\n      - master\r\n"
	if got, changed := rewriteWorkflowBranchTriggers(crlf, "master", "main"); !changed || got != "on:\r\n  push:\r\n    branches:\r\n      - main\r\n" {
		t.Fatalf("CRLF rewrite changed line endings: %q", got)
	}
	flowBefore := "on:\n  push:\n    branches: [ release, master ]\n  pull_request:\n    branches: [master]\n"
	flowAfter := "on:\n  push:\n    branches: [ release, main ]\n  pull_request:\n    branches: [main]\n"
	if got, changed := rewriteWorkflowBranchTriggers(flowBefore, "master", "main"); !changed || got != flowAfter {
		t.Fatalf("flow-list rewrite changed=%t\ngot=%q\nwant=%q", changed, got, flowAfter)
	}
	duplicates := "on:\n  push:\n    branches: [master, master]\n"
	if got, changed := rewriteWorkflowBranchTriggers(duplicates, "master", "main"); !changed || got != "on:\n  push:\n    branches: [main, main]\n" {
		t.Fatalf("duplicate flow-list rewrite changed=%t got=%q", changed, got)
	}
	for _, unsupported := range []string{
		"on:\n  push:\n    branches: [master] # comment\n",
		"on:\n  push:\n    branches: ['master']\n",
		"on:\n  push:\n    branches: [${{ github.ref }}]\n",
		"on:\n  push:\n    branches: [&old master]\n",
		"on:\n  push:\n    branches: [master, \"quoted\"]\n",
		"on:\n  push:\n    branches: [true, master]\n",
		"on:\n  push:\n    branches: [123, master]\n",
		"on:\n  push:\n    filters: &f\n      branches: [master]\n",
		"on:\n  push:\n    branches:\n      - 'master'\n",
		"uses: acme/action@master\n",
		"ref: master\n",
		"# master\n",
		"url: https://example.invalid/master\n",
		"jobs:\n  build:\n    steps:\n      - run: |\n          on:\n            push:\n              branches:\n                - master\n",
	} {
		if got, changed := rewriteWorkflowBranchTriggers(unsupported, "master", "main"); changed || got != unsupported {
			t.Fatalf("unsupported shape was rewritten: %q => %q", unsupported, got)
		}
	}
}

func TestApplyDefaultBranchWorkflowTriggersUsesOneCASCommitAndPostRead(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	old, next := strings.Repeat("a", 40), strings.Repeat("b", 40)
	workflow, rewritten := "on:\n  push:\n    branches:\n      - master\n", "on:\n  push:\n    branches:\n      - main\n"
	head, committed, mutations := old, false, 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte("{\"id\":1,\"default_branch\":\"master\"}"), nil
		case "repos/acme/app/branches/master":
			return []byte("{\"commit\":{\"sha\":\"" + head + "\"}}"), nil
		case "repos/acme/app/commits/" + next:
			return []byte("{\"parents\":[{\"sha\":\"" + old + "\"}]}"), nil
		case "repos/acme/app/branches/main", "repos/acme/app/pages", "repos/acme/app/branches/master/protection":
			return nil, errors.New("HTTP 404")
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
			return []byte("[]"), nil
		case "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte("[{\"path\":\".github/workflows/ci.yml\",\"sha\":\"blob\",\"type\":\"file\"}]"), nil
		case "repos/acme/app/git/blobs/blob":
			contents := workflow
			if committed {
				contents = rewritten
			}
			return []byte("{\"encoding\":\"base64\",\"content\":\"" + base64.StdEncoding.EncodeToString([]byte(contents)) + "\"}"), nil
		case "repos/acme/app/rules/branches/master?per_page=100":
			return []byte("[]"), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		joined := strings.Join(args, "\n")
		for _, required := range []string{"api\ngraphql", "expected=" + old, "branch[repositoryNameWithOwner]=acme/app", "branch[branchName]=master", "additions[][path]=.github/workflows/ci.yml", "additions[][contents]=" + base64.StdEncoding.EncodeToString([]byte(rewritten))} {
			if !strings.Contains(joined, required) {
				return githubobserver.CommandResponse{Err: errors.New("missing GraphQL argument " + required)}
			}
		}
		head, committed = next, true
		return githubobserver.CommandResponse{Stdout: []byte("{\"data\":{\"createCommitOnBranch\":{\"commit\":{\"oid\":\"" + next + "\"}}}}")}
	}
	planned := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, false, true)
	if planned.Disposition != "drift" || len(planned.WorkflowFiles) != 1 {
		t.Fatalf("planned=%#v", planned)
	}
	got := engine.applyDefaultBranchWorkflowTriggers(context.Background(), planned, func(Repository) error { return nil })
	if mutations != 1 || got.Disposition != "drift" || got.WorkflowPhase != "verified" || got.WorkflowCommit != next || got.OldHead != next {
		t.Fatalf("got=%#v mutations=%d", got, mutations)
	}
}

func TestRewriteWorkflowTriggersIsNoopWhenDefaultAlreadyMatches(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte("{\"id\":1,\"default_branch\":\"main\"}"), nil
		case "repos/acme/app/branches/main":
			return []byte("{\"commit\":{\"sha\":\"same\"}}"), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	got := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/app"), "main", false, false, true)
	if got.Disposition != "compliant" || got.Error != "" {
		t.Fatalf("already-main workflow rewrite = %#v", got)
	}
}

func TestRunDefaultBranchRewritesWorkflowThenRenames(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead       func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute    func(ctx context.Context, args ...string) githubobserver.CommandResponse
		defaultBranchRenameWait func(ctx context.Context, d time.Duration) error
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{waitFunc: func(ctx context.Context, d time.Duration) error { return defaultBranchRenameWait(ctx, d) }},
	}
	projectsRoot := t.TempDir()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return config }
	defaultBranchRenameWait = func(context.Context, time.Duration) error { return nil }
	old, workflowHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	defaultName, head, workflowDone, renames := "master", old, false, 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte("{\"id\":1,\"default_branch\":\"" + defaultName + "\"}"), nil
		case "repos/acme/app/branches/master":
			if defaultName == "main" {
				return nil, errors.New("HTTP 404")
			}
			return []byte("{\"commit\":{\"sha\":\"" + head + "\"}}"), nil
		case "repos/acme/app/branches/main":
			if defaultName != "main" {
				return nil, errors.New("HTTP 404")
			}
			return []byte("{\"commit\":{\"sha\":\"" + head + "\"}}"), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/rules/branches/master?per_page=100":
			return []byte("[]"), nil
		case "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte("[{\"path\":\".github/workflows/ci.yml\",\"sha\":\"blob\",\"type\":\"file\"}]"), nil
		case "repos/acme/app/git/blobs/blob":
			content := "on:\n  push:\n    branches:\n      - master\n"
			if workflowDone {
				content = "on:\n  push:\n    branches:\n      - main\n"
			}
			return []byte("{\"encoding\":\"base64\",\"content\":\"" + base64.StdEncoding.EncodeToString([]byte(content)) + "\"}"), nil
		case "repos/acme/app/branches/master/protection", "repos/acme/app/pages":
			return nil, errors.New("HTTP 404")
		case "repos/acme/app/commits/" + workflowHead:
			return []byte("{\"parents\":[{\"sha\":\"" + old + "\"}]}"), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "createCommitOnBranch"):
			workflowDone, head = true, workflowHead
			return githubobserver.CommandResponse{Stdout: []byte("{\"data\":{\"createCommitOnBranch\":{\"commit\":{\"oid\":\"" + workflowHead + "\"}}}}")}
		case strings.Contains(joined, "/branches/master/rename"):
			renames++
			defaultName = "main"
			return githubobserver.CommandResponse{}
		default:
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation " + joined)}
		}
	}
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, RewriteWorkflowTriggers: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if renames != 1 || report.Repositories[0].Disposition != "compliant" || report.Repositories[0].VerifiedDefault != "main" {
		t.Fatalf("report=%#v renames=%d", report.Repositories[0], renames)
	}
}

func TestDiscoverDefaultBranchFleetKeepsOwnerFailureAndDeduplicates(t *testing.T) {
	var (
		defaultBranchListRemote func(owner string) ([]discover.Repo, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{listRemoteFunc: func(owner string) ([]discover.Repo, error) { return defaultBranchListRemote(owner) }},
		Clock:     &fakeClock{},
	}
	defaultBranchListRemote = func(owner string) ([]discover.Repo, error) {
		switch owner {
		case "broken":
			return nil, errors.New("forbidden")
		case "good":
			return []discover.Repo{{Org: "good", Name: "selected"}, {Org: "good", Name: "Selected"}, {Org: "good", Name: "other"}}, nil
		default:
			return nil, errors.New("unexpected owner")
		}
	}
	repos, failures, err := engine.discoverDefaultBranchFleet("selected", []string{"broken", "good"}, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Slug() != "good/selected" {
		t.Fatalf("repos = %#v", repos)
	}
	if len(failures) != 1 || failures[0].Repository != "broken/*" || failures[0].Disposition != "error" {
		t.Fatalf("failures = %#v", failures)
	}
}

func TestDiscoverDefaultBranchFleetRefusesPotentiallyPartialOwnerListing(t *testing.T) {
	var (
		defaultBranchListRemote func(owner string) ([]discover.Repo, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{listRemoteFunc: func(owner string) ([]discover.Repo, error) { return defaultBranchListRemote(owner) }},
		Clock:     &fakeClock{},
	}
	defaultBranchListRemote = func(owner string) ([]discover.Repo, error) {
		if owner != "large" {
			return nil, errors.New("unexpected owner")
		}
		listed := make([]discover.Repo, 1000)
		for i := range listed {
			listed[i] = discover.Repo{Org: owner, Name: fmt.Sprintf("repo-%04d", i)}
		}
		return listed, nil
	}
	repos, failures, err := engine.discoverDefaultBranchFleet("", []string{"large"}, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 || len(failures) != 1 || !strings.Contains(failures[0].Error, "1000 entries") {
		t.Fatalf("partial owner listing was not refused: repos=%#v failures=%#v", repos, failures)
	}
}

func TestInspectDefaultBranchHandlesEmptyAndForkParentSafely(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/empty":
			return []byte(`{"default_branch":""}`), nil
		case "repos/acme/no-initial-commit":
			return []byte(`{"default_branch":"main","size":0}`), nil
		case "repos/acme/no-initial-commit/branches/main":
			return nil, errors.New("HTTP 404")
		case "repos/fork/app":
			return []byte(`{"default_branch":"master","fork":true,"parent":{"full_name":"upstream/app"}}`), nil
		case "repos/fork/app/branches/master":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/fork/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		case "repos/fork/app/pulls?state=open&head=fork%3Amaster":
			return []byte(`[]`), nil
		case "repos/upstream/app/pulls?state=open&head=fork%3Amaster":
			return []byte(`[]`), nil
		case "repos/fork/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	empty := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/empty"), "main", false, false)
	if empty.Disposition != "blocked" || !strings.Contains(empty.Error, "empty repository") {
		t.Fatalf("empty = %#v", empty)
	}
	noInitialCommit := engine.inspectDefaultBranchWithOptions(context.Background(), repo("acme/no-initial-commit"), "main", false, false)
	if noInitialCommit.Disposition != "blocked" || !strings.Contains(noInitialCommit.Error, "no initial commit") {
		t.Fatalf("no initial commit = %#v", noInitialCommit)
	}
	fork := engine.inspectDefaultBranchWithOptions(context.Background(), repo("fork/app"), "main", false, false)
	if fork.Disposition != "drift" || len(fork.Impacts) == 0 {
		t.Fatalf("fork = %#v", fork)
	}
}

func TestInspectDefaultBranchRefusesForkPRInEitherRepository(t *testing.T) {
	for _, blockedEndpoint := range []string{
		"repos/fork/app/pulls?state=open&head=fork%3Amaster",
		"repos/upstream/app/pulls?state=open&head=fork%3Amaster",
	} {
		t.Run(blockedEndpoint, func(t *testing.T) {
			var (
				defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
			)
			engine := &Engine{
				Git:       &fakeGit{},
				GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/fork/app":
					return []byte(`{"default_branch":"master","fork":true,"parent":{"full_name":"upstream/app"}}`), nil
				case "repos/fork/app/branches/master":
					return []byte(`{"commit":{"sha":"same"}}`), nil
				case "repos/fork/app/branches/main":
					return nil, errors.New("HTTP 404")
				case "repos/fork/app/pulls?state=open&head=fork%3Amaster", "repos/upstream/app/pulls?state=open&head=fork%3Amaster":
					if endpoint == blockedEndpoint {
						return []byte(`[{"number":1}]`), nil
					}
					return []byte(`[]`), nil
				default:
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			result := engine.inspectDefaultBranchWithOptions(context.Background(), repo("fork/app"), "main", false, false)
			if result.Disposition != "blocked" || !strings.Contains(result.Error, "open pull request") {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalResumesAlreadyMainTracking(t *testing.T) {
	var (
		defaultBranchGit func(ctx context.Context, dir string, args ...string) (string, error)
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	var calls []string
	defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H":
			return "", nil
		case "worktree list --porcelain":
			return "worktree /canonical\nbranch refs/heads/main", nil
		case "branch --show-current":
			return "main", nil
		case "rev-parse origin/main", "rev-parse main":
			return "abc", nil
		case "rev-parse --abbrev-ref --symbolic-full-name @{u}":
			return "", errors.New("no upstream")
		case "branch --set-upstream-to=origin/main main":
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repo := Repository{Repository: "acme/app", Desired: "main"}
	entry := Canonical{Path: "/canonical", Disposition: "blocked"}
	checkpoints := 0
	if err := engine.reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "abc", func() error { checkpoints++; return nil }); err != nil {
		t.Fatal(err)
	}
	if entry.Disposition != "compliant" || len(entry.Actions) != 1 || checkpoints != 2 {
		t.Fatalf("entry = %#v checkpoints=%d", entry, checkpoints)
	}
	for _, call := range calls {
		if strings.Contains(call, "branch -m") {
			t.Fatalf("already-main clone was renamed: %v", calls)
		}
	}
}

func TestDefaultBranchResumeSourceRefusesMissingOrStaleBindings(t *testing.T) {
	current := Repository{Repository: "acme/app", ObservedDefault: "main", Desired: "main", OldHead: "current"}
	prior := &Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", Disposition: "compliant", ObservedDefault: "master", Desired: "main", VerifiedDefault: "main", OldHead: "old", NewHead: "old",
		Actions: []string{"renamed master to main", "verified default branch and head"},
	}}}
	if source, head, reason := defaultBranchResumeSource(prior, current); source != "" || head != "" || !strings.Contains(reason, "no longer matches") {
		t.Fatalf("stale resume = %q %q %q", source, head, reason)
	}
	if source, head, reason := defaultBranchResumeSource(&Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply"}, current); source != "" || head != "" || !strings.Contains(reason, "no applied migration record") {
		t.Fatalf("missing resume = %q %q %q", source, head, reason)
	}
}

func TestReadDefaultBranchReportRequiresExactCallerDigestBeforeParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	raw := []byte(`{"schema_version":1,"mode":"apply","repositories":[]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDefaultBranchReport(path, ""); err == nil {
		t.Fatal("missing digest was accepted")
	}
	if _, err := readDefaultBranchReport(path, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("wrong digest = %v", err)
	}
	if _, err := readDefaultBranchReport(path, defaultBranchDigest(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultBranchArchiveRestoreIsDigestAndIdentityBound(t *testing.T) {
	prior := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", RepositoryID: 77, Desired: "main",
		Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FinalHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Phase: "failed", RecoveryRequired: true},
	}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived := false
	mutations := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":"main","archived":%t}`, archived)), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if strings.Join(args, " ") != "api --method PATCH repos/acme/app -f archived=true" {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		archived = true
		return githubobserver.CommandResponse{}
	}
	report, err := engine.run(context.Background(), "", "", Options{Apply: true, Repositories: []string{"acme/app"}, RestoreArchiveFrom: path, RestoreArchiveSHA256: defaultBranchDigest(raw), ReportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 1 || report.Repositories[0].Disposition != "compliant" || report.Repositories[0].Archive.Phase != "restored" || len(report.Repositories[0].CanonicalClones) != 0 {
		t.Fatalf("restore report = %#v mutations=%d", report, mutations)
	}

	mutations = 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":78,"default_branch":"main","archived":false}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	if _, err := engine.run(context.Background(), "", "", Options{Apply: true, Repositories: []string{"acme/app"}, RestoreArchiveFrom: path, RestoreArchiveSHA256: defaultBranchDigest(raw), ReportDir: t.TempDir()}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "repository ID") {
		t.Fatalf("wrong ID restore err = %v", err)
	}
	if mutations != 0 {
		t.Fatalf("wrong ID sent %d mutations", mutations)
	}
}

func TestApplyArchivedDefaultBranchRefreshesIdentityBeforeUnarchive(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	mutations := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":78,"default_branch":"master","archived":true}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Disposition: "drift", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if result.Disposition != "blocked" || !strings.Contains(result.Error, "repository ID") || mutations != 0 {
		t.Fatalf("unsafe unarchive = %#v mutations=%d", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchRestoresAfterMigrationFailure(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived := true
	mutations := []string{}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":"master","archived":%t}`, archived)), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		case "repos/acme/app/branches/main":
			return nil, errors.New("HTTP 404")
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutation := strings.Join(args, " ")
		mutations = append(mutations, mutation)
		switch mutation {
		case "api --method PATCH repos/acme/app -f archived=false":
			archived = false
			return githubobserver.CommandResponse{}
		case "api --method POST repos/acme/app/branches/master/rename -f new_name=main":
			return githubobserver.CommandResponse{Err: errors.New("rename rejected")}
		case "api --method PATCH repos/acme/app -f archived=true":
			archived = true
			return githubobserver.CommandResponse{}
		default:
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Disposition: "drift", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if !archived || len(mutations) != 3 || result.Archive.Phase != "restored" || result.Archive.RecoveryRequired || result.Disposition != "error" {
		t.Fatalf("failed migration did not restore archive: %#v mutations=%v", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchRewritesWorkflowAndRestores(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived, branch := true, "master"
	old, child := strings.Repeat("a", 40), strings.Repeat("b", 40)
	head, workflowDone, renameFailed := old, false, false
	mutations := []string{}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":%q,"archived":%t}`, branch, archived)), nil
		case "repos/acme/app/branches/master":
			if branch == "main" {
				return nil, errors.New("HTTP 404")
			}
			return []byte(fmt.Sprintf(`{"commit":{"sha":%q}}`, head)), nil
		case "repos/acme/app/branches/main":
			if branch != "main" {
				return nil, errors.New("HTTP 404")
			}
			return []byte(fmt.Sprintf(`{"commit":{"sha":%q}}`, head)), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
			return []byte(`[]`), nil
		case "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
		case "repos/acme/app/git/blobs/blob":
			contents := "name: remaster check\non:\n  push:\n    branches: [ master ]\n  pull_request:\n    branches: [master]\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n"
			if workflowDone {
				contents = "name: remaster check\non:\n  push:\n    branches: [ main ]\n  pull_request:\n    branches: [main]\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n"
			}
			return []byte(fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(contents)))), nil
		case "repos/acme/app/commits/" + child:
			return []byte(fmt.Sprintf(`{"parents":[{"sha":%q}]}`, old)), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutation := strings.Join(args, " ")
		mutations = append(mutations, mutation)
		switch mutation {
		case "api --method PATCH repos/acme/app -f archived=false":
			archived = false
		case "api graphql -f query=" + defaultBranchWorkflowMutation + " -F branch[repositoryNameWithOwner]=acme/app -F branch[branchName]=master -F expected=" + old + " -F message[headline]=chore: update default-branch workflow triggers -F additions[][path]=.github/workflows/ci.yml -F additions[][contents]=" + base64.StdEncoding.EncodeToString([]byte("name: remaster check\non:\n  push:\n    branches: [ main ]\n  pull_request:\n    branches: [main]\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n")):
			workflowDone, head = true, child
			return githubobserver.CommandResponse{Stdout: []byte(fmt.Sprintf(`{"data":{"createCommitOnBranch":{"commit":{"oid":%q}}}}`, child))}
		case "api --method POST repos/acme/app/branches/master/rename -f new_name=main":
			if renameFailed {
				return githubobserver.CommandResponse{Err: errors.New("rename rejected")}
			}
			branch = "main"
		case "api --method PATCH repos/acme/app -f archived=true":
			archived = true
		default:
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		return githubobserver.CommandResponse{}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: old, Disposition: "drift", WorkflowFiles: []Workflow{{Path: ".github/workflows/ci.yml"}}, Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: old, Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if !archived || branch != "main" || !workflowDone || result.Disposition != "compliant" || result.Archive.Phase != "restored" || result.Archive.FinalHead != child || len(mutations) != 4 {
		t.Fatalf("archived workflow migration = %#v mutations=%v", result, mutations)
	}
	if !strings.Contains(mutations[1], "createCommitOnBranch") || !strings.Contains(mutations[2], "/branches/master/rename") || !strings.Contains(mutations[3], "archived=true") {
		t.Fatalf("wrong archived workflow order: %v", mutations)
	}
	renameFailed, archived, branch = true, true, "master"
	head, workflowDone, mutations = old, false, nil
	repository.Archive = &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: old, Phase: "prepared"}
	result = engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if !archived || !workflowDone || result.Archive.Phase != "restored" || result.Disposition != "error" || len(mutations) != 4 || !strings.Contains(mutations[3], "archived=true") {
		t.Fatalf("failed post-workflow rename did not restore archive: %#v mutations=%v", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchSwitchesSameHeadAndRestores(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived, branch := true, "master"
	mutations := []string{}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":%q,"archived":%t}`, branch, archived)), nil
		case "repos/acme/app/branches/master", "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutation := strings.Join(args, " ")
		mutations = append(mutations, mutation)
		switch mutation {
		case "api --method PATCH repos/acme/app -f archived=false":
			archived = false
		case "api --method PATCH repos/acme/app -f default_branch=main":
			branch = "main"
		case "api --method PATCH repos/acme/app -f archived=true":
			archived = true
		default:
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		return githubobserver.CommandResponse{}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NewHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TargetExists: true, Disposition: "drift", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if !archived || branch != "main" || result.Disposition != "compliant" || result.Archive.Phase != "restored" || result.Archive.FinalHead != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(mutations) != 3 {
		t.Fatalf("same-head archived migration = %#v mutations=%v", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchRestoresAfterUnarchiveFailure(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	mutations := []string{}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":77,"default_branch":"master","archived":true}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutation := strings.Join(args, " ")
		mutations = append(mutations, mutation)
		if strings.Contains(mutation, "archived=false") {
			return githubobserver.CommandResponse{Err: errors.New("unarchive request lost")}
		}
		if strings.Contains(mutation, "archived=true") {
			return githubobserver.CommandResponse{}
		}
		return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Disposition: "drift", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if result.Archive.Phase != "restored" || result.Archive.RecoveryRequired || result.Disposition != "error" || len(mutations) != 2 || !strings.Contains(mutations[1], "archived=true") {
		t.Fatalf("unarchive failure restore = %#v mutations=%v", result, mutations)
	}
}

func TestValidatePlannedArchivedDefaultBranchFailsClosed(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	transition := &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for name, test := range map[string]struct {
		metadata defaultBranchRepoMetadata
		head     string
		want     string
	}{
		"ID":      {defaultBranchRepoMetadata{ID: 78, Archived: true, DefaultBranch: "master"}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "repository ID"},
		"active":  {defaultBranchRepoMetadata{ID: 77, Archived: false, DefaultBranch: "master"}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "no longer archived"},
		"default": {defaultBranchRepoMetadata{ID: 77, Archived: true, DefaultBranch: "trunk"}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "default branch"},
		"head":    {defaultBranchRepoMetadata{ID: 77, Archived: true, DefaultBranch: "master"}, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "default head"},
	} {
		t.Run(name, func(t *testing.T) {
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				if endpoint != "repos/acme/app/branches/master" {
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
				return []byte(`{"commit":{"sha":"` + test.head + `"}}`), nil
			}
			if err := engine.validatePlannedArchivedDefaultBranch(context.Background(), test.metadata, "acme/app", transition); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("planned archive validation err = %v, want %q", err, test.want)
			}
		})
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		return nil, errors.New("branch unavailable: " + endpoint)
	}
	if err := engine.validatePlannedArchivedDefaultBranch(context.Background(), defaultBranchRepoMetadata{ID: 77, Archived: true, DefaultBranch: "master"}, "acme/app", transition); err == nil || !strings.Contains(err.Error(), "read planned") {
		t.Fatalf("planned head read err = %v", err)
	}
}

func TestApplyArchivedDefaultBranchGuardsAndPreMutationCheckpoint(t *testing.T) {
	engine := &Engine{Git: &fakeGit{}, GitHub: &fakeGitHub{}, Discovery: &fakeDiscovery{}, Clock: &fakeClock{}}
	base := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	if result := engine.applyArchivedDefaultBranch(context.Background(), Repository{Repository: "acme/app"}, func(Repository) error { return nil }); result.Disposition != "blocked" || !strings.Contains(result.Error, "stable") {
		t.Fatalf("missing archive proof = %#v", result)
	}
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine = &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("metadata unavailable") }
	if result := engine.applyArchivedDefaultBranch(context.Background(), base, func(Repository) error { return nil }); result.Disposition != "error" || !strings.Contains(result.Error, "refresh archived") {
		t.Fatalf("metadata failure = %#v", result)
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":77,"default_branch":"master","archived":true}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutations := 0
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{}
	}
	if result := engine.applyArchivedDefaultBranch(context.Background(), base, func(Repository) error { return errors.New("disk full") }); result.Disposition != "error" || !strings.Contains(result.Error, "persist pending") || mutations != 0 {
		t.Fatalf("pre-mutation checkpoint failure = %#v mutations=%d", result, mutations)
	}
}

func TestRestoreArchivedDefaultBranchPersistsPreMutationReadFailure(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("metadata unavailable") }
	mutations, checkpoints := 0, []Repository{}
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
	result := engine.restoreArchivedDefaultBranch(context.Background(), repository, func(updated Repository) error {
		checkpoints = append(checkpoints, updated)
		return nil
	})
	if mutations != 0 || result.Archive.Phase != "failed" || !result.Archive.RecoveryRequired || len(checkpoints) != 2 || checkpoints[1].Archive.Phase != "failed" || !strings.Contains(result.Error, "refresh repository") {
		t.Fatalf("pre-restore read failure = %#v checkpoints=%#v mutations=%d", result, checkpoints, mutations)
	}
}

func TestApplyArchivedDefaultBranchRestoresAfterAcceptedUnarchiveCheckpointFailure(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived := true
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":"master","archived":%t}`, archived)), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		if strings.Contains(strings.Join(args, " "), "archived=false") {
			archived = false
		} else if strings.Contains(strings.Join(args, " "), "archived=true") {
			archived = true
		} else {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		return githubobserver.CommandResponse{}
	}
	checkpoints := 0
	repository := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error {
		checkpoints++
		if checkpoints == 2 {
			return errors.New("receipt disk full")
		}
		return nil
	})
	if !archived || result.Archive.Phase != "restored" || result.Disposition != "error" || !strings.Contains(result.Error, "persist accepted") {
		t.Fatalf("accepted-unarchive checkpoint recovery = %#v", result)
	}
}

func TestApplyArchivedDefaultBranchRefusesPostUnarchiveIdentityChange(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived, reads, mutations := true, 0, 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			reads++
			id := 77
			if reads >= 2 {
				id = 78
			}
			return []byte(fmt.Sprintf(`{"id":%d,"default_branch":"master","archived":%t}`, id, archived)), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if strings.Contains(strings.Join(args, " "), "archived=false") {
			archived = false
			return githubobserver.CommandResponse{}
		}
		return githubobserver.CommandResponse{Err: errors.New("must not rearchive a changed repository")}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := engine.applyArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if mutations != 1 || result.Archive.Phase != "failed" || !result.Archive.RecoveryRequired || !strings.Contains(result.Error, "repository ID changed") {
		t.Fatalf("post-unarchive identity change = %#v mutations=%d", result, mutations)
	}
}

func TestRestoreArchivedDefaultBranchRecordsActionAndFinalCheckpointFailure(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived := false
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":"master","archived":%t}`, archived)), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		archived = true
		return githubobserver.CommandResponse{}
	}
	checkpoints := 0
	repository := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Disposition: "compliant", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
	result := engine.restoreArchivedDefaultBranch(context.Background(), repository, func(Repository) error {
		checkpoints++
		if checkpoints == 2 {
			return errors.New("final receipt write failed")
		}
		return nil
	})
	if result.Disposition != "error" || !strings.Contains(result.Error, "persist verified") || len(result.Actions) != 1 || result.Actions[0] != "restored archived state" {
		t.Fatalf("final checkpoint failure = %#v", result)
	}
}

func TestRunDefaultBranchTemporarilyUnarchivesAndRestoresBeforeLocalReconcile(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	projectsRoot := t.TempDir()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return config }
	archived, branch := true, "master"
	mutations := []string{}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":%q,"archived":%t}`, branch, archived)), nil
		case "repos/acme/app/branches/master", "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutation := strings.Join(args, " ")
		mutations = append(mutations, mutation)
		switch mutation {
		case "api --method PATCH repos/acme/app -f archived=false":
			archived = false
		case "api --method PATCH repos/acme/app -f default_branch=main":
			branch = "main"
		case "api --method PATCH repos/acme/app -f archived=true":
			archived = true
		default:
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		return githubobserver.CommandResponse{}
	}
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, TemporarilyUnarchive: true, Parallel: 1, ReportDir: t.TempDir()}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	repository := report.Repositories[0]
	if !archived || branch != "main" || len(mutations) != 3 || repository.Disposition != "compliant" || repository.Archive == nil || repository.Archive.Phase != "restored" || len(repository.CanonicalClones) != 0 {
		t.Fatalf("archived command run = %#v mutations=%v", repository, mutations)
	}
	persisted, err := os.ReadFile(report.ReportPath)
	if err != nil || !strings.Contains(string(persisted), `"phase": "restored"`) {
		t.Fatalf("archived command receipt = %q err=%v", persisted, err)
	}
}

func TestRunDefaultBranchArchiveRestoreRejectsReceiptBeforeMutation(t *testing.T) {
	engine := &Engine{Git: &fakeGit{}, GitHub: &fakeGitHub{}, Discovery: &fakeDiscovery{}, Clock: &fakeClock{}}
	if _, err := engine.run(context.Background(), "", "", Options{RestoreArchiveFrom: "missing.json", RestoreArchiveSHA256: "bad"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "restore-archive-sha256") {
		t.Fatalf("invalid digest err = %v", err)
	}
	prior := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{Repository: "acme/app", RepositoryID: 77, Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.run(context.Background(), "", "", Options{Repositories: []string{"acme/missing"}, RestoreArchiveFrom: path, RestoreArchiveSHA256: defaultBranchDigest(raw)}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no archived") {
		t.Fatalf("wrong repository err = %v", err)
	}
	reportDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(reportDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.run(context.Background(), "", "", Options{Repositories: []string{"acme/app"}, RestoreArchiveFrom: path, RestoreArchiveSHA256: defaultBranchDigest(raw), ReportDir: reportDir}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("unwritable report directory err = %v", err)
	}
}

func TestArchiveRestoreReceiptFailureEdges(t *testing.T) {
	engine := &Engine{Git: &fakeGit{}, GitHub: &fakeGitHub{}, Discovery: &fakeDiscovery{}, Clock: &fakeClock{}}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err := readDefaultBranchArchiveRestoreReport(missing, strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("missing receipt err = %v", err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"mode":"apply"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDefaultBranchArchiveRestoreReport(path, strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("changed receipt err = %v", err)
	}
	transition := &Archive{RepositoryID: 77, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := engine.validateDefaultBranchArchiveRestoreTarget(context.Background(), defaultBranchRepoMetadata{ID: 78, DefaultBranch: "master"}, "acme/app", transition); err == nil || !strings.Contains(err.Error(), "repository ID") {
		t.Fatalf("wrong restore target ID err = %v", err)
	}
	report := Report{ReportPath: filepath.Join(t.TempDir(), "missing", "receipt.json"), Repositories: []Repository{{Repository: "acme/app", Archive: transition}}}
	if _, err := failDefaultBranchArchiveRestore(&report, report.Repositories[0], "receipt persistence failure"); err == nil {
		t.Fatal("failed receipt persistence was accepted")
	}
	unchanged := Repository{Repository: "acme/app"}
	if got := engine.restoreArchivedDefaultBranch(context.Background(), unchanged, func(Repository) error { return errors.New("must not checkpoint") }); got.Repository != unchanged.Repository || got.Archive != nil || got.Disposition != unchanged.Disposition || got.Error != unchanged.Error {
		t.Fatalf("nil archive transition changed repository: %#v", got)
	}
}

func TestRestoreArchivedDefaultBranchAttemptsRestoreAfterCheckpointFailure(t *testing.T) {
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	archived := false
	mutations := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":"master","archived":%t}`, archived)), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutations++
		if strings.Join(args, " ") != "api --method PATCH repos/acme/app -f archived=true" {
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		archived = true
		return githubobserver.CommandResponse{}
	}
	repository := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Disposition: "error", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
	result := engine.restoreArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return errors.New("disk full") })
	if mutations != 1 || !archived || result.Archive.Phase != "restored" || result.Disposition != "error" || !strings.Contains(result.Error, "persist verified") {
		t.Fatalf("checkpoint failure did not attempt durable restore: %#v mutations=%d", result, mutations)
	}

	mutations = 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if endpoint == "repos/acme/app" {
			return []byte(`{"id":78,"default_branch":"master","archived":false}`), nil
		}
		return nil, errors.New("unexpected endpoint " + endpoint)
	}
	result = engine.restoreArchivedDefaultBranch(context.Background(), repository, func(Repository) error { return nil })
	if mutations != 0 || result.Archive.Phase != "failed" || !strings.Contains(result.Error, "repository ID changed") {
		t.Fatalf("changed restore identity sent a mutation: %#v mutations=%d", result, mutations)
	}
}

func TestRestoreArchivedDefaultBranchPersistsPostMutationVerificationFailure(t *testing.T) {
	for name, read := range map[string]func(int, string) ([]byte, error){
		"metadata": func(repoReads int, endpoint string) ([]byte, error) {
			if endpoint != "repos/acme/app" {
				return nil, errors.New("unexpected endpoint " + endpoint)
			}
			if repoReads == 2 {
				return nil, errors.New("metadata unavailable")
			}
			return []byte(`{"id":77,"default_branch":"master","archived":false}`), nil
		},
		"head": func(_ int, endpoint string) ([]byte, error) {
			switch endpoint {
			case "repos/acme/app":
				return []byte(`{"id":77,"default_branch":"master","archived":true}`), nil
			case "repos/acme/app/branches/master":
				return nil, errors.New("head unavailable")
			default:
				return nil, errors.New("unexpected endpoint " + endpoint)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
				defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
			)
			engine := &Engine{
				Git: &fakeGit{},
				GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
					return defaultBranchExecute(ctx, args...)
				}},
				Discovery: &fakeDiscovery{},
				Clock:     &fakeClock{},
			}
			repoReads, mutations := 0, 0
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				if endpoint == "repos/acme/app" {
					repoReads++
				}
				return read(repoReads, endpoint)
			}
			defaultBranchExecute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				mutations++
				if strings.Join(args, " ") != "api --method PATCH repos/acme/app -f archived=true" {
					return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
				}
				return githubobserver.CommandResponse{}
			}
			persisted := []Repository{}
			repository := Repository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Disposition: "error", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
			result := engine.restoreArchivedDefaultBranch(context.Background(), repository, func(updated Repository) error {
				persisted = append(persisted, updated)
				return nil
			})
			if mutations != 1 || result.Archive.Phase != "failed" || !result.Archive.RecoveryRequired || len(persisted) < 2 || persisted[len(persisted)-1].Archive.Phase != "failed" || !persisted[len(persisted)-1].Archive.RecoveryRequired {
				t.Fatalf("post-mutation failure was not durably checkpointed: %#v persisted=%#v mutations=%d", result, persisted, mutations)
			}
		})
	}
}

func TestDefaultBranchArchiveRestorePairsDefaultAndHeadAndPersistsRefusal(t *testing.T) {
	transition := &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FinalHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if endpoint == "repos/acme/app/branches/master" {
			return []byte(`{"commit":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), nil
		}
		return nil, errors.New("unexpected endpoint " + endpoint)
	}
	if err := engine.validateDefaultBranchArchiveRestoreTarget(context.Background(), defaultBranchRepoMetadata{ID: 77, DefaultBranch: "master"}, "acme/app", transition); err == nil || !strings.Contains(err.Error(), "pair") {
		t.Fatalf("cross-paired restore state was accepted: %v", err)
	}

	prior := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: transition}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"id":77,"default_branch":"master","archived":false}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	report, err := engine.run(context.Background(), "", "", Options{Apply: true, Repositories: []string{"acme/app"}, RestoreArchiveFrom: path, RestoreArchiveSHA256: defaultBranchDigest(raw), ReportDir: t.TempDir()}, &bytes.Buffer{})
	if err == nil || report.Repositories[0].Archive.Phase != "failed" || !report.Repositories[0].Archive.RecoveryRequired {
		t.Fatalf("unsafe restore refusal was not durably recorded: %#v err=%v", report, err)
	}
	persisted, readErr := os.ReadFile(report.ReportPath)
	if readErr != nil || !strings.Contains(string(persisted), `"phase": "failed"`) {
		t.Fatalf("restore refusal receipt = %q err=%v", persisted, readErr)
	}
}

func TestDefaultBranchArchiveRestoreReceiptValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	if _, err := readDefaultBranchArchiveRestoreReport(path, "bad"); err == nil {
		t.Fatal("short digest was accepted")
	}
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	malformed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readDefaultBranchArchiveRestoreReport(path, defaultBranchDigest(malformed)); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed receipt err = %v", err)
	}
	wrongSchema := []byte(`{"schema_version":99,"mode":"apply"}`)
	if err := os.WriteFile(path, wrongSchema, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDefaultBranchArchiveRestoreReport(path, defaultBranchDigest(wrongSchema)); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("wrong schema err = %v", err)
	}

	valid := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{Repository: "acme/app", RepositoryID: 77, Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	if _, _, err := defaultBranchArchiveRestoreTarget(valid, "acme/missing"); err == nil || !strings.Contains(err.Error(), "no archived") {
		t.Fatalf("missing target err = %v", err)
	}
	invalid := valid
	invalid.Repositories = append([]Repository(nil), valid.Repositories...)
	invalidTransition := *invalid.Repositories[0].Archive
	invalidTransition.InitialHead = "not-a-sha"
	invalid.Repositories[0].Archive = &invalidTransition
	if _, _, err := defaultBranchArchiveRestoreTarget(invalid, "acme/app"); err == nil || !strings.Contains(err.Error(), "stable") {
		t.Fatalf("invalid target err = %v", err)
	}
	valid.Repositories[0].Desired = "trunk"
	if _, _, err := defaultBranchArchiveRestoreTarget(valid, "acme/app"); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting desired err = %v", err)
	}
}

func TestRunDefaultBranchArchiveRestoreVerifiesAlreadyArchivedAndMutationFailure(t *testing.T) {
	prior := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "failed"}}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	for name, archived := range map[string]bool{"already archived": true, "mutation failure": false} {
		t.Run(name, func(t *testing.T) {
			mutations := 0
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app":
					return []byte(fmt.Sprintf(`{"id":77,"default_branch":"master","archived":%t}`, archived)), nil
				case "repos/acme/app/branches/master":
					return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
				default:
					return nil, errors.New("unexpected endpoint " + endpoint)
				}
			}
			defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
				mutations++
				return githubobserver.CommandResponse{Err: errors.New("network dropped")}
			}
			report, runErr := engine.run(context.Background(), "", "", Options{Apply: true, Repositories: []string{"acme/app"}, RestoreArchiveFrom: path, RestoreArchiveSHA256: defaultBranchDigest(raw), ReportDir: t.TempDir()}, &bytes.Buffer{})
			if archived {
				if runErr != nil || mutations != 0 || report.Repositories[0].Archive.Phase != "restored" || report.Repositories[0].Disposition != "compliant" {
					t.Fatalf("already archived report = %#v err=%v mutations=%d", report, runErr, mutations)
				}
				return
			}
			if runErr == nil || mutations != 1 || report.Repositories[0].Archive.Phase != "failed" || !report.Repositories[0].Archive.RecoveryRequired {
				t.Fatalf("mutation failure report = %#v err=%v mutations=%d", report, runErr, mutations)
			}
		})
	}
}

func TestDefaultBranchResumeSourceRequiresVerifiedMigrationProof(t *testing.T) {
	current := Repository{Repository: "acme/app", ObservedDefault: "main", Desired: "main", OldHead: "same"}
	verified := Repository{
		Repository: "acme/app", Disposition: "compliant", ObservedDefault: "master", VerifiedDefault: "main", Desired: "main",
		OldHead: "same", NewHead: "same", Actions: []string{"renamed master to main", "verified default branch and head"},
	}
	if source, head, reason := defaultBranchResumeSource(&Report{Repositories: []Repository{verified}}, current); source != "master" || head != "same" || reason != "" {
		t.Fatalf("verified resume = %q %q %q", source, head, reason)
	}
	for name, mutate := range map[string]func(*Repository){
		"noncompliant":       func(v *Repository) { v.Disposition = "drift" },
		"unverified default": func(v *Repository) { v.VerifiedDefault = "" },
		"wrong head":         func(v *Repository) { v.NewHead = "other" },
		"forged action":      func(v *Repository) { v.Actions = []string{"renamed master to main"} },
		"same source":        func(v *Repository) { v.ObservedDefault = "main" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := verified
			mutate(&candidate)
			if source, head, reason := defaultBranchResumeSource(&Report{Repositories: []Repository{candidate}}, current); source != "" || head != "" || !strings.Contains(reason, "verified successful") {
				t.Fatalf("forged resume = %q %q %q", source, head, reason)
			}
		})
	}
}

func TestDefaultBranchResumeSourceRequiresTerminalPagesProof(t *testing.T) {
	sha := "2d2c113d93c2309584499baf511bfa26db66dbee"
	before := &PagesSource{BuildType: "legacy", Branch: "master", Path: "/"}
	after := &PagesSource{BuildType: "legacy", Branch: "main", Path: "/"}
	current := Repository{Repository: "angular-dnd/angular-dnd.github.io", RepositoryID: 263838803, ObservedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha, Disposition: "compliant", PagesAfter: after}
	verified := Repository{Repository: current.Repository, RepositoryID: current.RepositoryID, Disposition: "compliant", ObservedDefault: "master", VerifiedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha, RenameAccepted: true, PagesBefore: before, PagesAfter: after, PagesPhase: "verified", Actions: []string{"renamed master to main", "verified default branch and head", "verified GitHub automatic Pages source transition to main with path /"}}
	receipt := &Report{Repositories: []Repository{verified}}
	if source, head, reason := defaultBranchResumeSource(receipt, current); source != "master" || head != sha || reason != "" {
		t.Fatalf("verified Pages resume = %q %q %q", source, head, reason)
	}
	for name, mutate := range map[string]func(*Repository, *Repository){
		"unfinished Pages":        func(p, _ *Repository) { p.PagesPhase = "prepared" },
		"missing Pages post-read": func(p, _ *Repository) { p.PagesAfter = nil },
		"changed current path": func(_, c *Repository) {
			c.PagesAfter = &PagesSource{BuildType: "legacy", Branch: "main", Path: "/docs"}
		},
		"wrong repository ID": func(_, c *Repository) { c.RepositoryID++ },
		"forged action": func(p, _ *Repository) {
			p.Actions = []string{"renamed master to main", "verified default branch and head"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			priorCopy, currentCopy := verified, current
			mutate(&priorCopy, &currentCopy)
			if source, head, reason := defaultBranchResumeSource(&Report{Repositories: []Repository{priorCopy}}, currentCopy); source != "" || head != "" || reason == "" {
				t.Fatalf("unverified Pages resume = %q %q %q", source, head, reason)
			}
		})
	}
	verified.PagesAccepted = true
	verified.Actions = []string{"renamed master to main", "verified default branch and head", "migrated Pages source to main with path /", "verified Pages source"}
	if source, head, reason := defaultBranchResumeSource(&Report{Repositories: []Repository{verified}}, current); source != "master" || head != sha || reason != "" {
		t.Fatalf("verified Pages PUT resume = %q %q %q", source, head, reason)
	}
}

func TestDefaultBranchResumeSourceRequiresTerminalArchivedMigrationReceipt(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	current := Repository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha, Archived: true, Fork: true}
	verified := Repository{
		Repository: "acme/app", RepositoryID: 77, Disposition: "compliant", ObservedDefault: "master", VerifiedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha, Archived: true, Fork: true,
		Actions: []string{"renamed master to main", "verified default branch and head", "restored archived state"},
		Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: sha, FinalHead: sha, Phase: "restored", UnarchiveAccepted: true, RestoreAccepted: true},
	}
	if source, head, reason := defaultBranchResumeSource(&Report{Repositories: []Repository{verified}}, current); source != "master" || head != sha || reason != "" {
		t.Fatalf("terminal archived receipt = %q %q %q", source, head, reason)
	}
	for name, mutate := range map[string]func(*Repository, *Repository){
		"prior id missing":     func(v, _ *Repository) { v.RepositoryID = 0 },
		"current id changed":   func(_, c *Repository) { c.RepositoryID++ },
		"fork changed":         func(_, c *Repository) { c.Fork = false },
		"current not archived": func(_, c *Repository) { c.Archived = false },
		"prior not archived":   func(v, _ *Repository) { v.Archived = false },
		"archive omitted":      func(v, _ *Repository) { v.Archive = nil },
		"archive omitted ordinary actions": func(v, _ *Repository) {
			v.Archive = nil
			v.Actions = []string{"renamed master to main", "verified default branch and head"}
		},
		"archive id changed":       func(v, _ *Repository) { v.Archive.RepositoryID++ },
		"original archive missing": func(v, _ *Repository) { v.Archive.OriginalArchived = false },
		"initial default changed":  func(v, _ *Repository) { v.Archive.InitialDefault = "trunk" },
		"desired default changed":  func(v, _ *Repository) { v.Archive.DesiredDefault = "trunk" },
		"initial head changed":     func(v, _ *Repository) { v.Archive.InitialHead = strings.Repeat("a", 40) },
		"final head changed":       func(v, _ *Repository) { v.Archive.FinalHead = strings.Repeat("a", 40) },
		"not restored":             func(v, _ *Repository) { v.Archive.Phase = "restore_pending" },
		"unarchive not accepted":   func(v, _ *Repository) { v.Archive.UnarchiveAccepted = false },
		"restore not accepted":     func(v, _ *Repository) { v.Archive.RestoreAccepted = false },
		"recovery required":        func(v, _ *Repository) { v.Archive.RecoveryRequired = true },
		"restore error":            func(v, _ *Repository) { v.Archive.RestoreError = "network" },
		"forged action":            func(v, _ *Repository) { v.Actions[1] = "forged" },
		"reordered action":         func(v, _ *Repository) { v.Actions[0], v.Actions[1] = v.Actions[1], v.Actions[0] },
		"suffixed action":          func(v, _ *Repository) { v.Actions = append(v.Actions, "extra") },
		"malformed receipt SHA": func(v, _ *Repository) {
			v.OldHead = "short"
			v.NewHead = "short"
			v.Archive.InitialHead = "short"
			v.Archive.FinalHead = "short"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, refreshed := verified, current
			archive := *verified.Archive
			candidate.Archive = &archive
			candidate.Actions = append([]string(nil), verified.Actions...)
			mutate(&candidate, &refreshed)
			if source, head, reason := defaultBranchResumeSource(&Report{Repositories: []Repository{candidate}}, refreshed); source != "" || head != "" || !strings.Contains(reason, "verified successful") {
				t.Fatalf("invalid archived receipt = %q %q %q", source, head, reason)
			}
		})
	}
}

func TestRunDefaultBranchResumesTerminalArchivedMacReceiptOnVMClone(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead             func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute          func(ctx context.Context, args ...string) githubobserver.CommandResponse
		defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
		defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
		defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
			return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
		}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
			return defaultBranchAttachHead(ctx, dir, destination)
		}},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	projectsRoot := t.TempDir()
	clone := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	prior := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", RepositoryID: 77, Disposition: "compliant", ObservedDefault: "master", VerifiedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha, Archived: true, Fork: true,
		Actions: []string{"renamed master to main", "verified default branch and head", "restored archived state"},
		Archive: &Archive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: sha, FinalHead: sha, Phase: "restored", UnarchiveAccepted: true, RestoreAccepted: true},
	}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mac-archived-apply.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	metadataID, metadataArchived, metadataFork, remoteHead := int64(77), true, true, sha
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":%d,"default_branch":"main","archived":%t,"fork":%t}`, metadataID, metadataArchived, metadataFork)), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"` + remoteHead + `"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutations := 0
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{Err: errors.New("remote mutation during reconciliation")}
	}
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { return nil }
	defaultBranchAttachHead = func(_ context.Context, _, _ string) error { return nil }
	var calls []string
	defaultBranchGit = func(_ context.Context, dir string, args ...string) (string, error) {
		if dir != clone {
			return "", errors.New("unexpected clone " + dir)
		}
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "remote get-url origin":
			return "git@github.com:acme/app.git", nil
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H", "branch --set-upstream-to=origin/main main":
			return "", nil
		case "worktree list --porcelain":
			return "worktree " + clone + "\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "rev-parse origin/main", "rev-parse master", "rev-parse main", "rev-parse HEAD":
			return remoteHead, nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		case "branch -m master main":
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: t.TempDir(), ReconcileFrom: path, ReconcileSHA256: defaultBranchDigest(raw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 0 || len(report.Repositories[0].CanonicalClones) != 1 || report.Repositories[0].CanonicalClones[0].Disposition != "compliant" {
		t.Fatalf("archived VM reconciliation = %#v mutations=%d", report.Repositories[0], mutations)
	}
	for name, mutate := range map[string]func(*Repository){
		"repository id changed": func(_ *Repository) { metadataID = 78 },
		"archive changed":       func(_ *Repository) { metadataArchived = false },
		"fork changed":          func(_ *Repository) { metadataFork = false },
		"default SHA changed":   func(_ *Repository) { remoteHead = strings.Repeat("a", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			metadataID, metadataArchived, metadataFork, remoteHead = 77, true, true, sha
			mutate(&prior.Repositories[0])
			raw, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			candidatePath := filepath.Join(t.TempDir(), "invalid-archived-receipt.json")
			if err := os.WriteFile(candidatePath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			calls, mutations = nil, 0
			blocked, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: t.TempDir(), ReconcileFrom: candidatePath, ReconcileSHA256: defaultBranchDigest(raw)}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if mutations != 0 || len(blocked.Repositories[0].CanonicalClones) != 1 || blocked.Repositories[0].CanonicalClones[0].Disposition != "blocked" || strings.Contains(strings.Join(calls, "\n"), "branch -m") {
				t.Fatalf("invalid archived receipt mutated state: report=%#v mutations=%d calls=%v", blocked.Repositories[0], mutations, calls)
			}
		})
	}
}

func TestDefaultBranchLegacyRenameResumeRequiresExactV1PostProofRecord(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	current := Repository{Repository: "acme/app", Disposition: "compliant", ObservedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha}
	prior := &Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", Disposition: "error", Error: defaultBranchLegacyRenamePostProofError, ObservedDefault: "master", Desired: "main", OldHead: sha,
	}}}
	reads := 0
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		reads++
		if endpoint != "repos/acme/app/git/ref/heads/master" {
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
		return nil, errors.New("HTTP 404")
	}
	if source, head, reason := engine.defaultBranchLegacyRenameResume(context.Background(), prior, current); source != "master" || head != sha || reason != "" || reads != 1 {
		t.Fatalf("legacy resume = %q %q %q reads=%d", source, head, reason, reads)
	}
	responsePending := *prior
	responsePending.Repositories = append([]Repository(nil), prior.Repositories...)
	responsePending.Repositories[0].RenameAccepted = true
	responsePending.Repositories[0].Disposition = "error"
	responsePending.Repositories[0].Error = "read renamed target branch: context deadline exceeded"
	reads = 0
	if source, head, reason := engine.defaultBranchLegacyRenameResume(context.Background(), &responsePending, current); source != "master" || head != sha || reason != "" || reads != 1 {
		t.Fatalf("pending response resume = %q %q %q reads=%d", source, head, reason, reads)
	}
	for name, mutate := range map[string]func(*Report, *Repository){
		"wrong schema":       func(r *Report, _ *Repository) { r.SchemaVersion = 2 },
		"changed head":       func(_ *Report, c *Repository) { c.OldHead = strings.Repeat("a", 40) },
		"malformed old head": func(r *Report, _ *Repository) { r.Repositories[0].OldHead = "short" },
		"target existed":     func(r *Report, _ *Repository) { r.Repositories[0].TargetExists = true },
		"forged action": func(r *Report, _ *Repository) {
			r.Repositories[0].Actions = []string{"renamed master to main"}
		},
		"wrong error": func(r *Report, _ *Repository) { r.Repositories[0].Error = "other" },
		"pre mutation": func(r *Report, _ *Repository) {
			r.Repositories[0].Disposition, r.Repositories[0].Error = "drift", "default-branch mutation pending"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateReport := *prior
			candidateReport.Repositories = append([]Repository(nil), prior.Repositories...)
			candidateCurrent := current
			mutate(&candidateReport, &candidateCurrent)
			reads = 0
			if source, head, reason := engine.defaultBranchLegacyRenameResume(context.Background(), &candidateReport, candidateCurrent); source != "" || head != "" || reason == "" || reads != 0 {
				t.Fatalf("forged legacy resume = %q %q %q reads=%d", source, head, reason, reads)
			}
		})
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if endpoint != "repos/acme/app/git/ref/heads/master" {
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
		return []byte(`{"ref":"refs/heads/master"}`), nil
	}
	if source, head, reason := engine.defaultBranchLegacyRenameResume(context.Background(), prior, current); source != "" || head != "" || !strings.Contains(reason, "old source ref still exists") {
		t.Fatalf("present old ref was accepted: %q %q %q", source, head, reason)
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if endpoint != "repos/acme/app/git/ref/heads/master" {
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
		return nil, errors.New("HTTP 503")
	}
	if source, head, reason := engine.defaultBranchLegacyRenameResume(context.Background(), prior, current); source != "" || head != "" || !strings.Contains(reason, "could not prove") {
		t.Fatalf("unavailable old ref proof was accepted: %q %q %q", source, head, reason)
	}
	if source, head, reason := engine.defaultBranchLegacyRenameResume(context.Background(), &Report{SchemaVersion: 1, Mode: "apply"}, current); source != "" || head != "" || !strings.Contains(reason, "no applied") {
		t.Fatalf("missing legacy receipt record was accepted: %q %q %q", source, head, reason)
	}
	if validDefaultBranchCommit(strings.Repeat("g", 40)) {
		t.Fatal("non-hex receipt SHA was accepted")
	}
}

func TestRunDefaultBranchReconcileNeverResendsNamedRemoteMutation(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead    func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute func(ctx context.Context, args ...string) githubobserver.CommandResponse
	)
	engine := &Engine{
		Git: &fakeGit{},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	projectsRoot := t.TempDir()
	ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	sha := "0123456789abcdef0123456789abcdef01234567"
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"master"}`), nil
		case "repos/acme/app/branches/master":
			return []byte(`{"commit":{"sha":"` + sha + `"}}`), nil
		case "repos/acme/app/branches/main":
			return nil, errors.New("HTTP 404")
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	mutations := 0
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{Err: errors.New("remote mutation must not run during reconcile")}
	}
	for name, test := range map[string]struct {
		repository string
		oldHead    string
	}{
		"accepted response still pending": {repository: "acme/app", oldHead: sha},
		"stale receipt":                   {repository: "acme/app", oldHead: strings.Repeat("a", 40)},
		"missing receipt repository":      {repository: "other/app", oldHead: sha},
	} {
		t.Run(name, func(t *testing.T) {
			prior := Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{{
				Repository: "acme/app", Disposition: "pending", Error: defaultBranchRenameResponsePendingError, RenameAccepted: true,
				ObservedDefault: "master", Desired: "main", OldHead: test.oldHead,
			}}}
			prior.Repositories[0].Repository = test.repository
			raw, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "receipt.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			mutations = 0
			report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: t.TempDir(), ReconcileFrom: path, ReconcileSHA256: defaultBranchDigest(raw)}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			if mutations != 0 || report.Repositories[0].Disposition != "blocked" || !strings.Contains(report.Repositories[0].Error, "remote read-only") {
				t.Fatalf("report=%#v mutations=%d", report.Repositories[0], mutations)
			}
		})
	}
}

func TestRunDefaultBranchResumesMacReceiptOnVMClone(t *testing.T) {
	originalConfigPath := ConfigPath
	t.Cleanup(func() { ConfigPath = originalConfigPath })
	var (
		defaultBranchRead             func(ctx context.Context, endpoint string) ([]byte, error)
		defaultBranchExecute          func(ctx context.Context, args ...string) githubobserver.CommandResponse
		defaultBranchGit              func(ctx context.Context, dir string, args ...string) (string, error)
		defaultBranchAtomicRenameRefs func(ctx context.Context, dir, source, destination, expected string) error
		defaultBranchAttachHead       func(ctx context.Context, dir, destination string) error
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}, atomicRenameRefsFunc: func(ctx context.Context, dir, source, destination, expected string) error {
			return defaultBranchAtomicRenameRefs(ctx, dir, source, destination, expected)
		}, attachHeadFunc: func(ctx context.Context, dir, destination string) error {
			return defaultBranchAttachHead(ctx, dir, destination)
		}},
		GitHub: &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }, executeFunc: func(ctx context.Context, args ...string) githubobserver.CommandResponse {
			return defaultBranchExecute(ctx, args...)
		}},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error { return nil }
	defaultBranchAttachHead = func(_ context.Context, _, _ string) error { return nil }
	projectsRoot := t.TempDir()
	clone := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	prior := Report{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", Disposition: "compliant", ObservedDefault: "master", VerifiedDefault: "main", Desired: "main", OldHead: "same", NewHead: "same", Actions: []string{"renamed master to main", "verified default branch and head"},
	}}}
	priorRaw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	priorPath := filepath.Join(t.TempDir(), "mac-apply.json")
	if err := os.WriteFile(priorPath, priorRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"same"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	remoteHead := "same"
	var calls []string
	defaultBranchGit = func(_ context.Context, dir string, args ...string) (string, error) {
		if dir != clone {
			return "", errors.New("unexpected clone " + dir)
		}
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch call {
		case "remote get-url origin":
			return "git@github.com:acme/app.git", nil
		case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H", "branch --set-upstream-to=origin/main main":
			return "", nil
		case "worktree list --porcelain":
			return "worktree " + clone + "\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "rev-parse origin/main", "rev-parse master", "rev-parse main", "rev-parse HEAD":
			return remoteHead, nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		case "branch -m master main":
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	report, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: t.TempDir(), ReconcileFrom: priorPath, ReconcileSHA256: defaultBranchDigest(priorRaw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Repositories[0].CanonicalClones; len(got) != 1 || got[0].Disposition != "compliant" || !strings.Contains(strings.Join(got[0].Actions, " "), "renamed local master") {
		t.Fatalf("VM reconciliation = %#v", got)
	}
	legacy := Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{{
		Repository: "acme/app", Disposition: "error", Error: defaultBranchLegacyRenamePostProofError, ObservedDefault: "master", Desired: "main", OldHead: "0123456789abcdef0123456789abcdef01234567",
	}}}
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(t.TempDir(), "mac-failed-post-proof.json")
	if err := os.WriteFile(legacyPath, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(`{"default_branch":"main"}`), nil
		case "repos/acme/app/branches/main":
			return []byte(`{"commit":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`), nil
		case "repos/acme/app/git/ref/heads/master":
			return nil, errors.New("HTTP 404")
		default:
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	remoteHead = "0123456789abcdef0123456789abcdef01234567"
	calls = nil
	recovered, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: t.TempDir(), ReconcileFrom: legacyPath, ReconcileSHA256: defaultBranchDigest(legacyRaw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.Repositories[0]; got.ObservedDefault != "master" || got.VerifiedDefault != "main" || got.NewHead != "0123456789abcdef0123456789abcdef01234567" || got.RecoveredFrom != legacyPath || got.RecoveredSHA256 != defaultBranchDigest(legacyRaw) || !defaultBranchMigrationActionsVerified(got) || len(got.CanonicalClones) != 1 || got.CanonicalClones[0].Disposition != "compliant" {
		t.Fatalf("legacy receipt recovery = %#v", got)
	}
	secondRaw, err := json.Marshal(recovered)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(t.TempDir(), "mac-recovered.json")
	if err := os.WriteFile(secondPath, secondRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	remoteMutations := 0
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		remoteMutations++
		return githubobserver.CommandResponse{Err: errors.New("unexpected remote mutation")}
	}
	calls = nil
	secondHop, err := engine.run(context.Background(), projectsRoot, "", Options{Apply: true, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: t.TempDir(), ReconcileFrom: secondPath, ReconcileSHA256: defaultBranchDigest(secondRaw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if remoteMutations != 0 || len(secondHop.Repositories[0].CanonicalClones) != 1 || secondHop.Repositories[0].CanonicalClones[0].Disposition != "compliant" {
		t.Fatalf("second hop=%#v remoteMutations=%d calls=%v", secondHop.Repositories[0], remoteMutations, calls)
	}
}

func TestDefaultBranchLocalClonesExcludesCrossForgeAndMismatchedOrigins(t *testing.T) {
	var (
		defaultBranchGit func(ctx context.Context, dir string, args ...string) (string, error)
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	projectsRoot := t.TempDir()
	githubClone := filepath.Join(projectsRoot, "acme", "app")
	legacyMirror := filepath.Join(projectsRoot, "other", "app")
	gitlabClone := filepath.Join(projectsRoot, "gitlab.com", "acme", "app")
	mismatchedClone := filepath.Join(projectsRoot, "github.com", "acme", "mismatch")
	unreadableClone := filepath.Join(projectsRoot, "github.com", "acme", "unreadable")
	for _, clone := range []string{githubClone, legacyMirror, gitlabClone, mismatchedClone, unreadableClone} {
		if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defaultBranchGit = func(_ context.Context, dir string, args ...string) (string, error) {
		if strings.Join(args, " ") != "remote get-url origin" {
			return "", errors.New("unexpected git call")
		}
		switch dir {
		case githubClone:
			return "git@github.com:acme/app.git", nil
		case legacyMirror:
			return "git@gitlab.com:other/app.git", nil
		case mismatchedClone:
			return "git@github.com:acme/other.git", nil
		case unreadableClone:
			return "", errors.New("origin is unavailable")
		default:
			return "", errors.New("cross-forge clone should not be queried")
		}
	}
	clones, err := engine.defaultBranchLocalClones(projectsRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := clones.Eligible["acme/app"]; len(got) != 1 || got[0].Path != githubClone {
		t.Fatalf("eligible clones = %#v", clones)
	}
	if got := clones.Blocked["acme/mismatch"]; len(got) != 1 || got[0].Path != mismatchedClone || got[0].Disposition != "blocked" {
		t.Fatalf("mismatched canonical origin was not visible: %#v", clones)
	}
	if got := clones.Blocked["acme/unreadable"]; len(got) != 1 || got[0].Path != unreadableClone || got[0].Disposition != "error" {
		t.Fatalf("unreadable github canonical origin was not visible: %#v", clones)
	}
}

func TestDefaultBranchLocalClonesFailsClosedForUnavailableDiscoveryAndMalformedOrigin(t *testing.T) {
	var (
		defaultBranchGit func(ctx context.Context, dir string, args ...string) (string, error)
	)
	engine := &Engine{
		Git: &fakeGit{runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			return defaultBranchGit(ctx, dir, args...)
		}},
		GitHub:    &fakeGitHub{},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	if clones, err := engine.defaultBranchLocalClones("", ""); err != nil || len(clones.Eligible) != 0 || len(clones.Blocked) != 0 {
		t.Fatalf("empty projects root = %#v err=%v", clones, err)
	}
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if _, err := engine.defaultBranchLocalClones(missingRoot, ""); err == nil || !strings.Contains(err.Error(), "scan local canonical clones") {
		t.Fatalf("unavailable projects root was accepted: %v", err)
	}
	projectsRoot := t.TempDir()
	malformed := filepath.Join(projectsRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Join(malformed, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	defaultBranchGit = func(_ context.Context, dir string, args ...string) (string, error) {
		if dir != malformed || strings.Join(args, " ") != "remote get-url origin" {
			return "", errors.New("unexpected git request")
		}
		return "https://github.com/acme/too/many/segments", nil
	}
	clones, err := engine.defaultBranchLocalClones(projectsRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := clones.Blocked["acme/app"]; len(got) != 1 || got[0].Disposition != "blocked" || !strings.Contains(got[0].Error, "does not prove") {
		t.Fatalf("malformed github origin was not blocked: %#v", clones)
	}
}

func TestDefaultBranchConfigAndExactScopeRejectMalformedInputs(t *testing.T) {
	engine := &Engine{Git: &fakeGit{}, GitHub: &fakeGitHub{}, Discovery: &fakeDiscovery{}, Clock: &fakeClock{}}
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("fleet: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDefaultBranchConfig(path); err == nil || !strings.Contains(err.Error(), "parse WB config") {
		t.Fatalf("malformed config was accepted: %v", err)
	}
	if _, _, err := engine.discoverDefaultBranchFleet("", nil, []string{"acme/app/extra"}, false, false); err == nil || !strings.Contains(err.Error(), "invalid --repo") {
		t.Fatalf("malformed exact repository scope was accepted: %v", err)
	}
}

func TestDefaultBranchSafetyAcceptsWhitespaceEmptyRulesArray(t *testing.T) {
	var (
		defaultBranchRead func(ctx context.Context, endpoint string) ([]byte, error)
	)
	engine := &Engine{
		Git:       &fakeGit{},
		GitHub:    &fakeGitHub{readFunc: func(ctx context.Context, endpoint string) ([]byte, error) { return defaultBranchRead(ctx, endpoint) }},
		Discovery: &fakeDiscovery{},
		Clock:     &fakeClock{},
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[]`), nil
		case "repos/acme/app/rules/branches/master?per_page=100":
			return []byte("[\n]"), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	result := Repository{Repository: "acme/app"}
	if err := engine.defaultBranchSafetyWithOptions(context.Background(), &result, defaultBranchRepoMetadata{}, "master", false); err != nil {
		t.Fatalf("whitespace empty rules array blocked migration: %v", err)
	}
}

func repo(slug string) discover.Repo {
	owner, name, _ := strings.Cut(slug, "/")
	return discover.Repo{Org: owner, Name: name}
}

func TestEffectiveDefaultBranchPrefersExplicitThenOrgThenFleet(t *testing.T) {
	var cfg defaultBranchConfig
	cfg.Fleet.DefaultBranch = "main"
	cfg.Fleet.Organizations = map[string]struct {
		DefaultBranch string `yaml:"default_branch"`
	}{"legacy": {DefaultBranch: "trunk"}}
	if got := effectiveDefaultBranch("", cfg, "LeGaCy"); got != "trunk" {
		t.Fatalf("org default = %q", got)
	}
	if got := effectiveDefaultBranch("release", cfg, "legacy"); got != "release" {
		t.Fatalf("explicit default = %q", got)
	}
	if got := effectiveDefaultBranch("", cfg, "unknown-org"); got != "main" {
		t.Fatalf("fleet default = %q", got)
	}
}

// TestHasFindingsCoversEveryFindingCategory exercises HasFindings directly:
// cmd/wb's own dispatch tests exercise it too (via defaultbranch.Run then
// defaultbranch.HasFindings), but that cross-package call does not count
// toward this package's own coverage profile, so the move needs this direct
// test to keep this exported entry point covered here.
func TestHasFindingsCoversEveryFindingCategory(t *testing.T) {
	if HasFindings(Report{}) {
		t.Fatal("empty summary reported findings")
	}
	for name, report := range map[string]Report{
		"drift":            {Summary: Summary{Drift: 1}},
		"blocked":          {Summary: Summary{Blocked: 1}},
		"errors":           {Summary: Summary{Errors: 1}},
		"canonicalBlocked": {Summary: Summary{CanonicalBlocked: 1}},
		"canonicalErrors":  {Summary: Summary{CanonicalErrors: 1}},
	} {
		if !HasFindings(report) {
			t.Fatalf("%s: HasFindings = false, want true for %#v", name, report)
		}
	}
}
