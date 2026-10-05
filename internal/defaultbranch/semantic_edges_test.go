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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestMetadataAndPagesFailuresPreserveSafetyRefusals(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"{", `{"id":0,"default_branch":"main"}`, `{"id":1,"default_branch":".."}`} {
		service := New()
		service.deps.Read = func(context.Context, string) ([]byte, error) { return []byte(body), nil }
		if _, err := service.readDefaultBranchMetadata(t.Context(), "acme/app"); err == nil {
			t.Fatal(body)
		}
	}
	for _, kind := range []string{"read", "decode"} {
		service := New()
		service.deps.Read = func(context.Context, string) ([]byte, error) {
			if kind == "read" {
				return nil, errors.New("Pages unavailable")
			}
			return []byte("{"), nil
		}
		repo := Repository{Repository: "acme/app"}
		if err := service.inspectDefaultBranchPages(t.Context(), &repo, "master", true); err == nil {
			t.Fatal(kind)
		}
	}
	service := New()
	service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.HasSuffix(endpoint, "/branches/master") {
			return []byte(`{"commit":{"sha":"same"}}`), nil
		}
		return []byte(`{"default_branch":"master","archived":true}`), nil
	}
	report := service.inspectDefaultBranchWithOptions(t.Context(), discover.Repo{Org: "acme", Name: "app"}, "main", true, false)
	if report.Disposition != "blocked" || !strings.Contains(report.Error, "stable numeric ID") {
		t.Fatal(report)
	}
}
func TestWorkflowScalarAndBoundaryGrammar(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "123", "a?"} {
		if workflowPlainFlowBranchScalar(value) {
			t.Fatal(value)
		}
	}
	source := "on:\n  push:\n    branches:\n      - master\n    paths:\n      - master\n"
	got, changed := rewriteWorkflowBranchTriggers(source, "master", "main")
	if !changed || got != "on:\n  push:\n    branches:\n      - main\n    paths:\n      - master\n" {
		t.Fatal(got, changed)
	}
}
func TestResumeReceiptSelectionAndSummaryRetainTerminalProof(t *testing.T) {
	t.Parallel()
	if old, head, err := defaultBranchResumeSource(nil, Repository{}); old != "" || head != "" || err != "" {
		t.Fatal(old, head, err)
	}
	prior := &Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{{Repository: "acme/other"}}}
	current := Repository{Repository: "acme/app", Desired: "main", ObservedDefault: "main", Disposition: "compliant", OldHead: "same"}
	if _, _, err := defaultBranchResumeSource(prior, current); !strings.Contains(err, "no applied") {
		t.Fatal(err)
	}
	service := New()
	if _, _, err := service.defaultBranchLegacyRenameResume(t.Context(), prior, current); !strings.Contains(err, "no applied") {
		t.Fatal(err)
	}
	before := &PagesSource{BuildType: "legacy", Branch: "master", Path: "/docs"}
	after := &PagesSource{BuildType: "legacy", Branch: "main", Path: "/docs"}
	previous := Repository{RepositoryID: 1, ObservedDefault: "master", Desired: "main", TargetExists: true, PagesBefore: before, PagesAfter: after, PagesPhase: "verified", PagesAccepted: true, Actions: []string{"set default branch to existing same-SHA main", "verified default branch and head", "migrated Pages source to main with path /docs", "verified Pages source"}}
	current.RepositoryID = 1
	current.PagesAfter = after
	if !defaultBranchVerifiedPagesMigrationReceipt(previous, current) {
		t.Fatal("same-SHA Pages proof rejected")
	}
	report := Report{Repositories: []Repository{{CanonicalClones: []Canonical{{Disposition: "error"}}}}}
	summarizeDefaultBranch(&report)
	if report.Summary.CanonicalErrors != 1 || !HasFindings(report) || HasFindings(Report{}) {
		t.Fatal(report)
	}
	raw, _ := json.Marshal(Report{SchemaVersion: 0, Mode: "audit"})
	path := filepath.Join(t.TempDir(), "receipt")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDefaultBranchReport(path, defaultBranchDigest(raw)); err == nil {
		t.Fatal("wrong schema accepted")
	}
}
func TestRestoreArchivedFailureMatrixKeepsAmbiguousMutationAndCheckpointEvidence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"before", "execute", "identity", "head"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			reads, mutations, checkpoints := 0, 0, 0
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				if strings.Contains(endpoint, "/branches/") {
					return []byte(`{"commit":{"sha":"changed"}}`), nil
				}
				reads++
				if kind == "before" {
					return nil, errors.New("metadata unavailable")
				}
				if reads == 1 {
					return []byte(`{"id":1,"default_branch":"master"}`), nil
				}
				if kind == "execute" {
					return []byte(`{"id":1,"default_branch":"master"}`), nil
				}
				if kind == "identity" {
					return []byte(`{"id":2,"default_branch":"master","archived":true}`), nil
				}
				return []byte(`{"id":1,"default_branch":"master","archived":true}`), nil
			}
			service.deps.Execute = func(context.Context, ...string) githubobserver.CommandResponse {
				mutations++
				if kind == "execute" {
					return githubobserver.CommandResponse{Err: errors.New("lost reply"), Stderr: []byte("denied")}
				}
				return githubobserver.CommandResponse{}
			}
			repo := Repository{Repository: "acme/app", Desired: "main", Disposition: "compliant", Archive: &Archive{RepositoryID: 1, InitialDefault: "master", InitialHead: "same"}}
			result := service.restoreArchivedDefaultBranch(t.Context(), repo, func(Repository) error {
				checkpoints++
				if checkpoints == 2 {
					return errors.New("receipt refused")
				}
				return nil
			})
			if result.Disposition != "error" || !result.Archive.RecoveryRequired || !strings.Contains(result.Error, "persist failed archive restoration: receipt refused") {
				t.Fatal(kind, result)
			}
			if kind == "before" && mutations != 0 {
				t.Fatal("mutation before metadata proof")
			}
		})
	}
}
func TestDefaultBranchConfigReadErrorAndSortedOwnerFailures(t *testing.T) {
	t.Parallel()
	service := New()
	service.deps.ConfigPath = func() string { return t.TempDir() }
	if _, err := loadDefaultBranchConfig(service.deps.ConfigPath()); err == nil || !strings.Contains(err.Error(), "read WB config") {
		t.Fatal(err)
	}
	service.deps.ListRemote = func(string) ([]discover.Repo, error) { return nil, errors.New("denied") }
	_, failures, err := service.discoverDefaultBranchFleet("", []string{"zebra", "acme"}, nil, false, false)
	if err != nil || len(failures) != 2 || failures[0].Repository != "acme/*" {
		t.Fatal(failures, err)
	}
}
func TestReportDirectoryReadFailureOccursAfterActualAtomicPublish(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	report := Report{ReportPath: filepath.Join(dir, "report.json")}
	inj := &filewrite.Injector{Step: filewrite.StepRename, Hook: func() {
		if err := os.Chmod(dir, 0o300); err != nil {
			t.Fatal(err)
		}
	}}
	if err := persistDefaultBranchReportInjected(report, inj); err == nil {
		t.Fatal("unreadable directory accepted")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(report.ReportPath); err != nil {
		t.Fatal("published receipt lost", err)
	}
}

func TestRunCheckpointFailuresKeepPartialReceiptsAndNeverContinue(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"plain", "pages-only", "pages-rename", "unsupported", "audit"} {
		for failure := 1; failure <= 10; failure++ {
			t.Run(mode+strconv.Itoa(failure), func(t *testing.T) {
				t.Parallel()
				service := New()
				service.deps.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent") }
				branch := "master"
				if mode == "pages-only" || mode == "unsupported" {
					branch = "main"
				}
				source := "master"
				mutations := 0
				service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
					switch {
					case endpoint == "repos/acme/app":
						return []byte(fmt.Sprintf(`{"id":1,"default_branch":%q}`, branch)), nil
					case strings.Contains(endpoint, "/branches/") && !strings.HasSuffix(endpoint, "/protection") && !strings.Contains(endpoint, "/rules/"):
						return []byte(`{"commit":{"sha":"same"}}`), nil
					case strings.HasSuffix(endpoint, "/pages"):
						if mode == "plain" || mode == "audit" {
							return nil, errors.New("HTTP 404")
						}
						value := source
						if mode == "unsupported" {
							value = "trunk"
						}
						return []byte(fmt.Sprintf(`{"build_type":"legacy","source":{"branch":%q,"path":"/docs"}}`, value)), nil
					case strings.Contains(endpoint, "/pulls?") || strings.Contains(endpoint, "/rules/") || strings.Contains(endpoint, "/contents/"):
						return []byte(`[]`), nil
					case strings.HasSuffix(endpoint, "/protection"):
						return nil, errors.New("HTTP 404")
					}
					t.Fatal(endpoint)
					return nil, nil
				}
				service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
					mutations++
					if strings.Contains(strings.Join(args, " "), "/pages") {
						source = "main"
					} else {
						branch = "main"
					}
					return githubobserver.CommandResponse{}
				}
				sentinel := errors.New("checkpoint refused")
				calls := 0
				lastMutation := 0
				actual := service.deps.Persist
				service.deps.Persist = func(report Report) error {
					calls++
					if calls >= failure {
						lastMutation = mutations
						return sentinel
					}
					return actual(report)
				}
				request := Request{Scope: Scope{ProjectsRoot: t.TempDir()}, Options: Options{Apply: mode != "audit", Branch: "main", MigratePagesSource: mode != "plain" && mode != "audit", Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}}
				report, err := service.Run(t.Context(), request, &bytes.Buffer{})
				if calls >= failure {
					if !errors.Is(err, sentinel) || mutations != lastMutation || report.ReportPath == "" {
						t.Fatal(mode, failure, calls, mutations, lastMutation, report, err)
					}
				} else if err != nil {
					t.Fatal(mode, failure, report, err)
				}
			})
		}
	}
}
func TestRenameVisibilityRefusesAThirdDefaultAndCanonicalFetchFailure(t *testing.T) {
	t.Parallel()
	service := New()
	service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "/branches/") {
			return []byte(`{"commit":{"sha":"same"}}`), nil
		}
		return []byte(`{"id":1,"default_branch":"trunk"}`), nil
	}
	planned := Repository{Repository: "acme/app", ObservedDefault: "master", Desired: "main", OldHead: "same"}
	observed := service.readDefaultBranchRenameVisibility(t.Context(), planned, time.Now().Add(time.Second))
	if observed.Disposition != "blocked" || !strings.Contains(observed.Error, "default changed") {
		t.Fatal(observed)
	}
	service.deps.Git = func(context.Context, string, ...string) (string, error) { return "", errors.New("fetch denied") }
	service.reconcileDefaultBranchCanonicals(t.Context(), &planned, []discover.Repo{{Path: "checkout"}}, "master", "same", func() error { return nil })
	if len(planned.CanonicalClones) != 1 || !strings.Contains(planned.CanonicalClones[0].Error, "fetch denied") {
		t.Fatal(planned)
	}
}
func TestLocalCloneSelectionFiltersAndOrdersDuplicateCanonicalPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{"acme/app", "github.com/acme/app", "beta/other"} {
		dir := filepath.Join(root, path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		scratchGit(t, dir, "init", "-b", "main")
		slug := "acme/app"
		if strings.HasPrefix(path, "beta/") {
			slug = "beta/other"
		}
		scratchGit(t, dir, "remote", "add", "origin", "https://github.com/"+slug+".git")
	}
	service := New()
	clones, err := service.defaultBranchLocalClones(Scope{ProjectsRoot: root}, "acme/")
	if err != nil || len(clones.Eligible["acme/app"]) != 2 || len(clones.Eligible) != 1 || clones.Eligible["acme/app"][0].Path > clones.Eligible["acme/app"][1].Path {
		t.Fatal(clones, err)
	}
}
func TestArchiveRestoreCheckpointMatrixPreservesPartialRecoveryReceipts(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"already", "active", "metadata fail", "verify fail", "head fail"} {
		for failure := 0; failure <= 3; failure++ {
			t.Run(mode+strconv.Itoa(failure), func(t *testing.T) {
				t.Parallel()
				service := New()
				dir := t.TempDir()
				previous := Report{SchemaVersion: 1, Mode: "apply", Repositories: []Repository{{Repository: "acme/app", RepositoryID: 1, Desired: "main", Archive: &Archive{RepositoryID: 1, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: strings.Repeat("a", 40)}}}}
				raw, _ := json.Marshal(previous)
				receipt := filepath.Join(dir, "prior.json")
				if err := os.WriteFile(receipt, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				reads, mutations, calls := 0, 0, 0
				service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
					if strings.Contains(endpoint, "/branches/") {
						if mode == "head fail" {
							return nil, errors.New("head unavailable")
						}
						return []byte(`{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`), nil
					}
					reads++
					if mode == "metadata fail" || mode == "verify fail" && reads > 1 {
						return nil, errors.New("metadata unavailable")
					}
					archived := mode == "already" || reads > 1
					return []byte(fmt.Sprintf(`{"id":1,"default_branch":"master","archived":%t}`, archived)), nil
				}
				service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
					mutations++
					if strings.Join(args, " ") != "api --method PATCH repos/acme/app -f archived=true" {
						t.Fatal(args)
					}
					return githubobserver.CommandResponse{}
				}
				sentinel := errors.New("receipt refused")
				actual := service.deps.Persist
				service.deps.Persist = func(report Report) error {
					calls++
					if failure > 0 && calls >= failure {
						return sentinel
					}
					return actual(report)
				}
				report, err := service.Run(t.Context(), Request{Options: Options{Repositories: []string{"acme/app"}, RestoreArchiveFrom: receipt, RestoreArchiveSHA256: defaultBranchDigest(raw), ReportDir: filepath.Join(dir, "reports")}}, &bytes.Buffer{})
				if failure > 0 && calls >= failure {
					if !errors.Is(err, sentinel) {
						t.Fatal(mode, failure, calls, report, err)
					}
				} else if mode == "metadata fail" || mode == "verify fail" || mode == "head fail" {
					if err == nil {
						t.Fatal(mode, report)
					}
				} else if err != nil || report.Repositories[0].Archive.Phase != "restored" {
					t.Fatal(mode, report, err)
				}
				if (mode == "already" || mode == "metadata fail" || mode == "head fail") && mutations != 0 {
					t.Fatal("mutation without proof", mode, mutations)
				}
			})
		}
	}
}
func TestRunReportPathFailuresPreserveInspectedReport(t *testing.T) {
	t.Parallel()
	for _, apply := range []bool{false, true} {
		service := New()
		service.deps.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent") }
		service.deps.Read = func(context.Context, string) ([]byte, error) { return nil, errors.New("metadata unavailable") }
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := service.Run(t.Context(), Request{Options: Options{Apply: apply, Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1, ReportDir: filepath.Join(blocker, "reports")}}, &bytes.Buffer{})
		if err == nil || len(report.Repositories) != 1 || report.Repositories[0].Error != "metadata unavailable" {
			t.Fatal(apply, report, err)
		}
	}
}

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
func TestReportPathHomeAndReservationRemovalFailures(t *testing.T) {
	//nolint:paralleltest // This row or a sibling exercises process-wide environment selected by the native fixture.
	t.Run("home", func(t *testing.T) {
		t.Setenv(wbhome.EnvOverride, "")
		t.Setenv("HOME", "")
		if _, err := defaultBranchReportPath(Scope{}, ""); err == nil {
			t.Fatal("missing home accepted")
		}
	})
	//nolint:paralleltest // This row or a sibling exercises process-wide environment selected by the native fixture.
	t.Run("reservation removal", func(t *testing.T) {
		dir := t.TempDir()
		inj := &filewrite.Injector{Step: filewrite.StepClose, Hook: func() {
			matches, err := filepath.Glob(filepath.Join(dir, "default-branch-*.json"))
			if err != nil || len(matches) != 1 {
				t.Fatal(matches, err)
			}
			if err := os.Remove(matches[0]); err != nil {
				t.Fatal(err)
			}
		}}
		if path, err := defaultBranchReportPathInjected(Scope{}, dir, inj); path != "" || !errors.Is(err, os.ErrNotExist) {
			t.Fatal(path, err)
		}
	})
}
func TestArchivedFreshObservationAndWorkflowFailureRestoreWithoutRename(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"invalid observation", "fresh compliant", "workflow failure"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			service := New()
			archived := true
			branch := "master"
			mutations := []string{}
			slug := "acme/app"
			if kind == "invalid observation" {
				slug = "invalid"
			}
			head := strings.Repeat("a", 40)
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				switch {
				case endpoint == "repos/"+slug:
					return []byte(fmt.Sprintf(`{"id":1,"default_branch":%q,"archived":%t}`, branch, archived)), nil
				case strings.Contains(endpoint, "/branches/") && !strings.Contains(endpoint, "/rules/") && !strings.HasSuffix(endpoint, "/protection"):
					return []byte(fmt.Sprintf(`{"commit":{"sha":%q}}`, head)), nil
				case strings.Contains(endpoint, "/contents/"):
					if kind == "workflow failure" {
						return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
					}
					return []byte(`[]`), nil
				case strings.Contains(endpoint, "/git/blobs/"):
					return []byte(fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("on:\n  push:\n    branches: [master]\n")))), nil
				case strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection"):
					return nil, errors.New("HTTP 404")
				default:
					return []byte(`[]`), nil
				}
			}
			service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				call := strings.Join(args, " ")
				mutations = append(mutations, call)
				if strings.Contains(call, "archived=false") {
					archived = false
					if kind == "fresh compliant" {
						branch = "main"
					}
					return githubobserver.CommandResponse{}
				}
				if strings.Contains(call, "archived=true") {
					archived = true
					return githubobserver.CommandResponse{}
				}
				if strings.Contains(call, "graphql") {
					return githubobserver.CommandResponse{Err: errors.New("workflow write refused")}
				}
				t.Fatal("unexpected rename", call)
				return githubobserver.CommandResponse{}
			}
			planned := Repository{Repository: slug, RepositoryID: 1, Desired: "main", ObservedDefault: "master", OldHead: head, Disposition: "drift", Archive: &Archive{RepositoryID: 1, OriginalArchived: true, InitialDefault: "master", DesiredDefault: "main", InitialHead: head}}
			if kind == "workflow failure" {
				planned.WorkflowFiles = []Workflow{{Path: ".github/workflows/ci.yml"}}
			}
			result := service.applyArchivedDefaultBranch(t.Context(), planned, func(Repository) error { return nil })
			if result.Disposition != "error" || !archived || len(mutations) < 2 || !strings.Contains(mutations[len(mutations)-1], "archived=true") {
				t.Fatal(kind, result, mutations)
			}
		})
	}
}
func TestWorkflowSafetyRequiresExplicitRewritePermission(t *testing.T) {
	t.Parallel()
	service := New()
	service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
		if strings.Contains(endpoint, "/contents/") {
			return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
		}
		if strings.Contains(endpoint, "/git/blobs/") {
			return []byte(fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("on:\n  push:\n    branches: [master]\n")))), nil
		}
		return []byte(`[]`), nil
	}
	repo := Repository{Repository: "acme/app", Desired: "main"}
	if err := service.defaultBranchSafetyWithOptions(t.Context(), &repo, RepoMetadata{}, "master", false); err == nil || !strings.Contains(err.Error(), "pass --rewrite-workflow-triggers") {
		t.Fatal(repo, err)
	}
}
func TestRunProgressFailureIsReturnedAfterVerifiedMutation(t *testing.T) {
	t.Parallel()
	service := New()
	service.deps.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent") }
	defaultBranchPagesFixture(service, t, "/docs", "legacy", "master", false, false)
	report, err := service.Run(t.Context(), Request{Scope: Scope{ProjectsRoot: t.TempDir()}, Options: Options{Apply: true, Branch: "main", MigratePagesSource: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}}, progressErrorWriter{})
	if err == nil || !strings.Contains(err.Error(), "progress refused") || len(report.Repositories) != 1 || report.Repositories[0].Disposition != "compliant" || report.Repositories[0].PagesPhase != "verified" {
		t.Fatal(report, err)
	}
}

