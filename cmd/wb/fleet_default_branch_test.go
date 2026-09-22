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
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

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

func TestFleetDefaultBranchHelpAndPolicyPrecedence(t *testing.T) {
	command := newFleetDefaultBranchCmd()
	for _, name := range []string{"apply", "branch", "org", "repo", "user", "all-orgs", "parallel", "report-dir", "reconcile-from", "reconcile-sha256", "temporarily-unarchive", "restore-archive-from", "restore-archive-sha256", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s", name)
		}
	}
	if !strings.Contains(command.Long, "read-only") || !strings.Contains(command.Long, "never rewrites") || !strings.Contains(command.Long, "accepted response") {
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
	originalRead, originalExecute, originalConfig, originalProjects := defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, projectsRoot
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, projectsRoot = originalRead, originalExecute, originalConfig, originalProjects
	})
	projectsRoot = t.TempDir()
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

func TestApplyDefaultBranchRenamesAndProvesResult(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	planned := inspectDefaultBranch(context.Background(), repo("acme/app"), "main")
	if planned.Disposition != "drift" || planned.OldHead != "source" {
		t.Fatalf("plan = %#v", planned)
	}
	result := applyDefaultBranch(context.Background(), planned)
	if result.Disposition != "compliant" || result.VerifiedDefault != "main" || result.NewHead != "source" {
		t.Fatalf("rename proof = %#v", result)
	}
	if got := strings.Join(result.Actions, "\n"); got != "renamed master to main\nverified default branch and head" {
		t.Fatalf("actions = %q", got)
	}
}

func TestApplyDefaultBranchWaitsForDelayedRenameVisibilityWithoutRetrying(t *testing.T) {
	originalRead, originalExecute, originalWait := defaultBranchRead, defaultBranchExecute, defaultBranchRenameWait
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchExecute, defaultBranchRenameWait = originalRead, originalExecute, originalWait
	})
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
	planned := inspectDefaultBranch(context.Background(), repo("acme/app"), "main")
	result := applyDefaultBranch(context.Background(), planned)
	if result.Disposition != "compliant" || result.VerifiedDefault != "main" || mutations != 1 || waits != 1 {
		t.Fatalf("delayed visibility result=%#v mutations=%d waits=%d", result, mutations, waits)
	}
}

func TestDefaultBranchRenameVisibilityTimeoutDoesNotSleepThroughItsDeadline(t *testing.T) {
	originalNow, originalWait, originalRead := defaultBranchRenameNow, defaultBranchRenameWait, defaultBranchRead
	t.Cleanup(func() {
		defaultBranchRenameNow, defaultBranchRenameWait, defaultBranchRead = originalNow, originalWait, originalRead
	})
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
	planned := defaultBranchRepository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "source"}
	result, err := waitForDefaultBranchRename(context.Background(), planned)
	if err != nil || result.Disposition != "drift" || result.ObservedDefault != "master" || result.OldHead != "source" || waits != 0 {
		t.Fatalf("timeout result=%#v err=%v waits=%d", result, err, waits)
	}
}

func TestApplyDefaultBranchCheckpointsProtectBothMutationBoundaries(t *testing.T) {
	for name, failAfterResponse := range map[string]bool{"before response": false, "after response": true} {
		t.Run(name, func(t *testing.T) {
			originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
			t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
			result := applyDefaultBranchWithCheckpoint(context.Background(), defaultBranchRepository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "source"}, func(repository defaultBranchRepository) error {
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
	original := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = original })
	planned := defaultBranchRepository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "source"}
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
			if result := readDefaultBranchRenameVisibility(context.Background(), planned, time.Now().Add(time.Second)); result.Disposition != test.want {
				t.Fatalf("visibility = %#v", result)
			}
		})
	}
}

