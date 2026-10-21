//go:build e2e && !windows

package orchestrate

import (
	"context"
	"flag"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	view.Head.SHA = fixture.headSHA
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
	wrongRepository.Head.SHA = fixture.headSHA
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
	view.Head.SHA = fixture.headSHA
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
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal == nil || refusal.code != "cleanup-unverifiable" {
			t.Fatalf("contradictory registered source evidence authorized landing (keep=%t): %+v", keep, refusal)
		}
	}
	if _, _, err := cleanupLandedWorktrees(t.Context(), fixture.projects, "acme/app", view.Head.Ref, view.Head.SHA, "main", view.Head.SHA); err == nil || !strings.Contains(err.Error(), "source worktree cleanup inventory is incomplete at "+source) {
		t.Fatalf("contradictory source cleanup inventory was accepted: %v", err)
	}
	if got, err := os.ReadFile(gitFile); err != nil || string(got) != "gitdir: /missing/source-evidence\n" {
		t.Fatalf("refused cleanup mutated source evidence: %q %v", got, err)
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
	forkView.Head.SHA = fixture.headSHA
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
	view.Head.SHA = fixture.headSHA
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
	assertRefused := func(label string, dirtySource bool) {
		t.Helper()
		for _, keep := range []bool{false, true} {
			options.Keep = keep
			result, err := LandPullRequest(t.Context(), options)
			code := "cleanup-unverifiable"
			if dirtySource && !keep {
				code = "cleanup-blocked-dirty"
			}
			if err != nil || result.RefusalCode != code || fixtureHasMarker(fixture, "auto-merge") || fixture.readState(t, "merged") != "false" {
				t.Fatalf("%s escaped source admission (keep=%t): %+v %v", label, keep, result, err)
			}
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
	assertRefused("broken actual producer", false)
	if err := os.WriteFile(gitFile, gitEvidence, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(source, "go.work")
	writeEngineFile(t, workspace, "{malformed source workspace\n")
	assertRefused("unknown actual source link", true)
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
	symbolicParent := filepath.Join(fixture.root, "retired-stage-parent-alias")
	if err := os.Symlink(stage, symbolicParent); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"ordinary", "case", "symbolic parent"} {
		parent := stage
		if alias == "case" {
			parent = filepath.Join(filepath.Dir(stage), strings.ToUpper(filepath.Base(stage)))
			actual, actualErr := os.Stat(stage)
			alternate, alternateErr := os.Stat(parent)
			if actualErr != nil || alternateErr != nil || !os.SameFile(actual, alternate) {
				t.Log("case-alias fixture skipped on a case-sensitive filesystem")
				continue
			}
		}
		if alias == "symbolic parent" {
			parent = symbolicParent
		}
		for _, state := range []string{"source", "detached"} {
			producer := filepath.Join(parent, ".wb-retired-checkout-producer")
			arguments := []string{"worktree", "add", "--force"}
			if state == "detached" {
				arguments = append(arguments, "--detach")
			}
			arguments = append(arguments, producer, view.Head.Ref)
			runEngineGit(t, fixture.canonical, arguments...)
			gitFile := filepath.Join(producer, ".git")
			gitEvidence, err := os.ReadFile(gitFile)
			if err != nil {
				t.Fatal(err)
			}
			if alias != "ordinary" {
				// Some Git versions resolve symbolic parents during add. Retain
				// the equivalent alias in this disposable registration so Git's
				// actual porcelain output exercises the physical identity guard.
				admin := strings.TrimSpace(strings.TrimPrefix(string(gitEvidence), "gitdir: "))
				writeEngineFile(t, filepath.Join(admin, "gitdir"), gitFile+"\n")
				registry := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain", "-z")
				if !strings.Contains(registry, "worktree "+producer+"\x00") {
					t.Fatalf("native Git registry did not retain %s alias: %q", alias, registry)
				}
				parentIdentity, parentErr := os.Stat(parent)
				stageIdentity, stageErr := os.Stat(stage)
				relative, relativeErr := filepath.Rel(stage, producer)
				if parentErr != nil || stageErr != nil || !os.SameFile(parentIdentity, stageIdentity) || relativeErr != nil || !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					t.Fatalf("%s fixture does not prove physical containment despite lexical exclusion", alias)
				}
				t.Logf("exercised native %s alias for %s registration", alias, state)
			}
			assertRefused(alias+" registered "+state+" inside retired artifact", false)
			if err := os.WriteFile(gitFile, []byte("gitdir: /missing/nested-producer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			assertRefused(alias+" registered broken "+state+" inside retired artifact", false)
			if err := os.WriteFile(gitFile, gitEvidence, 0600); err != nil {
				t.Fatal(err)
			}
			// Remove only this disposable test checkout before exercising the
			// next alias; each case must independently retain admission.
			runEngineGit(t, fixture.canonical, "worktree", "remove", "--force", producer)
		}
	}
	if refusal := preflightLandingCleanup(t.Context(), options, view, "7", true); refusal != nil {
		t.Fatalf("unrelated retirement stopped passing after alias checks: %+v", refusal)
	}
	// Reproduce a foreign tool's clean detached checkout with a retained local
	// manifest but no WB projection/claim. The manifest cannot grant ownership;
	// exact Git commit, status, and physical registry evidence may exclude it.
	foreign := filepath.Join(fixture.canonical, ".claude", "worktrees", "foreign-detached")
	// This seed is a tiny Go repository, without the operator's global ignore
	// rules for foreign tool metadata. Keep canonical synchronization's clean
	// precondition while reproducing the foreign placement in this fixture.
	exclude := filepath.Join(fixture.canonical, ".git", "info", "exclude")
	exclusions, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, exclude, string(exclusions)+"\n/.claude/\n")
	runEngineGit(t, fixture.canonical, "worktree", "add", "-b", "claude/foreign-review", foreign, "main")
	if err := worktrees.WriteManifest(foreign, worktrees.Manifest{Version: 1, EffortID: "foreign-detached", EffortKind: "task", Repository: "acme/app", Worktree: foreign, Branch: "claude/foreign-review", Base: "main", BaseSHA: fixture.baseSHA, CreatedAt: time.Now().UTC(), Provenance: "reconstructed", InferredFields: []string{"base_sha"}}); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, foreign, "checkout", "--detach", fixture.baseSHA)
	view.Head.SHA = fixture.headSHA
	listed, err = worktrees.ListWithDiagnostics(t.Context(), worktrees.ListOptions{ProjectsRoot: fixture.projects, Filter: "acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	foundForeign := false
	for _, diagnostic := range listed.Diagnostics {
		foundForeign = foundForeign || diagnostic.Path == foreign && strings.Contains(diagnostic.Message, "work-log projection not found")
	}
	if !foundForeign {
		t.Fatalf("fixture did not reproduce uncorroborated foreign manifest: %+v", listed.Diagnostics)
	}
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal != nil {
			t.Fatalf("clean detached other commit blocked source (keep=%t): %+v", keep, refusal)
		}
	}
	runEngineGit(t, foreign, "checkout", "--detach", fixture.headSHA)
	assertRefused("detached checkout at exact reviewed source head", false)
	exactHeadDirty := filepath.Join(foreign, "untracked-at-source-head.go")
	writeEngineFile(t, exactHeadDirty, "untracked exact-head work\n")
	assertRefused("dirty detached checkout at exact reviewed source head", false)
	if err := os.Remove(exactHeadDirty); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, foreign, "checkout", "--detach", fixture.baseSHA)
	untracked := filepath.Join(foreign, "untracked-source.go")
	writeEngineFile(t, untracked, "dirty untracked source\n")
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal != nil {
			t.Fatalf("unrelated dirty detached checkout blocked source (keep=%t): %+v", keep, refusal)
		}
	}
	if got, err := os.ReadFile(untracked); err != nil || string(got) != "dirty untracked source\n" {
		t.Fatalf("unrelated untracked work changed: %q %v", got, err)
	}
	if err := os.Remove(untracked); err != nil {
		t.Fatal(err)
	}
	tracked := strings.Split(runEngineGit(t, foreign, "ls-files", "-z"), "\x00")[0]
	trackedPath := filepath.Join(foreign, tracked)
	originalTracked, err := os.ReadFile(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trackedPath, append(append([]byte{}, originalTracked...), []byte("\nchanged tracked fixture\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal != nil {
			t.Fatalf("unrelated tracked changes blocked source (keep=%t): %+v", keep, refusal)
		}
	}
	if got, err := os.ReadFile(trackedPath); err != nil || string(got) != string(originalTracked)+"\nchanged tracked fixture\n" {
		t.Fatalf("unrelated tracked work changed: %q %v", got, err)
	}
	if err := os.WriteFile(trackedPath, originalTracked, 0600); err != nil {
		t.Fatal(err)
	}
	if refusal := preflightLandingCleanup(t.Context(), options, view, "7", true); refusal != nil {
		t.Fatalf("restored detached other commit stopped passing: %+v", refusal)
	}
	mismatch := &landingRegistryMismatchRunner{Runner: defaultRunner, path: foreign, before: fixture.baseSHA, after: fixture.headSHA}
	options.run = mismatch
	assertRefused("detached checkout with contradictory canonical registration", false)
	if !mismatch.exercised {
		t.Fatal("contradictory registration fault was not exercised")
	}
	options.run = nil
	// All three previously broad-blocking foreign states coexist. The global
	// audit still reports the log-only orphan; source admission does not open it.
	orphan := filepath.Join(fixture.canonical, ".worktrees", "stranded-squash-landing")
	orphanLog := filepath.Join(orphan, ".wb", "local", "run", "events.jsonl")
	writeEngineFile(t, orphanLog, "{retained unrelated run event}\n")
	global, err := worktrees.ListWithDiagnostics(t.Context(), worktrees.ListOptions{ProjectsRoot: fixture.projects, Filter: "acme/app"})
	if err != nil {
		t.Fatal(err)
	}
	foundOrphan := false
	for _, diagnostic := range global.Diagnostics {
		foundOrphan = foundOrphan || diagnostic.Path == orphan
	}
	if !foundOrphan {
		t.Fatal("global inventory lost the orphan diagnostic")
	}
	for _, keep := range []bool{false, true} {
		if refusal := preflightLandingCleanup(t.Context(), options, view, "7", keep); refusal != nil {
			t.Fatalf("combined unrelated foreign states blocked source (keep=%t): %+v", keep, refusal)
		}
	}
	projection := filepath.Join(source, ".wb-worklog", "recovery.json")
	if err := os.Rename(projection, projection+".retained"); err != nil {
		t.Fatal(err)
	}
	assertRefused("matching source with missing immutable claim projection", false)
	if err := os.Rename(projection+".retained", projection); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(source, ".wb", "local", "manifest.yaml")
	manifestEvidence, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, manifestPath, "[malformed source manifest")
	assertRefused("matching source with unreadable creation metadata", false)
	if err := os.WriteFile(manifestPath, manifestEvidence, 0600); err != nil {
		t.Fatal(err)
	}
	assertLinkedSource := func(handwritten bool) {
		t.Helper()
		for _, keep := range []bool{false, true} {
			options.Keep = keep
			result, err := LandPullRequest(t.Context(), options)
			code := "cleanup-blocked-live-link"
			if handwritten && !keep {
				code = "cleanup-blocked-dirty"
			}
			if err != nil || result.RefusalCode != code || fixtureHasMarker(fixture, "auto-merge") || fixture.readState(t, "merged") != "false" {
				t.Fatalf("combined foreign fixture let linked source escape (keep=%t): %+v %v", keep, result, err)
			}
		}
	}
	store, err := streams.Open(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(streams.Stream{Name: "combined-source-link", Members: []streams.Member{{Repository: "acme/app", Worktree: source, Links: []streams.Link{{Library: "/elsewhere/library", Mechanism: streams.MechanismPnpmLink, Identity: "@acme/core"}}}}}); err != nil {
		t.Fatal(err)
	}
	assertLinkedSource(false)
	if err := os.RemoveAll(store.Dir("combined-source-link")); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(source, "go.work"), "go 1.27\nuse /elsewhere/unpublished\n")
	assertLinkedSource(true)
	if err := os.Remove(filepath.Join(source, "go.work")); err != nil {
		t.Fatal(err)
	}
	// Complete the normal retirement journey through the same source query.
	options.Keep = false
	landed, err := LandPullRequest(t.Context(), options)
	if err != nil || landed.Outcome != LandSuccess || len(landed.CleanedTasks) != 1 || landed.CleanedTasks[0] != "retirement-source" {
		t.Fatalf("source-scoped landing/cleanup failed: %+v %v", landed, err)
	}
	if raw, err := os.ReadFile(orphanLog); err != nil || string(raw) != "{retained unrelated run event}\n" {
		t.Fatalf("landing changed unrelated log-only residue: %q %v", raw, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, foreign, "rev-parse", "HEAD")); got != fixture.baseSHA {
		t.Fatalf("landing changed foreign detached checkout: %s", got)
	}
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "retained legacy recovery evidence\n" {
		t.Fatalf("retired artifact evidence changed: %q %v", raw, err)
	}
}

