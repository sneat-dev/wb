package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestFleetDefaultBranchHelpAndPolicyPrecedence(t *testing.T) {
	command := newFleetDefaultBranchCmd()
	for _, name := range []string{"apply", "branch", "org", "repo", "user", "all-orgs", "parallel", "report-dir", "reconcile-from", "reconcile-sha256", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	if !strings.Contains(command.Long, "read-only") || !strings.Contains(command.Long, "never rewrites") {
		t.Fatal("help omits safety contract")
	}
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
}

func TestInspectDefaultBranchRefusesArchivedAndDifferentTarget(t *testing.T) {
	original := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = original })
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
	archived := inspectDefaultBranch(context.Background(), repo("acme/old"), "main")
	if archived.Disposition != "blocked" || !strings.Contains(archived.Error, "archived") {
		t.Fatalf("archived = %#v", archived)
	}
	diverged := inspectDefaultBranch(context.Background(), repo("acme/diverged"), "main")
	if diverged.Disposition != "blocked" || !strings.Contains(diverged.Error, "different SHA") {
		t.Fatalf("diverged = %#v", diverged)
	}
}

func TestRunDefaultBranchSameSHAChangesOnlyDefaultAfterFreshProof(t *testing.T) {
	originalRead, originalExecute, originalConfig := defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath = originalRead, originalExecute, originalConfig
	})
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultBranchConfigPath = func() string { return config }
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
	report, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, parallel: 1, reportDir: t.TempDir()}, &bytes.Buffer{})
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
	var persistedReport defaultBranchReport
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
}

func TestDiscoverDefaultBranchFleetKeepsOwnerFailureAndDeduplicates(t *testing.T) {
	original := defaultBranchListRemote
	t.Cleanup(func() { defaultBranchListRemote = original })
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
	repos, failures, err := discoverDefaultBranchFleet("selected", []string{"broken", "good"}, nil, false, false)
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
	original := defaultBranchListRemote
	t.Cleanup(func() { defaultBranchListRemote = original })
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
	repos, failures, err := discoverDefaultBranchFleet("", []string{"large"}, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 || len(failures) != 1 || !strings.Contains(failures[0].Error, "1000 entries") {
		t.Fatalf("partial owner listing was not refused: repos=%#v failures=%#v", repos, failures)
	}
}

func TestFleetDefaultBranchRequiresDigestForReconciliationReceipt(t *testing.T) {
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fleet", "default-branch", "--repo", "acme/app", "--apply", "--reconcile-from", "receipt.json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--reconcile-sha256") {
		t.Fatalf("missing receipt digest was accepted: %v", err)
	}
}