func TestApplyDefaultBranchRefusesFreshPlanDrift(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	result := applyDefaultBranch(context.Background(), defaultBranchRepository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "planned"})
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
			original := defaultBranchRead
			t.Cleanup(func() { defaultBranchRead = original })
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) { return configure(endpoint) }
			result := defaultBranchRepository{Repository: "acme/app"}
			if err := defaultBranchSafety(context.Background(), &result, defaultBranchRepoMetadata{}, "master"); err == nil {
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
			original := defaultBranchRead
			t.Cleanup(func() { defaultBranchRead = original })
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
			result := defaultBranchRepository{Repository: "acme/app"}
			if err := defaultBranchSafety(context.Background(), &result, defaultBranchRepoMetadata{}, "master"); err == nil || !strings.Contains(err.Error(), want) {
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
			original := defaultBranchRead
			t.Cleanup(func() { defaultBranchRead = original })
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
			result := inspectDefaultBranch(context.Background(), repo("acme/app"), "main")
			if result.Disposition != "blocked" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("unsafe repository state = %#v", result)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalRefusesStaleRemoteHead(t *testing.T) {
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
			return "worktree /canonical\nbranch refs/heads/master", nil
		case "branch --show-current":
			return "master", nil
		case "rev-parse origin/main":
			return "advanced", nil
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
	entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
	if err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "planned", func() error { return nil }); err == nil || !strings.Contains(err.Error(), "not planned source") {
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
			originalGit, originalAncestor := defaultBranchGit, defaultBranchIsAncestor
			t.Cleanup(func() { defaultBranchGit, defaultBranchIsAncestor = originalGit, originalAncestor })
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
				case "rev-parse origin/main", "rev-parse master":
					return "same", nil
				case "for-each-ref --format=%(refname:strip=2) refs/heads":
					return "master", nil
				default:
					return "", errors.New("unexpected mutation " + call)
				}
			}
			repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error { return nil })
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
			original := defaultBranchGit
			t.Cleanup(func() { defaultBranchGit = original })
			defaultBranchGit = func(_ context.Context, _ string, args ...string) (string, error) {
				switch call := strings.Join(args, " "); call {
				case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "log --branches --not --remotes --format=%H", "branch -m master main":
					return "", nil
				case "worktree list --porcelain":
					return "worktree /canonical\nbranch refs/heads/master", nil
				case "branch --show-current":
					return "master", nil
				case "rev-parse origin/main", "rev-parse master":
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
			repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			checkpoints := 0
			err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error { checkpoints++; return nil })
			if upstreamError {
				if err == nil || entry.Disposition != "error" || len(entry.Actions) != 1 || checkpoints != 2 {
					t.Fatalf("partial reconciliation = %#v checkpoints=%d err=%v", entry, checkpoints, err)
				}
				return
			}
			if err != nil || entry.Disposition != "compliant" || len(entry.Actions) != 2 || checkpoints != 2 {
				t.Fatalf("reconciliation = %#v checkpoints=%d err=%v", entry, checkpoints, err)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalFastForwardsOnlyContainedSource(t *testing.T) {
	originalGit, originalAncestor := defaultBranchGit, defaultBranchIsAncestor
	t.Cleanup(func() { defaultBranchGit, defaultBranchIsAncestor = originalGit, originalAncestor })
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
		case "rev-parse master":
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
	repo := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
	entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
	var receipts []defaultBranchCanonical
	if err := reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "new", func() error {
		receipts = append(receipts, entry)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if entry.Disposition != "compliant" || strings.Join(entry.Actions, " | ") != "fast-forwarded local master to origin/main | renamed local master to main | set upstream to origin/main" {
		t.Fatalf("entry = %#v", entry)
	}
	if len(receipts) != 4 || !strings.Contains(strings.Join(receipts[0].Actions, " "), "old") || !strings.Contains(strings.Join(receipts[0].Actions, " "), "new") || !strings.Contains(strings.Join(receipts[1].Actions, " "), "fast-forwarded") || !strings.Contains(strings.Join(receipts[2].Actions, " "), "planned rename") {
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
			originalGit, originalAncestor := defaultBranchGit, defaultBranchIsAncestor
			t.Cleanup(func() { defaultBranchGit, defaultBranchIsAncestor = originalGit, originalAncestor })
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
			repo := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			err := reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "remote", func() error { return nil })
			if err == nil || !strings.Contains(err.Error(), map[bool]string{true: "has unpublished commits", false: "not contained"}[unpublished != ""]) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(strings.Join(calls, "\n"), "merge --ff-only") || strings.Contains(strings.Join(calls, "\n"), "branch -m") {
				t.Fatalf("diverged clone mutated: %v", calls)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalRefusesRefMovementBeforeRename(t *testing.T) {
	originalGit, originalAncestor := defaultBranchGit, defaultBranchIsAncestor
	t.Cleanup(func() { defaultBranchGit, defaultBranchIsAncestor = originalGit, originalAncestor })
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
		case "branch -m master main":
			return "", errors.New("rename must not run after ref movement")
		default:
			return "", errors.New("unexpected git " + call)
		}
	}
	repo := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
	entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
	checkpoints := 0
	err := reconcileDefaultBranchCanonical(context.Background(), &repo, &entry, "master", "new", func() error {
		checkpoints++
		if checkpoints == 3 {
			localHead, remoteHead = "moved", "moved"
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "before rename checkpoint new") {
		t.Fatalf("moved refs were accepted: %v", err)
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
		{name: "rename", failGitCall: "branch -m master main", want: "rename local default branch"},
		{name: "durable result", failCheckpoint: 2, want: "persist local-reconciliation receipt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := defaultBranchGit
			t.Cleanup(func() { defaultBranchGit = original })
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
			repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error {
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
	if result := applyDefaultBranch(context.Background(), defaultBranchRepository{Repository: "not-a-slug"}); result.Disposition != "error" || !strings.Contains(result.Error, "invalid repository") {
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
			originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
			t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
			result := applyDefaultBranch(context.Background(), defaultBranchRepository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "planned"})
			if result.Disposition != "error" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("unproven mutation = %#v", result)
			}
		})
	}
}

func TestDefaultBranchReportPathAndValidationGuardrails(t *testing.T) {
	originalProjects := projectsRoot
	t.Cleanup(func() { projectsRoot = originalProjects })
	projectsRoot = t.TempDir()
	path, err := defaultBranchReportPath("")
	if err != nil || !strings.Contains(path, "reports/default-branch/default-branch-") {
		t.Fatalf("default report path = %q err=%v", path, err)
	}
	if err := persistDefaultBranchReport(defaultBranchReport{ReportPath: filepath.Join(t.TempDir(), "missing", "report.json")}); err == nil {
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
			original := defaultBranchRead
			t.Cleanup(func() { defaultBranchRead = original })
			defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) { return test.read(endpoint) }
			result := inspectDefaultBranch(context.Background(), repo("acme/app"), "main")
			if result.Disposition != "error" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("untrusted observation = %#v", result)
			}
		})
	}
	if result := inspectDefaultBranch(context.Background(), repo("acme/app"), "bad ref"); result.Disposition != "blocked" || !strings.Contains(result.Error, "invalid desired branch") {
		t.Fatalf("invalid desired branch = %#v", result)
	}
}