type progressErrorWriter struct{}

func (progressErrorWriter) Write([]byte) (int, error) { return 0, errors.New("progress refused") }
func TestWorkflowAndArchivedRunCheckpointErrorsKeepActualPartialMutationReceipts(t *testing.T) {
	t.Parallel()
	for _, archived := range []bool{false, true} {
		for failure := 1; failure <= 14; failure++ {
			t.Run(strconv.FormatBool(archived)+strconv.Itoa(failure), func(t *testing.T) {
				t.Parallel()
				service, mutations := workflowServiceFixture(t, archived)
				service.deps.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent") }
				actual := service.deps.Persist
				sentinel := errors.New("checkpoint refused")
				calls := 0
				service.deps.Persist = func(report Report) error {
					calls++
					if calls >= failure {
						return sentinel
					}
					return actual(report)
				}
				report, err := service.Run(t.Context(), Request{Scope: Scope{ProjectsRoot: t.TempDir()}, Options: Options{Apply: true, Branch: "main", RewriteWorkflowTriggers: true, TemporarilyUnarchive: archived, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: t.TempDir()}}, &bytes.Buffer{})
				if calls >= failure {
					if !errors.Is(err, sentinel) || report.ReportPath == "" {
						t.Fatal(failure, calls, report, err)
					}
				} else if err != nil || len(report.Repositories) != 1 || report.Repositories[0].Disposition != "compliant" {
					t.Fatal(failure, calls, report, err)
				}
				for _, call := range mutations() {
					if strings.Contains(call, "archived=") && !archived {
						t.Fatal("unexpected archive mutation", call)
					}
				}
			})
		}
	}
}
