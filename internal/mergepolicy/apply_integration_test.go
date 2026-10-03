package mergepolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCwDepsMergePolicyApplyWithStubbedGitHub(t *testing.T) {
	service := New()

	t.Setenv(wbhome.EnvOverride, t.TempDir())
	ReportDir := filepath.Join(t.TempDir(), "reports")
	calls := cwDepsStubMergePolicyGitHub(service, t, nil)

	observed, err := service.Run(t.Context(), Request{Scope: Scope{ProjectsRoot: t.TempDir()}, Options: Options{Apply: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: ReportDir}}, &bytes.Buffer{})
	raw, encodeErr := json.Marshal(observed)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	stdout := string(raw)
	if err != nil {
		t.Fatalf("merge-policy --apply: %v\n%s", err, stdout)
	}
	var report Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("merge-policy JSON: %v\n%s", err, stdout)
	}
	if report.Mode != "apply" || report.Summary.Applied != 1 || report.Summary.Compliant != 0 {
		t.Fatalf("applied report = %+v", report)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Disposition != "applied" {
		t.Fatalf("repository = %+v", report.Repositories)
	}
	for _, want := range []string{"updated_repository_merge_settings", "removed_classic_required_linear_history"} {
		found := false
		for _, action := range report.Repositories[0].AppliedActions {
			if action == want {
				found = true
			}
		}
		if !found {
			t.Errorf("applied actions %v missing %q", report.Repositories[0].AppliedActions, want)
		}
	}
	// Both repository rulesets carry policy that has to change: one requires
	// linear history and one only admits squash merges.
	if len(report.Rulesets) != 2 {
		t.Fatalf("ruleset changes = %+v", report.Rulesets)
	}
	for _, change := range report.Rulesets {
		if change.Disposition != "applied" {
			t.Errorf("ruleset change = %+v", change)
		}
	}
	if _, statErr := os.Stat(filepath.Join(ReportDir, "merge-policy.json")); statErr != nil {
		t.Errorf("the applied report was not persisted: %v", statErr)
	}
	if calls == nil || len(*calls) == 0 {
		t.Error("the GitHub read seam was never exercised")
	}
}
func TestCwDepsMergePolicyApplyBlocksARepositoryThatChanged(t *testing.T) {
	service := New()

	t.Setenv(wbhome.EnvOverride, t.TempDir())
	// The repository settings differ on the re-read inside the apply phase, so
	// the plan is refused rather than applied against a moved target.
	cwDepsStubMergePolicyGitHub(service, t, func(call int, endpoint string) []byte {
		if endpoint == "repos/acme/app" && call > 2 {
			return cwDepsMergePolicyPolicyBody(false)
		}
		return nil
	})
	observed, err := service.Run(t.Context(), Request{Scope: Scope{ProjectsRoot: t.TempDir()}, Options: Options{Apply: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: ""}}, &bytes.Buffer{})
	raw, encodeErr := json.Marshal(observed)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	stdout := string(raw)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Summary.Blocked == 0 {
		t.Fatalf("a repository that changed after planning must be blocked:\n%s", stdout)
	}
	var report Report
	if jsonErr := json.Unmarshal([]byte(stdout), &report); jsonErr != nil {
		t.Fatalf("merge-policy JSON: %v\n%s", jsonErr, stdout)
	}
	if report.Summary.Blocked == 0 {
		t.Fatalf("report = %+v", report.Summary)
	}
}
func TestCwDepsMergePolicyApplyReportsAMutationFailure(t *testing.T) {
	service := New()

	t.Setenv(wbhome.EnvOverride, t.TempDir())
	previousExecute := service.deps.Execute
	t.Cleanup(func() { service.deps.Execute = previousExecute })
	cwDepsStubMergePolicyGitHub(service, t, nil)
	service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		if len(args) > 2 && args[2] == "PATCH" {
			return githubobserver.CommandResponse{Stderr: []byte("gh: patch refused"), Err: errors.New("exit status 1")}
		}
		return githubobserver.CommandResponse{}
	}
	observed, err := service.Run(t.Context(), Request{Scope: Scope{ProjectsRoot: t.TempDir()}, Options: Options{Apply: true, Repositories: []string{"acme/app"}, Parallel: 1, ReportDir: ""}}, &bytes.Buffer{})
	raw, encodeErr := json.Marshal(observed)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	stdout := string(raw)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Summary.Errors == 0 {
		t.Fatalf("a refused mutation must be an error:\n%s", stdout)
	}
	var report Report
	if jsonErr := json.Unmarshal([]byte(stdout), &report); jsonErr != nil {
		t.Fatalf("merge-policy JSON: %v\n%s", jsonErr, stdout)
	}
	if report.Summary.Errors == 0 || !strings.Contains(report.Repositories[0].Error, "patch refused") {
		t.Fatalf("report = %+v", report.Repositories)
	}
}