// Preserve real Git and all native admission calls while contradicting only
// the detached checkout's canonical registry HEAD; no Git files are edited.
type landingRegistryMismatchRunner struct {
	runner.Runner
	path, before, after string
	exercised           bool
}

func (r *landingRegistryMismatchRunner) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, options, name, args...)
	if err == nil && name == "git" && strings.Join(args, " ") == "--no-optional-locks worktree list --porcelain -z" {
		before := "worktree " + r.path + "\x00HEAD " + r.before + "\x00detached"
		after := "worktree " + r.path + "\x00HEAD " + r.after + "\x00detached"
		r.exercised = r.exercised || strings.Contains(result.CombinedOutput+result.Stdout, before)
		result.CombinedOutput = strings.ReplaceAll(result.CombinedOutput, before, after)
		result.Stdout = strings.ReplaceAll(result.Stdout, before, after)
	}
	return result, err
}

//nolint:paralleltest // newLandFixture and WB creation configure process-wide test environment.
func TestE2ELandingAdmitsFinalizedPublishedSourceThroughNormalCleanup(t *testing.T) {
	fixture := newLandFixture(t, "feature/finalized-source", "feature.txt")
	created, err := worktrees.Create(t.Context(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "finalized-source", Branch: "feature/finalized-source",
		BranchChosen: true, Resume: true, WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create published producer: %+v %v", created, err)
	}
	producer := created[0]
	if _, err := worktrees.LogFinalize(t.Context(), worktrees.LogFinalizeOptions{
		ProjectsRoot: fixture.projects, Worktree: producer.WorktreeDir,
		Result: "success", Report: []byte("Published for independent review; landing pending.\n"), Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	options := landOptions(fixture)
	options.Keep = false
	view := orchCovPullRequestView(t, `{"head":{"ref":"feature/finalized-source","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
	view.Head.SHA = fixture.headSHA
	if refusal := preflightLandingCleanup(t.Context(), options, view, "7", false); refusal != nil {
		t.Fatalf("successful sealed producer refused before landing: %+v", refusal)
	}
	wrongHead := view
	wrongHead.Head.SHA = fixture.baseSHA
	if refusal := preflightLandingCleanup(t.Context(), options, wrongHead, "7", false); refusal == nil || refusal.code != "cleanup-unverifiable" || fixture.readState(t, "merged") != "false" {
		t.Fatalf("wrong reviewed head reached merge: %+v", refusal)
	}
	review := filepath.Join(t.TempDir(), "independent-review.md")
	if err := os.WriteFile(review, []byte("Approved after independent review.\n\nReviewed-Head: "+fixture.headSHA+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options.ApprovedBy = review
	home, err := wbhome.Root(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := filepath.Glob(filepath.Join(home, "worklogs", "finalized-source", "outbox", "*-sealed.json"))
	if err != nil || len(sealed) != 1 {
		t.Fatalf("sealed outbox fixture = %v, %v", sealed, err)
	}
	if err := os.Rename(sealed[0], sealed[0]+".held"); err != nil {
		t.Fatal(err)
	}
	refused, err := LandPullRequest(t.Context(), options)
	if err != nil || refused.RefusalCode != "cleanup-unverifiable" || fixture.readState(t, "merged") != "false" {
		t.Fatalf("missing immutable outbox reached remote merge: %+v %v", refused, err)
	}
	if err := os.Rename(sealed[0]+".held", sealed[0]); err != nil {
		t.Fatal(err)
	}
	landed, err := LandPullRequest(t.Context(), options)
	if err != nil || landed.Outcome != LandSuccess || len(landed.CleanedTasks) != 1 || landed.CleanedTasks[0] != "finalized-source" {
		t.Fatalf("normal finalized-source landing/cleanup: %+v %v", landed, err)
	}
	if fixture.readState(t, "merged") != "true" {
		t.Fatalf("remote merge not confirmed: %+v", landed)
	}
	if _, err := os.Stat(producer.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("source worktree not retired after landing: %v", err)
	}
}