func TestFleetDefaultBranchRejectsUnsafeFlagCombinations(t *testing.T) {
	originalProjects := projectsRoot
	t.Cleanup(func() { projectsRoot = originalProjects })
	for name, args := range map[string][]string{
		"missing apply scope":          {"fleet", "default-branch", "--apply"},
		"repo and org":                 {"fleet", "default-branch", "--repo", "acme/app", "--org", "acme"},
		"all orgs and org":             {"fleet", "default-branch", "--all-orgs", "--org", "acme"},
		"invalid parallel":             {"fleet", "default-branch", "--repo", "acme/app", "--parallel", "0"},
		"resume without apply":         {"fleet", "default-branch", "--reconcile-from", "receipt.json"},
		"digest without receipt":       {"fleet", "default-branch", "--reconcile-sha256", strings.Repeat("a", 64)},
		"receipt without valid digest": {"fleet", "default-branch", "--apply", "--repo", "acme/app", "--reconcile-from", "receipt.json"},
	} {
		t.Run(name, func(t *testing.T) {
			command := newRootCmd()
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatalf("unsafe flags were accepted: %v", args)
			}
		})
	}
}

func TestDefaultBranchReportPersistsAndPrintsCloneFindings(t *testing.T) {
	path, err := defaultBranchReportPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := defaultBranchReport{ReportPath: path, Desired: "main", Mode: "apply", Repositories: []defaultBranchRepository{{
		Repository: "acme/app", Disposition: "blocked", Error: "workflow review required",
		CanonicalClones: []defaultBranchCanonical{{Path: "/projects/acme/app", Disposition: "blocked", Error: "local changes present"}},
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
	if err := printDefaultBranchReport(&output, report); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "acme/app: blocked") || !strings.Contains(got, "/projects/acme/app: blocked") {
		t.Fatalf("printed report = %q", got)
	}
	for failAt := 1; failAt <= 5; failAt++ {
		writer := &defaultBranchFailWriter{failAt: failAt}
		if err := printDefaultBranchReport(writer, report); err == nil {
			t.Fatalf("output failure at write %d was ignored", failAt)
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
	originalFilter, originalProjects, originalConfig := filterFlag, projectsRoot, defaultBranchConfigPath
	t.Cleanup(func() {
		filterFlag, projectsRoot, defaultBranchConfigPath = originalFilter, originalProjects, originalConfig
	})
	testProjectsRoot := t.TempDir()
	defaultBranchConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--projects-root", testProjectsRoot, "--filter", "selected", "fleet", "default-branch", "--repo", "acme/other", "--branch", "main", "--json"})
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

func TestDefaultBranchArchiveRestoreIsDigestAndIdentityBound(t *testing.T) {
	prior := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{
		Repository: "acme/app", RepositoryID: 77, Desired: "main",
		Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FinalHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Phase: "failed", RecoveryRequired: true},
	}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	report, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, restoreArchiveFrom: path, restoreArchiveSHA256: defaultBranchDigest(raw), reportDir: t.TempDir()}, &bytes.Buffer{})
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
	if _, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, restoreArchiveFrom: path, restoreArchiveSHA256: defaultBranchDigest(raw), reportDir: t.TempDir()}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "repository ID") {
		t.Fatalf("wrong ID restore err = %v", err)
	}
	if mutations != 0 {
		t.Fatalf("wrong ID sent %d mutations", mutations)
	}
}