func TestInspectDefaultBranchHandlesEmptyAndForkParentSafely(t *testing.T) {
	original := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = original })
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
	empty := inspectDefaultBranch(context.Background(), repo("acme/empty"), "main")
	if empty.Disposition != "blocked" || !strings.Contains(empty.Error, "empty repository") {
		t.Fatalf("empty = %#v", empty)
	}
	noInitialCommit := inspectDefaultBranch(context.Background(), repo("acme/no-initial-commit"), "main")
	if noInitialCommit.Disposition != "blocked" || !strings.Contains(noInitialCommit.Error, "no initial commit") {
		t.Fatalf("no initial commit = %#v", noInitialCommit)
	}
	fork := inspectDefaultBranch(context.Background(), repo("fork/app"), "main")
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
			original := defaultBranchRead
			t.Cleanup(func() { defaultBranchRead = original })
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
			result := inspectDefaultBranch(context.Background(), repo("fork/app"), "main")
			if result.Disposition != "blocked" || !strings.Contains(result.Error, "open pull request") {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalResumesAlreadyMainTracking(t *testing.T) {
	original := defaultBranchGit
	t.Cleanup(func() { defaultBranchGit = original })
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
	repo := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
	entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
	checkpoints := 0
	if err := reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "abc", func() error { checkpoints++; return nil }); err != nil {
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

func TestFleetDefaultBranchRootFilterRestrictsExactRepositoryScope(t *testing.T) {
	originalFilter, originalProjects := filterFlag, projectsRoot
	t.Cleanup(func() { filterFlag, projectsRoot = originalFilter, originalProjects })
	projectsRoot = ""
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--filter", "selected", "fleet", "default-branch", "--repo", "acme/other", "--branch", "main", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"inspected": 0`) {
		t.Fatalf("root --filter did not restrict exact scope: %s", out.String())
	}
}

func TestDefaultBranchResumeSourceRefusesMissingOrStaleBindings(t *testing.T) {
	current := defaultBranchRepository{Repository: "acme/app", ObservedDefault: "main", Desired: "main", OldHead: "current"}
	prior := &defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{
		Repository: "acme/app", Disposition: "compliant", ObservedDefault: "master", Desired: "main", VerifiedDefault: "main", OldHead: "old", NewHead: "old",
		Actions: []string{"renamed master to main", "verified default branch and head"},
	}}}
	if source, head, reason := defaultBranchResumeSource(prior, current); source != "" || head != "" || !strings.Contains(reason, "no longer matches") {
		t.Fatalf("stale resume = %q %q %q", source, head, reason)
	}
	if source, head, reason := defaultBranchResumeSource(&defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply"}, current); source != "" || head != "" || !strings.Contains(reason, "no applied migration record") {
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

func TestDefaultBranchResumeSourceRequiresVerifiedMigrationProof(t *testing.T) {
	current := defaultBranchRepository{Repository: "acme/app", ObservedDefault: "main", Desired: "main", OldHead: "same"}
	verified := defaultBranchRepository{
		Repository: "acme/app", Disposition: "compliant", ObservedDefault: "master", VerifiedDefault: "main", Desired: "main",
		OldHead: "same", NewHead: "same", Actions: []string{"renamed master to main", "verified default branch and head"},
	}
	if source, head, reason := defaultBranchResumeSource(&defaultBranchReport{Repositories: []defaultBranchRepository{verified}}, current); source != "master" || head != "same" || reason != "" {
		t.Fatalf("verified resume = %q %q %q", source, head, reason)
	}
	for name, mutate := range map[string]func(*defaultBranchRepository){
		"noncompliant":       func(v *defaultBranchRepository) { v.Disposition = "drift" },
		"unverified default": func(v *defaultBranchRepository) { v.VerifiedDefault = "" },
		"wrong head":         func(v *defaultBranchRepository) { v.NewHead = "other" },
		"forged action":      func(v *defaultBranchRepository) { v.Actions = []string{"renamed master to main"} },
		"same source":        func(v *defaultBranchRepository) { v.ObservedDefault = "main" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := verified
			mutate(&candidate)
			if source, head, reason := defaultBranchResumeSource(&defaultBranchReport{Repositories: []defaultBranchRepository{candidate}}, current); source != "" || head != "" || !strings.Contains(reason, "verified successful") {
				t.Fatalf("forged resume = %q %q %q", source, head, reason)
			}
		})
	}
}

func TestRunDefaultBranchResumesMacReceiptOnVMClone(t *testing.T) {
	originalRead, originalConfig, originalGit, originalProjects := defaultBranchRead, defaultBranchConfigPath, defaultBranchGit, projectsRoot
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchConfigPath, defaultBranchGit, projectsRoot = originalRead, originalConfig, originalGit, originalProjects
	})
	projectsRoot = t.TempDir()
	clone := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	prior := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{
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
	defaultBranchConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
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
		case "rev-parse origin/main", "rev-parse master":
			return "same", nil
		case "for-each-ref --format=%(refname:strip=2) refs/heads":
			return "master", nil
		case "branch -m master main":
			return "", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	report, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, branch: "main", parallel: 1, reportDir: t.TempDir(), reconcileFrom: priorPath, reconcileSHA256: defaultBranchDigest(priorRaw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Repositories[0].CanonicalClones; len(got) != 1 || got[0].Disposition != "compliant" || !strings.Contains(strings.Join(got[0].Actions, " "), "renamed local master") {
		t.Fatalf("VM reconciliation = %#v", got)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "branch -m master main") {
		t.Fatalf("VM did not rename its safe local branch: %v", calls)
	}
}

func TestDefaultBranchLocalClonesExcludesCrossForgeAndMismatchedOrigins(t *testing.T) {
	originalGit, originalProjects := defaultBranchGit, projectsRoot
	t.Cleanup(func() { defaultBranchGit, projectsRoot = originalGit, originalProjects })
	projectsRoot = t.TempDir()
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
	clones, err := defaultBranchLocalClones("")
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

func TestDefaultBranchSafetyAcceptsWhitespaceEmptyRulesArray(t *testing.T) {
	original := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = original })
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
	result := defaultBranchRepository{Repository: "acme/app"}
	if err := defaultBranchSafety(context.Background(), &result, defaultBranchRepoMetadata{}, "master"); err != nil {
		t.Fatalf("whitespace empty rules array blocked migration: %v", err)
	}
}

func repo(slug string) discover.Repo {
	owner, name, _ := strings.Cut(slug, "/")
	return discover.Repo{Org: owner, Name: name}
}
