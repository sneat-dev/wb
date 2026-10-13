//go:build e2e && !windows

package orchestrate

import (
	"context"
	"flag"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestE2ELandingCleanupAdmissionRefusesBeforeMergeAndPreservesCheckout(t *testing.T) {
	const marker = "WB_LANDING_CLEANUP_FIXTURE_CHILD"
	if os.Getenv(marker) == t.Name() {
		fixtureLandingCleanupAdmission(t)
		return
	}
	t.Parallel()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
	command.Env = append(os.Environ(), marker+"="+t.Name())
	if coverFlag := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && coverFlag != nil && coverFlag.Value.String() != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+coverFlag.Value.String())
		command.Env = append(command.Env, "GOCOVERDIR="+coverFlag.Value.String())
	}
	output, err := command.CombinedOutput()
	t.Logf("isolated admission fixture:\n%s", output)
	if err != nil {
		t.Fatalf("isolated admission fixture: %v", err)
	}
}

func fixtureLandingCleanupAdmission(t *testing.T) {
	fixture := newLandFixture(t, "bump/admission", "go.mod")
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{ProjectsRoot: fixture.projects, Operation: "admission", Branch: "bump/admission", BranchChosen: true, Resume: true, WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatal(created, err)
	}
	checkout := created[0].WorktreeDir
	options := landOptions(fixture)
	options.Keep = true
	view := orchCovPullRequestView(t, `{"head":{"ref":"bump/admission","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
	evidence := filepath.Join(checkout, "go.work")
	original := []byte("{malformed\n")
	if err = os.WriteFile(evidence, original, 0600); err != nil {
		t.Fatal(err)
	}
	wrongBranch := view
	wrongBranch.Head.Ref = "unrelated"
	if got := preflightLandingCleanup(context.Background(), options, wrongBranch, "7", true); got != nil {
		t.Fatalf("unrelated branch refused: %+v", got)
	}
	wrongRepository := orchCovPullRequestView(t, `{"head":{"ref":"bump/admission","repo":{"full_name":"acme/ap"}},"base":{"ref":"main"}}`)
	if got := preflightLandingCleanup(context.Background(), options, wrongRepository, "7", true); got != nil {
		t.Fatalf("substring repository match refused: %+v", got)
	}
	if got := preflightLandingCleanup(context.Background(), options, view, "7", false); got == nil || got.code != "cleanup-blocked-dirty" {
		t.Fatalf("dirty admission=%+v", got)
	}
	for _, fault := range []string{"malformed workspace", "corrupt stream", "live workspace"} {
		if fault == "corrupt stream" {
			if err = os.Remove(evidence); err != nil {
				t.Fatal(err)
			}
			store, openErr := streams.Open(fixture.projects)
			if openErr != nil {
				t.Fatal(openErr)
			}
			evidence = filepath.Join(store.Root, "broken", "stream.json")
			if err = os.MkdirAll(filepath.Dir(evidence), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if fault == "live workspace" {
			if err = os.RemoveAll(filepath.Dir(evidence)); err != nil {
				t.Fatal(err)
			}
			evidence = filepath.Join(checkout, "go.work")
			original = []byte("go 1.27\nuse /elsewhere/unpublished\n")
			if err = os.WriteFile(evidence, original, 0600); err != nil {
				t.Fatal(err)
			}
		}
		result, landErr := LandPullRequest(context.Background(), options)
		code := "cleanup-unverifiable"
		if fault == "live workspace" {
			code = "cleanup-blocked-live-link"
		}
		if landErr != nil || result.Outcome != LandRefused || result.RefusalCode != code || !strings.Contains(result.Reason, evidence) {
			t.Fatalf("%s landed: %+v %v", fault, result, landErr)
		}
		if fixture.readState(t, "merged") != "false" || fixtureHasMarker(fixture, "auto-merge") {
			t.Fatal("irreversible landing ran before admission")
		}
		if _, err = os.Stat(checkout); err != nil {
			t.Fatal("refusal removed checkout", err)
		}
		raw, readErr := os.ReadFile(evidence)
		if readErr != nil || string(raw) != string(original) {
			t.Fatal("refusal changed evidence", readErr)
		}
	}
	// A WB-home resolution failure must not silently discard recorded links.
	projects := filepath.Join(t.TempDir(), "cycle")
	if err := os.Symlink(projects, projects); err != nil {
		t.Fatal(err)
	}
	if refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout}); refusal == nil || refusal.code != "cleanup-unverifiable" {
		t.Fatalf("unresolvable link storage accepted: %+v", refusal)
	}
	if err := os.Remove(evidence); err != nil {
		t.Fatal(err)
	}
	if refusal := refuseLinkedWorktree(projects, worktrees.ListResult{WorktreeDir: checkout}); refusal == nil || refusal.code != "cleanup-unverifiable" {
		t.Fatalf("absent workspace hid unresolvable link storage: %+v", refusal)
	}

}

func TestE2ELandingChecksOnlyTheAuthoritativeSourceWorktree(t *testing.T) {
	const marker = "WB_LANDING_SCOPED_SOURCE_CHILD"
	if os.Getenv(marker) != t.Name() {
		t.Parallel()
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
		command.Env = append(os.Environ(), marker+"="+t.Name())
		if coverFlag := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && coverFlag != nil && coverFlag.Value.String() != "" {
			command.Args = append(command.Args, "-test.gocoverdir="+coverFlag.Value.String())
			command.Env = append(command.Env, "GOCOVERDIR="+coverFlag.Value.String())
		}
		output, err := command.CombinedOutput()
		t.Logf("isolated scoped-source fixture:\n%s", output)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	fixture := newLandFixture(t, "bump/scoped-source", "go.mod")
	create := func(task, branch string, resume bool) string {
		t.Helper()
		created, err := worktrees.Create(t.Context(), []string{"acme/app"}, worktrees.CreateOptions{ProjectsRoot: fixture.projects, Operation: task, Branch: branch, BranchChosen: true, Resume: resume, WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
		if err != nil || len(created) != 1 {
			t.Fatal(created, err)
		}
		return created[0].WorktreeDir
	}
	source := create("scoped-source", "bump/scoped-source", true)
	unrelated := create("diagnostic", "diagnostic", false)
	evidence := filepath.Join(unrelated, "go.work")
	original := []byte("{malformed workspace\n")
	if err := os.WriteFile(evidence, original, 0600); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	view := orchCovPullRequestView(t, `{"head":{"ref":"bump/scoped-source","repo":{"full_name":"Acme/App"}},"base":{"ref":"main"}}`)
	fixture.writeState(t, "head-repository", "Acme/App")
	// The public inventory's substring filter also selects app-extra. A
	// malformed manifest there is still provably a different Git repository,
	// even with exactly the same branch name as the PR source.
	prefixCanonical := filepath.Join(fixture.projects, "acme", "app-extra")
	runEngineGit(t, fixture.root, "clone", "--no-hardlinks", fixture.remote, prefixCanonical)
	prefixCreated, err := worktrees.Create(t.Context(), []string{"acme/app-extra"}, worktrees.CreateOptions{ProjectsRoot: fixture.projects, Operation: "prefix-diagnostic", Branch: "bump/scoped-source", BranchChosen: true, Resume: true, WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
	if err != nil || len(prefixCreated) != 1 {
		t.Fatal(prefixCreated, err)
	}
	prefixEvidence := filepath.Join(prefixCreated[0].WorktreeDir, ".wb", "local", "manifest.yaml")
	if err := os.WriteFile(prefixEvidence, []byte("[malformed manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	branchEvidence := filepath.Join(unrelated, ".wb", "local", "manifest.yaml")
	if err := os.WriteFile(branchEvidence, []byte("[malformed manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal != nil {
			t.Fatalf("unrelated dirty malformed workspace blocked source (keep=%t): %+v", keep, refusal)
		}
	}
	gitFile := filepath.Join(source, ".git")
	gitEvidence, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("gitdir: /missing/source-evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if refusal := preflightLandingCleanup(t.Context(), options, view, "7", true); refusal == nil || refusal.code != "cleanup-unverifiable" {
		t.Fatalf("skipped source inventory diagnostic authorized landing: %+v", refusal)
	}
	if err := os.WriteFile(gitFile, gitEvidence, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := streams.Open(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(streams.Stream{Name: "source-link", Members: []streams.Member{{Repository: "acme/app", Worktree: source, Links: []streams.Link{{Library: "/elsewhere/library", Mechanism: streams.MechanismPnpmLink, Identity: "@acme/core"}}}}}); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		options.Keep = keep
		result, err := LandPullRequest(t.Context(), options)
		if err != nil || result.RefusalCode != "cleanup-blocked-live-link" || strings.Contains(result.SanctionedCommand, "--keep") || fixtureHasMarker(fixture, "auto-merge") || fixture.readState(t, "merged") != "false" {
			t.Fatalf("mixed-case recorded source link escaped admission (keep=%t): %+v %v", keep, result, err)
		}
	}
	if err := os.RemoveAll(store.Dir("source-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.work"), []byte("go 1.27\nuse /elsewhere/library\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		options.Keep = keep
		result, err := LandPullRequest(t.Context(), options)
		code := "cleanup-blocked-live-link"
		if !keep {
			code = "cleanup-blocked-dirty"
		}
		if err != nil || result.RefusalCode != code || fixtureHasMarker(fixture, "auto-merge") || fixture.readState(t, "merged") != "false" {
			t.Fatalf("mixed-case hand-written source workspace escaped admission (keep=%t): %+v %v", keep, result, err)
		}
	}
	if err := os.Remove(filepath.Join(source, "go.work")); err != nil {
		t.Fatal(err)
	}
	// A same-named branch in the target repository is unrelated to a fork's
	// source, both for link admission and for post-merge cleanup.
	forkCanonical := filepath.Join(fixture.projects, "fork", "app")
	if err := os.MkdirAll(filepath.Dir(forkCanonical), 0755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.root, "clone", "--no-hardlinks", fixture.remote, forkCanonical)
	created, err := worktrees.Create(t.Context(), []string{"fork/app"}, worktrees.CreateOptions{ProjectsRoot: fixture.projects, Operation: "fork-source", Branch: "bump/scoped-source", BranchChosen: true, Resume: true, WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatal(created, err)
	}
	forkSource := created[0].WorktreeDir
	forkView := orchCovPullRequestView(t, `{"head":{"ref":"bump/scoped-source","repo":{"full_name":"fork/app"}},"base":{"ref":"main"}}`)
	if err := os.WriteFile(filepath.Join(source, "go.work"), []byte("go 1.27\nuse /elsewhere/target-library\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if refusal := preflightLandingCleanup(t.Context(), options, forkView, "7", false); refusal != nil {
		t.Fatalf("target branch collision blocked fork source: %+v", refusal)
	}
	if err := os.WriteFile(filepath.Join(forkSource, "go.work"), []byte("go 1.27\nuse /elsewhere/fork-library\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if refusal := preflightLandingCleanup(t.Context(), options, forkView, "7", true); refusal == nil || refusal.code != "cleanup-blocked-live-link" {
		t.Fatalf("fork source live workspace escaped admission: %+v", refusal)
	}
	if err := os.Remove(filepath.Join(forkSource, "go.work")); err != nil {
		t.Fatal(err)
	}
	fixture.writeState(t, "head-repository", "fork/app")
	options.Keep = false
	result, err := LandPullRequest(t.Context(), options)
	if err != nil || result.Outcome != LandSuccess || !strings.Contains(result.Evidence["cleanup"], "fork source worktrees retained") || len(result.CleanedTasks) != 0 {
		t.Fatalf("unrelated diagnostic checkout blocked real landing: %+v %v", result, err)
	}
	for _, checkout := range []string{source, unrelated, forkSource} {
		if _, err := os.Stat(checkout); err != nil {
			t.Fatal("fork landing removed unrelated or retained source checkout", checkout, err)
		}
	}
	if raw, err := os.ReadFile(evidence); err != nil || string(raw) != string(original) {
		t.Fatalf("unrelated evidence changed: %q %v", raw, err)
	}
}

func TestE2ELandingPreservesUnrelatedRetiredSiblingStage(t *testing.T) {
	const marker = "WB_LANDING_RETIRED_ARTIFACT_CHILD"
	if os.Getenv(marker) != t.Name() {
		t.Parallel()
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.v")
		command.Env = append(os.Environ(), marker+"="+t.Name())
		if coverFlag := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && coverFlag != nil && coverFlag.Value.String() != "" {
			command.Args = append(command.Args, "-test.gocoverdir="+coverFlag.Value.String())
			command.Env = append(command.Env, "GOCOVERDIR="+coverFlag.Value.String())
		}
		output, err := command.CombinedOutput()
		t.Logf("isolated retirement fixture:\n%s", output)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	fixture := newLandFixture(t, "bump/retirement-admission", "go.mod")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeEngineFile(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: repository-local\n")
	created, err := worktrees.Create(t.Context(), []string{"acme/app"}, worktrees.CreateOptions{ProjectsRoot: fixture.projects, Operation: "retirement-source", Branch: "bump/retirement-admission", BranchChosen: true, Resume: true, WorkLog: worktrees.WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatal(created, err)
	}
	source := created[0].WorktreeDir
	stage := filepath.Join(fixture.canonical, ".worktrees", ".wb-retired-stage-6c1f44adcc9f37e8714a364751203148")
	payload := filepath.Join(stage, ".wb-retired-checkout-646a6024d926487690b2f583704c5aa5", "retained")
	writeEngineFile(t, payload, "retained legacy recovery evidence\n")
	options := landOptions(fixture)
	view := orchCovPullRequestView(t, `{"head":{"ref":"bump/retirement-admission","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
	listed, err := worktrees.ListWithDiagnostics(t.Context(), worktrees.ListOptions{ProjectsRoot: fixture.projects, Filter: "acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	foundArtifact, foundDiagnostic := false, false
	for _, artifact := range listed.Artifacts {
		foundArtifact = foundArtifact || artifact.Path == stage && artifact.InspectionVerified && artifact.State == "quarantined" && artifact.Disposition == "unscoped_local_stage"
	}
	for _, diagnostic := range listed.Diagnostics {
		foundDiagnostic = foundDiagnostic || diagnostic.Path == stage
	}
	if !foundArtifact || !foundDiagnostic {
		t.Fatalf("fixture did not reproduce typed recovery artifact plus cleanup diagnostic: %+v", listed)
	}
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal != nil {
			t.Fatalf("unregistered retired recovery state blocked source (keep=%t): %+v", keep, refusal)
		}
	}
	assertRefused := func(label string) {
		t.Helper()
		result, err := LandPullRequest(t.Context(), options)
		if err != nil || result.RefusalCode != "cleanup-unverifiable" || fixtureHasMarker(fixture, "auto-merge") || fixture.readState(t, "merged") != "false" {
			t.Fatalf("%s escaped source admission: %+v %v", label, result, err)
		}
	}
	gitFile := filepath.Join(source, ".git")
	gitEvidence, err := os.ReadFile(gitFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitFile, []byte("gitdir: /missing/producer-evidence\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertRefused("broken actual producer")
	if err := os.WriteFile(gitFile, gitEvidence, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(source, "go.work")
	writeEngineFile(t, workspace, "{malformed source workspace\n")
	assertRefused("unknown actual source link")
	if err := os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	// A retired container may retain a registered independent checkout. Its
	// branch is authoritative evidence that it did not produce this PR.
	independent := filepath.Join(stage, ".wb-retired-checkout-independent")
	runEngineGit(t, fixture.canonical, "worktree", "add", "-b", "independent-retirement", independent, "main")
	if refusal := preflightLandingCleanup(t.Context(), options, view, "7", true); refusal != nil {
		t.Fatalf("registered independent retirement blocked source: %+v", refusal)
	}
	producer := filepath.Join(stage, ".wb-retired-checkout-source")
	runEngineGit(t, fixture.canonical, "worktree", "add", "--force", producer, view.Head.Ref)
	assertRefused("registered source inside retired artifact")
	if err := os.WriteFile(filepath.Join(producer, ".git"), []byte("gitdir: /missing/nested-producer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertRefused("registered broken source inside retired artifact")
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "retained legacy recovery evidence\n" {
		t.Fatalf("retired artifact evidence changed: %q %v", raw, err)
	}
}