func TestApplyArchivedDefaultBranchRefreshesIdentityBeforeUnarchive(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Disposition: "drift", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := applyArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return nil })
	if result.Disposition != "blocked" || !strings.Contains(result.Error, "repository ID") || mutations != 0 {
		t.Fatalf("unsafe unarchive = %#v mutations=%d", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchRestoresAfterMigrationFailure(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Disposition: "drift", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := applyArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return nil })
	if !archived || len(mutations) != 3 || result.Archive.Phase != "restored" || result.Archive.RecoveryRequired || result.Disposition != "error" {
		t.Fatalf("failed migration did not restore archive: %#v mutations=%v", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchSwitchesSameHeadAndRestores(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NewHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TargetExists: true, Disposition: "drift", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := applyArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return nil })
	if !archived || branch != "main" || result.Disposition != "compliant" || result.Archive.Phase != "restored" || result.Archive.FinalHead != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(mutations) != 3 {
		t.Fatalf("same-head archived migration = %#v mutations=%v", result, mutations)
	}
}

func TestApplyArchivedDefaultBranchRestoresAfterUnarchiveFailure(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, ObservedDefault: "master", Desired: "main", OldHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Disposition: "drift", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := applyArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return nil })
	if result.Archive.Phase != "restored" || result.Archive.RecoveryRequired || result.Disposition != "error" || len(mutations) != 2 || !strings.Contains(mutations[1], "archived=true") {
		t.Fatalf("unarchive failure restore = %#v mutations=%v", result, mutations)
	}
}

func TestValidatePlannedArchivedDefaultBranchFailsClosed(t *testing.T) {
	originalRead := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = originalRead })
	transition := &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
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
			if err := validatePlannedArchivedDefaultBranch(context.Background(), test.metadata, "acme/app", transition); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("planned archive validation err = %v, want %q", err, test.want)
			}
		})
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		return nil, errors.New("branch unavailable: " + endpoint)
	}
	if err := validatePlannedArchivedDefaultBranch(context.Background(), defaultBranchRepoMetadata{ID: 77, Archived: true, DefaultBranch: "master"}, "acme/app", transition); err == nil || !strings.Contains(err.Error(), "read planned") {
		t.Fatalf("planned head read err = %v", err)
	}
}

func TestApplyArchivedDefaultBranchGuardsAndPreMutationCheckpoint(t *testing.T) {
	base := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	if result := applyArchivedDefaultBranch(context.Background(), defaultBranchRepository{Repository: "acme/app"}, func(defaultBranchRepository) error { return nil }); result.Disposition != "blocked" || !strings.Contains(result.Error, "stable") {
		t.Fatalf("missing archive proof = %#v", result)
	}
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
	defaultBranchRead = func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("metadata unavailable") }
	if result := applyArchivedDefaultBranch(context.Background(), base, func(defaultBranchRepository) error { return nil }); result.Disposition != "error" || !strings.Contains(result.Error, "refresh archived") {
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
	if result := applyArchivedDefaultBranch(context.Background(), base, func(defaultBranchRepository) error { return errors.New("disk full") }); result.Disposition != "error" || !strings.Contains(result.Error, "persist pending") || mutations != 0 {
		t.Fatalf("pre-mutation checkpoint failure = %#v mutations=%d", result, mutations)
	}
}

func TestRestoreArchivedDefaultBranchPersistsPreMutationReadFailure(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
	defaultBranchRead = func(_ context.Context, _ string) ([]byte, error) { return nil, errors.New("metadata unavailable") }
	mutations, checkpoints := 0, []defaultBranchRepository{}
	defaultBranchExecute = func(_ context.Context, _ ...string) githubobserver.CommandResponse {
		mutations++
		return githubobserver.CommandResponse{}
	}
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
	result := restoreArchivedDefaultBranch(context.Background(), repository, func(updated defaultBranchRepository) error {
		checkpoints = append(checkpoints, updated)
		return nil
	})
	if mutations != 0 || result.Archive.Phase != "failed" || !result.Archive.RecoveryRequired || len(checkpoints) != 2 || checkpoints[1].Archive.Phase != "failed" || !strings.Contains(result.Error, "refresh repository") {
		t.Fatalf("pre-restore read failure = %#v checkpoints=%#v mutations=%d", result, checkpoints, mutations)
	}
}

func TestApplyArchivedDefaultBranchRestoresAfterAcceptedUnarchiveCheckpointFailure(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := applyArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error {
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
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "prepared"}}
	result := applyArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return nil })
	if mutations != 1 || result.Archive.Phase != "failed" || !result.Archive.RecoveryRequired || !strings.Contains(result.Error, "repository ID changed") {
		t.Fatalf("post-unarchive identity change = %#v mutations=%d", result, mutations)
	}
}

func TestRestoreArchivedDefaultBranchRecordsActionAndFinalCheckpointFailure(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Disposition: "compliant", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
	result := restoreArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error {
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
	originalRead, originalExecute, originalConfig, originalProjects := defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, projectsRoot
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, projectsRoot = originalRead, originalExecute, originalConfig, originalProjects
	})
	projectsRoot = t.TempDir()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(config, []byte("fleet:\n  default_branch: main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultBranchConfigPath = func() string { return config }
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
	report, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, temporarilyUnarchive: true, parallel: 1, reportDir: t.TempDir()}, &bytes.Buffer{})
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
	if _, err := runDefaultBranch(context.Background(), defaultBranchOptions{restoreArchiveFrom: "missing.json", restoreArchiveSHA256: "bad"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "restore-archive-sha256") {
		t.Fatalf("invalid digest err = %v", err)
	}
	prior := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{Repository: "acme/app", RepositoryID: 77, Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runDefaultBranch(context.Background(), defaultBranchOptions{repositories: []string{"acme/missing"}, restoreArchiveFrom: path, restoreArchiveSHA256: defaultBranchDigest(raw)}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no archived") {
		t.Fatalf("wrong repository err = %v", err)
	}
	reportDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(reportDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runDefaultBranch(context.Background(), defaultBranchOptions{repositories: []string{"acme/app"}, restoreArchiveFrom: path, restoreArchiveSHA256: defaultBranchDigest(raw), reportDir: reportDir}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("unwritable report directory err = %v", err)
	}
}

func TestArchiveRestoreReceiptFailureEdges(t *testing.T) {
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
	transition := &defaultBranchArchive{RepositoryID: 77, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := validateDefaultBranchArchiveRestoreTarget(context.Background(), defaultBranchRepoMetadata{ID: 78, DefaultBranch: "master"}, "acme/app", transition); err == nil || !strings.Contains(err.Error(), "repository ID") {
		t.Fatalf("wrong restore target ID err = %v", err)
	}
	report := defaultBranchReport{ReportPath: filepath.Join(t.TempDir(), "missing", "receipt.json"), Repositories: []defaultBranchRepository{{Repository: "acme/app", Archive: transition}}}
	if _, err := failDefaultBranchArchiveRestore(&report, report.Repositories[0], "receipt persistence failure"); err == nil {
		t.Fatal("failed receipt persistence was accepted")
	}
	unchanged := defaultBranchRepository{Repository: "acme/app"}
	if got := restoreArchivedDefaultBranch(context.Background(), unchanged, func(defaultBranchRepository) error { return errors.New("must not checkpoint") }); got.Repository != unchanged.Repository || got.Archive != nil || got.Disposition != unchanged.Disposition || got.Error != unchanged.Error {
		t.Fatalf("nil archive transition changed repository: %#v", got)
	}
}

func TestRestoreArchivedDefaultBranchAttemptsRestoreAfterCheckpointFailure(t *testing.T) {
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
	repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Disposition: "error", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
	result := restoreArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return errors.New("disk full") })
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
	result = restoreArchivedDefaultBranch(context.Background(), repository, func(defaultBranchRepository) error { return nil })
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
			originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
			t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
			persisted := []defaultBranchRepository{}
			repository := defaultBranchRepository{Repository: "acme/app", RepositoryID: 77, Desired: "main", Disposition: "error", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "unarchived"}}
			result := restoreArchivedDefaultBranch(context.Background(), repository, func(updated defaultBranchRepository) error {
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
	transition := &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FinalHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	originalRead := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = originalRead })
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if endpoint == "repos/acme/app/branches/master" {
			return []byte(`{"commit":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`), nil
		}
		return nil, errors.New("unexpected endpoint " + endpoint)
	}
	if err := validateDefaultBranchArchiveRestoreTarget(context.Background(), defaultBranchRepoMetadata{ID: 77, DefaultBranch: "master"}, "acme/app", transition); err == nil || !strings.Contains(err.Error(), "pair") {
		t.Fatalf("cross-paired restore state was accepted: %v", err)
	}

	prior := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: transition}}}
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
	report, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, restoreArchiveFrom: path, restoreArchiveSHA256: defaultBranchDigest(raw), reportDir: t.TempDir()}, &bytes.Buffer{})
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

	valid := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{Repository: "acme/app", RepositoryID: 77, Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}
	if _, _, err := defaultBranchArchiveRestoreTarget(valid, "acme/missing"); err == nil || !strings.Contains(err.Error(), "no archived") {
		t.Fatalf("missing target err = %v", err)
	}
	invalid := valid
	invalid.Repositories = append([]defaultBranchRepository(nil), valid.Repositories...)
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
	prior := defaultBranchReport{SchemaVersion: defaultBranchSchemaVersion, Mode: "apply", Repositories: []defaultBranchRepository{{Repository: "acme/app", RepositoryID: 77, Desired: "main", Archive: &defaultBranchArchive{RepositoryID: 77, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Phase: "failed"}}}}
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	originalRead, originalExecute := defaultBranchRead, defaultBranchExecute
	t.Cleanup(func() { defaultBranchRead, defaultBranchExecute = originalRead, originalExecute })
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
			report, runErr := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, restoreArchiveFrom: path, restoreArchiveSHA256: defaultBranchDigest(raw), reportDir: t.TempDir()}, &bytes.Buffer{})
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

func TestDefaultBranchLegacyRenameResumeRequiresExactV1PostProofRecord(t *testing.T) {
	original := defaultBranchRead
	t.Cleanup(func() { defaultBranchRead = original })
	sha := "0123456789abcdef0123456789abcdef01234567"
	current := defaultBranchRepository{Repository: "acme/app", Disposition: "compliant", ObservedDefault: "main", Desired: "main", OldHead: sha, NewHead: sha}
	prior := &defaultBranchReport{SchemaVersion: 1, Mode: "apply", Repositories: []defaultBranchRepository{{
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
	if source, head, reason := defaultBranchLegacyRenameResume(context.Background(), prior, current); source != "master" || head != sha || reason != "" || reads != 1 {
		t.Fatalf("legacy resume = %q %q %q reads=%d", source, head, reason, reads)
	}
	responsePending := *prior
	responsePending.Repositories = append([]defaultBranchRepository(nil), prior.Repositories...)
	responsePending.Repositories[0].RenameAccepted = true
	responsePending.Repositories[0].Disposition = "error"
	responsePending.Repositories[0].Error = "read renamed target branch: context deadline exceeded"
	reads = 0
	if source, head, reason := defaultBranchLegacyRenameResume(context.Background(), &responsePending, current); source != "master" || head != sha || reason != "" || reads != 1 {
		t.Fatalf("pending response resume = %q %q %q reads=%d", source, head, reason, reads)
	}
	for name, mutate := range map[string]func(*defaultBranchReport, *defaultBranchRepository){
		"wrong schema":       func(r *defaultBranchReport, _ *defaultBranchRepository) { r.SchemaVersion = 2 },
		"changed head":       func(_ *defaultBranchReport, c *defaultBranchRepository) { c.OldHead = strings.Repeat("a", 40) },
		"malformed old head": func(r *defaultBranchReport, _ *defaultBranchRepository) { r.Repositories[0].OldHead = "short" },
		"target existed":     func(r *defaultBranchReport, _ *defaultBranchRepository) { r.Repositories[0].TargetExists = true },
		"forged action": func(r *defaultBranchReport, _ *defaultBranchRepository) {
			r.Repositories[0].Actions = []string{"renamed master to main"}
		},
		"wrong error": func(r *defaultBranchReport, _ *defaultBranchRepository) { r.Repositories[0].Error = "other" },
		"pre mutation": func(r *defaultBranchReport, _ *defaultBranchRepository) {
			r.Repositories[0].Disposition, r.Repositories[0].Error = "drift", "default-branch mutation pending"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateReport := *prior
			candidateReport.Repositories = append([]defaultBranchRepository(nil), prior.Repositories...)
			candidateCurrent := current
			mutate(&candidateReport, &candidateCurrent)
			reads = 0
			if source, head, reason := defaultBranchLegacyRenameResume(context.Background(), &candidateReport, candidateCurrent); source != "" || head != "" || reason == "" || reads != 0 {
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
	if source, head, reason := defaultBranchLegacyRenameResume(context.Background(), prior, current); source != "" || head != "" || !strings.Contains(reason, "old source ref still exists") {
		t.Fatalf("present old ref was accepted: %q %q %q", source, head, reason)
	}
	defaultBranchRead = func(_ context.Context, endpoint string) ([]byte, error) {
		if endpoint != "repos/acme/app/git/ref/heads/master" {
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
		return nil, errors.New("HTTP 503")
	}
	if source, head, reason := defaultBranchLegacyRenameResume(context.Background(), prior, current); source != "" || head != "" || !strings.Contains(reason, "could not prove") {
		t.Fatalf("unavailable old ref proof was accepted: %q %q %q", source, head, reason)
	}
	if source, head, reason := defaultBranchLegacyRenameResume(context.Background(), &defaultBranchReport{SchemaVersion: 1, Mode: "apply"}, current); source != "" || head != "" || !strings.Contains(reason, "no applied") {
		t.Fatalf("missing legacy receipt record was accepted: %q %q %q", source, head, reason)
	}
	if validDefaultBranchCommit(strings.Repeat("g", 40)) {
		t.Fatal("non-hex receipt SHA was accepted")
	}
}

func TestRunDefaultBranchReconcileNeverResendsNamedRemoteMutation(t *testing.T) {
	originalRead, originalExecute, originalConfig, originalProjects := defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, projectsRoot
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, projectsRoot = originalRead, originalExecute, originalConfig, originalProjects
	})
	projectsRoot = t.TempDir()
	defaultBranchConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
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
			prior := defaultBranchReport{SchemaVersion: 1, Mode: "apply", Repositories: []defaultBranchRepository{{
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
			report, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, branch: "main", parallel: 1, reportDir: t.TempDir(), reconcileFrom: path, reconcileSHA256: defaultBranchDigest(raw)}, &bytes.Buffer{})
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
	originalRead, originalExecute, originalConfig, originalGit, originalProjects := defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, defaultBranchGit, projectsRoot
	t.Cleanup(func() {
		defaultBranchRead, defaultBranchExecute, defaultBranchConfigPath, defaultBranchGit, projectsRoot = originalRead, originalExecute, originalConfig, originalGit, originalProjects
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
		case "rev-parse origin/main", "rev-parse master":
			return remoteHead, nil
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
	legacy := defaultBranchReport{SchemaVersion: 1, Mode: "apply", Repositories: []defaultBranchRepository{{
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
	recovered, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, branch: "main", parallel: 1, reportDir: t.TempDir(), reconcileFrom: legacyPath, reconcileSHA256: defaultBranchDigest(legacyRaw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.Repositories[0]; got.ObservedDefault != "master" || got.VerifiedDefault != "main" || got.NewHead != "0123456789abcdef0123456789abcdef01234567" || got.RecoveredFrom != legacyPath || got.RecoveredSHA256 != defaultBranchDigest(legacyRaw) || !defaultBranchMigrationActionsVerified(got) || len(got.CanonicalClones) != 1 || got.CanonicalClones[0].Disposition != "compliant" {
		t.Fatalf("legacy receipt recovery = %#v", got)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "branch -m master main") {
		t.Fatalf("legacy receipt did not reconcile the verified local branch: %v", calls)
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
	secondHop, err := runDefaultBranch(context.Background(), defaultBranchOptions{apply: true, repositories: []string{"acme/app"}, branch: "main", parallel: 1, reportDir: t.TempDir(), reconcileFrom: secondPath, reconcileSHA256: defaultBranchDigest(secondRaw)}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if remoteMutations != 0 || len(secondHop.Repositories[0].CanonicalClones) != 1 || secondHop.Repositories[0].CanonicalClones[0].Disposition != "compliant" || !strings.Contains(strings.Join(calls, "\n"), "branch -m master main") {
		t.Fatalf("second hop=%#v remoteMutations=%d calls=%v", secondHop.Repositories[0], remoteMutations, calls)
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

func TestDefaultBranchLocalClonesFailsClosedForUnavailableDiscoveryAndMalformedOrigin(t *testing.T) {
	originalGit, originalProjects := defaultBranchGit, projectsRoot
	t.Cleanup(func() { defaultBranchGit, projectsRoot = originalGit, originalProjects })
	projectsRoot = ""
	if clones, err := defaultBranchLocalClones(""); err != nil || len(clones.Eligible) != 0 || len(clones.Blocked) != 0 {
		t.Fatalf("empty projects root = %#v err=%v", clones, err)
	}
	projectsRoot = filepath.Join(t.TempDir(), "missing")
	if _, err := defaultBranchLocalClones(""); err == nil || !strings.Contains(err.Error(), "scan local canonical clones") {
		t.Fatalf("unavailable projects root was accepted: %v", err)
	}
	projectsRoot = t.TempDir()
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
	clones, err := defaultBranchLocalClones("")
	if err != nil {
		t.Fatal(err)
	}
	if got := clones.Blocked["acme/app"]; len(got) != 1 || got[0].Disposition != "blocked" || !strings.Contains(got[0].Error, "does not prove") {
		t.Fatalf("malformed github origin was not blocked: %#v", clones)
	}
}

func TestDefaultBranchConfigAndExactScopeRejectMalformedInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("fleet: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDefaultBranchConfig(path); err == nil || !strings.Contains(err.Error(), "parse WB config") {
		t.Fatalf("malformed config was accepted: %v", err)
	}
	if _, _, err := discoverDefaultBranchFleet("", nil, []string{"acme/app/extra"}, false, false); err == nil || !strings.Contains(err.Error(), "invalid --repo") {
		t.Fatalf("malformed exact repository scope was accepted: %v", err)
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
