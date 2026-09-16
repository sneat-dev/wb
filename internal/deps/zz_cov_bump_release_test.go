package deps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/testenv"
)

// This file raises statement coverage for the wave handler and the
// release-observation machinery in bump.go (lines ~760-1524). Every test
// asserts observable behavior — returned decisions, errors, files written,
// report fields — rather than merely executing lines.

// depsCovBumpReleaseFakeTool installs an executable shim named name at the front of
// PATH for the duration of one test, so the production code's own command
// runner executes the shim instead of a real (network-dependent) tool. It
// mutates process env, so callers must not run in parallel.
func depsCovBumpReleaseFakeTool(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// A POSIX shell shim cannot be executed on Windows; the platform has no
		// portable equivalent that the production code would resolve by name.
		t.Skip("fake " + name + " shim requires a POSIX shell")
	}
	testenv.Isolate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// depsCovBumpReleaseFakeGoOutput installs a fake `go` that always prints body on stdout
// and exits 0.
func depsCovBumpReleaseFakeGoOutput(t *testing.T, body string) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "go-output")
	writeTestFile(t, output, body)
	depsCovBumpReleaseFakeTool(t, "go", "cat '"+output+"'\nexit 0\n")
}

// depsCovBumpReleaseWriteGoMod writes one go.mod fixture and returns its absolute path.
func depsCovBumpReleaseWriteGoMod(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "go.mod")
	writeTestFile(t, path, body)
	return path
}

// TestDepsCovBumpReleaseWaveHandlerInspectClassifiesAssessment pins every
// assessment outcome waveHandler.Inspect can produce, plus the fatal path
// where the adapter itself cannot read the repository at the given base.
func TestDepsCovBumpReleaseWaveHandlerInspectClassifiesAssessment(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	consumer := newBumpRepository(t, root, githubDir, "consumer", "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n")
	current := newBumpRepository(t, root, githubDir, "current", "module example.com/current\n\ngo 1.24\n\nrequire example.com/provider v0.2.0\n")
	absent := newBumpRepository(t, root, githubDir, "absent", "module example.com/absent\n\ngo 1.24\n")

	inspect := func(repository Repository, base string) (orchestrate.Assessment[[]Decision], error) {
		handler := waveHandler{
			ecosystem:           EcosystemGo,
			targetsByRepository: map[string][]Target{repository.Slug: {{Ecosystem: EcosystemGo, Dependency: "example.com/provider", Version: "v0.2.0"}}},
			options:             Options{Timeout: time.Minute},
		}
		return handler.Inspect(context.Background(), repository.Path, base, orchestrate.Repository{Slug: repository.Slug, Path: repository.Path})
	}

	assessment, err := inspect(consumer, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Applicable || !assessment.NeedsChange || assessment.Reason != "published provider events require a dependency wave update" {
		t.Fatalf("planned assessment = %+v", assessment)
	}
	if len(assessment.Metadata) != 1 || assessment.Metadata[0].Action != "planned" || assessment.Metadata[0].BeforeVersion != "v0.1.0" {
		t.Fatalf("planned decisions = %+v", assessment.Metadata)
	}

	assessment, err = inspect(current, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Applicable || assessment.NeedsChange || assessment.Reason != "all release events are already selected" {
		t.Fatalf("already-selected assessment = %+v", assessment)
	}
	if len(assessment.Metadata) != 1 || assessment.Metadata[0].Action != "unchanged" {
		t.Fatalf("already-selected decisions = %+v", assessment.Metadata)
	}

	assessment, err = inspect(absent, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Applicable || assessment.NeedsChange || assessment.Reason != "release events are absent from repository manifests" {
		t.Fatalf("absent assessment = %+v", assessment)
	}
	if len(assessment.Metadata) != 0 {
		t.Fatalf("absent decisions = %+v", assessment.Metadata)
	}

	if _, err := inspect(consumer, "origin/does-not-exist"); err == nil {
		t.Fatal("an unavailable base ref must fail inspection instead of reporting an assessment")
	}
}

// TestDepsCovBumpReleaseWaveHandlerApplyGoAndNpm pins Apply's three important
// outcomes: a Go wave that validates its final module selection, a Go wave
// whose official tooling fails, and an npm wave that edits manifests and then
// reaches the Nx version-plan hook.
func TestDepsCovBumpReleaseWaveHandlerApplyGoAndNpm(t *testing.T) {
	t.Run("go applies and validates the exact final selection", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n")
		depsCovBumpReleaseFakeTool(t, "go", "case \"$*\" in\n  *\"list -m -f\"*) printf 'v0.2.0\\n'; exit 0 ;;\nesac\nexit 0\n")
		handler := waveHandler{
			ecosystem:           EcosystemGo,
			targetsByRepository: map[string][]Target{"acme/consumer": {{Ecosystem: EcosystemGo, Dependency: "example.com/provider", Version: "v0.2.0"}}},
			options:             Options{Timeout: time.Minute},
		}
		decisions, err := handler.Apply(context.Background(), worktree, orchestrate.Repository{Slug: "acme/consumer"})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "updated" || decisions[0].AfterVersion != "v0.2.0" || decisions[0].Dependency != "example.com/provider" {
			t.Fatalf("decisions = %+v", decisions)
		}
	})

	t.Run("go reports a failed target when official tooling fails", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n")
		depsCovBumpReleaseFakeTool(t, "go", "echo 'go: module lookup disabled by GOPROXY=off' >&2\nexit 1\n")
		handler := waveHandler{
			ecosystem:           EcosystemGo,
			targetsByRepository: map[string][]Target{"acme/consumer": {{Ecosystem: EcosystemGo, Dependency: "example.com/provider", Version: "v0.2.0"}}},
			options:             Options{Timeout: time.Minute},
		}
		decisions, err := handler.Apply(context.Background(), worktree, orchestrate.Repository{Slug: "acme/consumer"})
		if err == nil || !strings.Contains(err.Error(), "module lookup disabled") {
			t.Fatalf("error = %v", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "failed" || !strings.Contains(decisions[0].Reason, "module lookup disabled") {
			t.Fatalf("decisions = %+v", decisions)
		}
	})

	t.Run("go fails when a later target invalidates the final selection", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n")
		// The first selection (during apply) matches the target; the second
		// (during final wave validation) does not, exactly the minimal-version-
		// selection drift validateGoWaveSelections exists to catch.
		countFile := filepath.Join(t.TempDir(), "list-count")
		depsCovBumpReleaseFakeTool(t, "go",
			"count=0\n"+
				"if [ -f '"+countFile+"' ]; then count=$(cat '"+countFile+"'); fi\n"+
				"case \"$*\" in\n"+
				"  *\"list -m -f\"*)\n"+
				"    count=$((count+1))\n"+
				"    printf '%s' \"$count\" > '"+countFile+"'\n"+
				"    if [ \"$count\" = \"1\" ]; then printf 'v0.2.0\\n'; else printf 'v0.3.0\\n'; fi\n"+
				"    exit 0 ;;\n"+
				"esac\n"+
				"exit 0\n")
		handler := waveHandler{
			ecosystem:           EcosystemGo,
			targetsByRepository: map[string][]Target{"acme/consumer": {{Ecosystem: EcosystemGo, Dependency: "example.com/provider", Version: "v0.2.0"}}},
			options:             Options{Timeout: time.Minute},
		}
		decisions, err := handler.Apply(context.Background(), worktree, orchestrate.Repository{Slug: "acme/consumer"})
		if err == nil || !strings.Contains(err.Error(), "selected v0.3.0; want v0.2.0") {
			t.Fatalf("error = %v", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "failed" || decisions[0].AfterVersion != "v0.3.0" {
			t.Fatalf("decisions = %+v", decisions)
		}
	})

	t.Run("npm reports a version-plan failure", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@acme/app", "@acme/provider", "0.1.0"))
		writeTestFile(t, filepath.Join(worktree, "nx.json"), "this is not json\n")
		handler := waveHandler{
			ecosystem:           EcosystemNPM,
			targetsByRepository: map[string][]Target{"acme/app": {{Ecosystem: EcosystemNPM, Dependency: "@acme/provider", Version: "0.2.0"}}},
			versionPlanID:       "deps-bump-npm-covwave",
		}
		decisions, err := handler.Apply(context.Background(), worktree, orchestrate.Repository{Slug: "acme/app"})
		if err == nil || !strings.Contains(err.Error(), "parse nx.json") {
			t.Fatalf("error = %v", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "updated" {
			t.Fatalf("decisions = %+v", decisions)
		}
	})

	t.Run("npm applies literal manifest edits and runs the version-plan hook", func(t *testing.T) {
		worktree := t.TempDir()
		writeTestFile(t, filepath.Join(worktree, "package.json"), npmPackageJSONWithDependency("@acme/app", "@acme/provider", "0.1.0"))
		handler := waveHandler{
			ecosystem:           EcosystemNPM,
			targetsByRepository: map[string][]Target{"acme/app": {{Ecosystem: EcosystemNPM, Dependency: "@acme/provider", Version: "0.2.0"}}},
			versionPlanID:       "deps-bump-npm-covwave",
		}
		decisions, err := handler.Apply(context.Background(), worktree, orchestrate.Repository{Slug: "acme/app"})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if len(decisions) != 1 || decisions[0].Action != "updated" || decisions[0].AfterVersion != "0.2.0" {
			t.Fatalf("decisions = %+v", decisions)
		}
		manifest, err := os.ReadFile(filepath.Join(worktree, "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(manifest), `"@acme/provider": "0.2.0"`) {
			t.Fatalf("package.json was not rewritten:\n%s", manifest)
		}
		if _, err := os.Stat(filepath.Join(worktree, ".nx", "version-plans")); !os.IsNotExist(err) {
			t.Fatalf("a worktree without nx.json must not gain a version plan: %v", err)
		}
	})
}

// TestDepsCovBumpReleaseWaveHandlerValidatePublishablePerEcosystem pins the
// ecosystem gate: Go (and the zero ecosystem that historically means Go)
// rejects local module replacements, while npm has no such rule to enforce.
func TestDepsCovBumpReleaseWaveHandlerValidatePublishablePerEcosystem(t *testing.T) {
	clean := t.TempDir()
	writeTestFile(t, filepath.Join(clean, "go.mod"), "module example.com/clean\n\ngo 1.24\n")
	if err := (waveHandler{ecosystem: EcosystemGo}).ValidatePublishable(context.Background(), clean, orchestrate.Repository{}); err != nil {
		t.Fatalf("clean Go manifests must validate: %v", err)
	}

	local := t.TempDir()
	writeTestFile(t, filepath.Join(local, "go.mod"), "module example.com/local\n\ngo 1.24\n\nreplace example.com/dep => ../dep\n")
	err := (waveHandler{ecosystem: EcosystemGo}).ValidatePublishable(context.Background(), local, orchestrate.Repository{})
	if err == nil || !strings.Contains(err.Error(), "local Go module replacements cannot be committed or published") {
		t.Fatalf("local replacement error = %v", err)
	}

	if err := (waveHandler{ecosystem: EcosystemNPM}).ValidatePublishable(context.Background(), local, orchestrate.Repository{}); err != nil {
		t.Fatalf("npm repositories must bypass the Go publishability guard: %v", err)
	}
	if err := (waveHandler{}).ValidatePublishable(context.Background(), local, orchestrate.Repository{}); err == nil {
		t.Fatal("the zero ecosystem must keep the historical Go publishability guard")
	}
}

// TestDepsCovBumpReleaseValidateGoWaveSelectionsSkipsAndSelectionOutcomes pins
// the final wave validation: decisions it must not re-check, a matching final
// selection that passes, and a lookup failure that fails the decision.
func TestDepsCovBumpReleaseValidateGoWaveSelectionsSkipsAndSelectionOutcomes(t *testing.T) {
	skipped := []Decision{
		{File: "go.mod", Action: "planned"},
		{Dependency: "example.com/failed", File: "go.mod", Action: "failed"},
		{Dependency: "example.com/blocked", File: "go.mod", Action: "blocked_downgrade"},
	}
	if err := validateGoWaveSelections(context.Background(), t.TempDir(), skipped, Options{}); err != nil {
		t.Fatalf("decisions without a live selection must be skipped: %v", err)
	}
	if skipped[1].Action != "failed" || skipped[2].Action != "blocked_downgrade" {
		t.Fatalf("skipped decisions were mutated: %+v", skipped)
	}

	worktree := t.TempDir()
	writeTestFile(t, filepath.Join(worktree, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/provider v0.1.0\n")

	depsCovBumpReleaseFakeTool(t, "go", "case \"$*\" in\n  *\"list -m -f\"*) printf 'v0.2.0\\n'; exit 0 ;;\nesac\nexit 0\n")
	matched := []Decision{{Dependency: "example.com/provider", File: "go.mod", TargetVersion: "v0.2.0", Action: "updated"}}
	if err := validateGoWaveSelections(context.Background(), worktree, matched, Options{Timeout: time.Minute}); err != nil {
		t.Fatalf("matching final selection must pass: %v", err)
	}
	if matched[0].AfterVersion != "v0.2.0" || matched[0].AfterRef != "v0.2.0" || matched[0].Action != "updated" {
		t.Fatalf("matched decision = %+v", matched[0])
	}

	depsCovBumpReleaseFakeTool(t, "go", "case \"$*\" in\n  *\"list -m -f\"*) printf 'v0.3.0\\n'; exit 0 ;;\nesac\nexit 0\n")
	mismatched := []Decision{{Dependency: "example.com/provider", File: "go.mod", TargetVersion: "v0.2.0", Action: "updated"}}
	err := validateGoWaveSelections(context.Background(), worktree, mismatched, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "selected v0.3.0; want v0.2.0") {
		t.Fatalf("error = %v", err)
	}
	if mismatched[0].Action != "failed" || mismatched[0].AfterVersion != "v0.3.0" ||
		mismatched[0].Reason != "final Go module selection produced v0.3.0 instead of exact wave target v0.2.0" {
		t.Fatalf("mismatched decision = %+v", mismatched[0])
	}

	depsCovBumpReleaseFakeTool(t, "go", "echo 'go: module lookup disabled by GOPROXY=off' >&2\nexit 1\n")
	failed := []Decision{{Dependency: "example.com/provider", File: "go.mod", TargetVersion: "v0.2.0", Action: "updated"}}
	err = validateGoWaveSelections(context.Background(), worktree, failed, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "final selection for example.com/provider") {
		t.Fatalf("error = %v", err)
	}
	if failed[0].Action != "failed" || !strings.Contains(failed[0].Reason, "final wave validation failed") {
		t.Fatalf("failed decision = %+v", failed[0])
	}
}

// TestDepsCovBumpReleaseCommitMessageAndPullRequestPerMode pins the commit
// title for one versus several targets and all four pull-request bodies.
func TestDepsCovBumpReleaseCommitMessageAndPullRequestPerMode(t *testing.T) {
	single := waveHandler{
		ecosystem:           EcosystemNPM,
		targetsByRepository: map[string][]Target{"acme/app": {{Dependency: "@acme/provider", Version: "2.0.0"}}},
	}
	repository := orchestrate.Repository{Slug: "acme/app"}
	if got := single.CommitMessage(repository); got != "chore(deps): bump @acme/provider to 2.0.0" {
		t.Fatalf("single-target commit message = %q", got)
	}

	multiple := single
	multiple.targetsByRepository = map[string][]Target{"acme/app": {
		{Dependency: "@acme/provider", Version: "2.0.0"},
		{Dependency: "@acme/other", Version: "1.0.0"},
	}}
	if got := multiple.CommitMessage(repository); got != "chore(deps): apply dependency release wave" {
		t.Fatalf("multi-target commit message = %q", got)
	}
	if got := single.CommitMessage(orchestrate.Repository{Slug: "acme/unknown"}); got != "chore(deps): apply dependency release wave" {
		t.Fatalf("unknown-repository commit message = %q", got)
	}

	for _, testCase := range []struct {
		name    string
		options Options
		want    string
	}{
		{name: "fast with merge", options: Options{ValidationMode: ValidationModeFast, Merge: true}, want: "before merge"},
		{name: "fast without merge", options: Options{ValidationMode: ValidationModeFast}, want: "explicit follow-up"},
		{name: "no local verification", options: Options{ValidationMode: ValidationModeNone}, want: "legacy no-verify policy"},
		{name: "full local verification", options: Options{ValidationMode: ValidationModeFull}, want: "full local verification completed"},
	} {
		handler := single
		handler.options = testCase.options
		title, body := handler.PullRequest(repository)
		if title != "chore(deps): bump @acme/provider to 2.0.0" || !strings.Contains(body, testCase.want) {
			t.Fatalf("%s: title=%q body=%q", testCase.name, title, body)
		}
	}
}

// TestDepsCovBumpReleaseCaptureReleaseBaselinesErrorAndSkips pins baseline
// capture's two non-happy paths: a module with no external consumer is not
// observed at all, and a registry failure is recorded per module and joined
// into the returned error.
func TestDepsCovBumpReleaseCaptureReleaseBaselinesErrorAndSkips(t *testing.T) {
	graph := goFleetGraph{requirements: map[string][]goFleetRequirement{
		"example.com/observed": {{Repository: "acme/consumer"}},
		"example.com/internal": {{Repository: "acme/provider"}},
	}}
	affected := map[string]map[string]bool{
		"acme/provider": {"example.com/observed": true, "example.com/internal": true},
	}
	observations, err := captureReleaseBaselines(context.Background(), graph, affected, BumpOptions{
		Options: Options{Parallel: 1, ParallelExplicit: true},
		LatestGoVersion: func(_ context.Context, module string) (string, error) {
			return "", errors.New("registry unavailable for " + module)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "observe baseline release for example.com/observed") {
		t.Fatalf("error = %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %+v, want the internally consumed module skipped", observations)
	}
	observation := observations["example.com/observed"]
	if observation.Status != "failed" || !strings.Contains(observation.Reason, "registry unavailable") || observation.Repository != "acme/provider" || observation.Module != "example.com/observed" {
		t.Fatalf("observation = %+v", observation)
	}
	if _, present := observations["example.com/internal"]; present {
		t.Fatalf("a module with no external consumers must not be observed: %+v", observations)
	}
}

// TestDepsCovBumpReleaseCaptureReleaseBaselinesSuccessAndOrdering pins the
// success path — including the deterministic module/repository ordering that
// makes the read-only pool's fan-out reproducible.
func TestDepsCovBumpReleaseCaptureReleaseBaselinesSuccessAndOrdering(t *testing.T) {
	graph := goFleetGraph{requirements: map[string][]goFleetRequirement{
		"example.com/a":      {{Repository: "acme/consumer"}},
		"example.com/b":      {{Repository: "acme/consumer"}},
		"example.com/shared": {{Repository: "acme/consumer"}},
	}}
	affected := map[string]map[string]bool{
		"acme/provider": {"example.com/a": true, "example.com/b": true, "example.com/shared": true},
		"acme/other":    {"example.com/shared": true},
	}
	observations, err := captureReleaseBaselines(context.Background(), graph, affected, BumpOptions{
		Options: Options{Parallel: 1, ParallelExplicit: true},
		LatestGoVersion: func(context.Context, string) (string, error) {
			return "v0.9.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 3 {
		t.Fatalf("observations = %+v", observations)
	}
	for module, observation := range observations {
		if observation.Module != module || observation.Before != "v0.9.0" || observation.Status != "baseline" || !observation.RequireNewer ||
			observation.Reason != "latest published version captured before wave merge" ||
			observation.Source != latestVersionCommandDescription(EcosystemGo, module) {
			t.Fatalf("observation for %s = %+v", module, observation)
		}
	}
}

// TestDepsCovBumpReleaseMergedReleaseBaselinesKeepsOnlyMergedAffectedModules
// pins that only baselines from a repository that actually merged, and whose
// module was affected, survive — sorted by module.
func TestDepsCovBumpReleaseMergedReleaseBaselinesKeepsOnlyMergedAffectedModules(t *testing.T) {
	results := []orchestrate.Result[[]Decision]{
		{Repository: "acme/merged", Merged: true},
		{Repository: "acme/open", Merged: false},
	}
	affected := map[string]map[string]bool{
		"acme/merged": {"example.com/b": true, "example.com/c": true},
		"acme/open":   {"example.com/a": true},
	}
	baselines := map[string]ReleaseObservation{
		"example.com/b": {Module: "example.com/b", Repository: "acme/merged"},
		"example.com/c": {Module: "example.com/c", Repository: "acme/merged"},
		"example.com/a": {Module: "example.com/a", Repository: "acme/open"},
		"example.com/d": {Module: "example.com/d", Repository: "acme/merged"},
	}
	merged := mergedReleaseBaselines(results, affected, baselines)
	if len(merged) != 2 || merged[0].Module != "example.com/b" || merged[1].Module != "example.com/c" {
		t.Fatalf("merged baselines = %+v", merged)
	}
}

// depsCovBumpReleaseCarrierGraph builds the minimal fleet evidence discoverExistingReleaseCarriers
// needs: one provider requirement whose consumer has an external consumer of
// its own, plus one matching requirement whose consumer has none (and must
// therefore be skipped).
func depsCovBumpReleaseCarrierGraph(repository string) goFleetGraph {
	return goFleetGraph{requirements: map[string][]goFleetRequirement{
		"example.com/provider": {
			{Dependency: "example.com/provider", Version: "v0.2.0", ConsumerModule: "example.com/adapter", Repository: repository},
			{Dependency: "example.com/provider", Version: "v0.2.0", ConsumerModule: "example.com/self", Repository: repository},
		},
		"example.com/adapter": {{Repository: "acme/consumer"}},
	}}
}

// TestDepsCovBumpReleaseDiscoverExistingReleaseCarriers pins the four
// published-carrier outcomes: a no-registry plan refuses outright, a published
// release that selects every event is released, one that does not is awaiting
// release, and a registry failure is reported with the command that would have
// answered it.
func TestDepsCovBumpReleaseDiscoverExistingReleaseCarriers(t *testing.T) {
	events := []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0"}}
	graph := depsCovBumpReleaseCarrierGraph("acme/adapter")

	carriers, err := discoverExistingReleaseCarriers(context.Background(), graph, events, BumpOptions{
		NoRegistry: true,
		LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
			t.Fatal("a no-registry plan must not consult the registry")
			return PublishedGoRelease{}, nil
		},
	})
	if err != nil || carriers != nil {
		t.Fatalf("no-registry carriers = %+v, err = %v", carriers, err)
	}

	carriers, err = discoverExistingReleaseCarriers(context.Background(), graph, events, BumpOptions{
		Options: Options{Parallel: 1, ParallelExplicit: true, Ref: "main"},
		LatestGoRelease: func(_ context.Context, module string) (PublishedGoRelease, error) {
			if module != "example.com/adapter" {
				return PublishedGoRelease{}, fmt.Errorf("unexpected module %s", module)
			}
			return PublishedGoRelease{Version: "v0.2.1", Requirements: map[string]string{"example.com/provider": "v0.2.0"}, Source: "test registry"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(carriers) != 1 {
		t.Fatalf("carriers = %+v", carriers)
	}
	released := carriers[0]
	if released.Module != "example.com/adapter" || released.Repository != "acme/adapter" || released.Status != "released" ||
		released.Before != "v0.2.1" || released.After != "v0.2.1" || released.Source != "test registry" ||
		released.ExpectedRequirements["example.com/provider"] != "v0.2.0" {
		t.Fatalf("released carrier = %+v", released)
	}

	carriers, err = discoverExistingReleaseCarriers(context.Background(), graph, events, BumpOptions{
		Options: Options{Parallel: 1, ParallelExplicit: true, Ref: "trunk"},
		LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
			return PublishedGoRelease{Version: "v0.2.1", Requirements: map[string]string{"example.com/provider": "v0.1.0"}, Source: "test registry"}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain the dependency versions selected on origin/trunk") {
		t.Fatalf("stale carrier error = %v", err)
	}
	if len(carriers) != 1 || carriers[0].Status != "awaiting_release" || carriers[0].Before != "v0.2.1" || carriers[0].After != "" {
		t.Fatalf("stale carrier = %+v", carriers)
	}

	carriers, err = discoverExistingReleaseCarriers(context.Background(), graph, events, BumpOptions{
		Options: Options{Parallel: 1, ParallelExplicit: true, Ref: "main"},
		LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
			return PublishedGoRelease{}, errors.New("registry unavailable")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "inspect published release for example.com/adapter") {
		t.Fatalf("failed carrier error = %v", err)
	}
	if len(carriers) != 1 || carriers[0].Status != "failed" || !strings.Contains(carriers[0].Reason, "registry unavailable") {
		t.Fatalf("failed carrier = %+v", carriers)
	}
	if carriers[0].Source != latestPublishedReleaseCommandDescription(EcosystemGo, "example.com/adapter") {
		t.Fatalf("failed carrier source = %q, want the command that would have answered the lookup", carriers[0].Source)
	}
}

// TestDepsCovBumpReleaseLatestPublishedGoRelease pins both the injected
// resolver seam and every failure mode of the real `go mod download -json`
// path, driven by a fake `go` on PATH so no network is touched.
func TestDepsCovBumpReleaseLatestPublishedGoRelease(t *testing.T) {
	t.Run("injected resolver", func(t *testing.T) {
		release, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "v1.2.3", Requirements: map[string]string{"example.com/dep": "v1.0.0"}}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if release.Version != "v1.2.3" || release.Source != "injected release resolver for example.com/provider" || release.Requirements["example.com/dep"] != "v1.0.0" {
			t.Fatalf("release = %+v", release)
		}

		release, err = latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "v1.2.3", Source: "custom resolver"}, nil
			},
		})
		if err != nil || release.Source != "custom resolver" {
			t.Fatalf("release = %+v, err = %v", release, err)
		}

		if _, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{}, errors.New("resolver down")
			},
		}); err == nil || !strings.Contains(err.Error(), "resolver down") {
			t.Fatalf("resolver error = %v", err)
		}

		if _, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "banana"}, nil
			},
		}); err == nil || !strings.Contains(err.Error(), `latest Go version for example.com/provider is invalid: "banana"`) {
			t.Fatalf("invalid-version error = %v", err)
		}
	})

	t.Run("go mod download succeeds", func(t *testing.T) {
		gomod := depsCovBumpReleaseWriteGoMod(t, "module example.com/provider\n\ngo 1.24\n\nrequire example.com/dep v1.4.0\n")
		depsCovBumpReleaseFakeGoOutput(t, fmt.Sprintf(`{"Version":"v1.2.3","GoMod":%q}`, gomod))
		release, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err != nil {
			t.Fatal(err)
		}
		if release.Version != "v1.2.3" || release.Requirements["example.com/dep"] != "v1.4.0" ||
			release.Source != "go mod download example.com/provider@v1.2.3" {
			t.Fatalf("release = %+v", release)
		}
	})

	t.Run("download error field", func(t *testing.T) {
		depsCovBumpReleaseFakeGoOutput(t, `{"Version":"","Error":"module example.com/provider: not found"}`)
		_, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "download published Go release for example.com/provider: module example.com/provider: not found") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid published version", func(t *testing.T) {
		gomod := depsCovBumpReleaseWriteGoMod(t, "module example.com/provider\n\ngo 1.24\n")
		depsCovBumpReleaseFakeGoOutput(t, fmt.Sprintf(`{"Version":"banana","GoMod":%q}`, gomod))
		_, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), `latest Go version for example.com/provider is invalid: "banana"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unreadable go.mod", func(t *testing.T) {
		depsCovBumpReleaseFakeGoOutput(t, `{"Version":"v1.2.3","GoMod":"/nonexistent-deps-cov/go.mod"}`)
		_, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "read published go.mod for example.com/provider@v1.2.3") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("malformed go.mod", func(t *testing.T) {
		gomod := depsCovBumpReleaseWriteGoMod(t, "this is not a go.mod file\n")
		depsCovBumpReleaseFakeGoOutput(t, fmt.Sprintf(`{"Version":"v1.2.3","GoMod":%q}`, gomod))
		_, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "parse published go.mod for example.com/provider@v1.2.3") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("undecodable output", func(t *testing.T) {
		depsCovBumpReleaseFakeGoOutput(t, "not json")
		_, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "decode published Go release for example.com/provider") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("go command failure", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "go", "echo 'go: network unreachable' >&2\nexit 1\n")
		_, err := latestPublishedGoRelease(context.Background(), "example.com/provider", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "network unreachable") {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestDepsCovBumpReleaseWaitForPublishedGoRequirements pins the wait loop that
// makes a campaign traverse an already-current consumer: immediate release,
// release after a newer version appears, timeout, registry failure, and
// context cancellation.
func TestDepsCovBumpReleaseWaitForPublishedGoRequirements(t *testing.T) {
	expected := map[string]string{"example.com/provider": "v0.2.0"}

	t.Run("already published", func(t *testing.T) {
		observation, err := waitForPublishedGoRequirements(context.Background(), ReleaseObservation{
			Module: "example.com/adapter", Repository: "acme/adapter", Before: "v0.5.0", ExpectedRequirements: expected,
		}, BumpOptions{
			Options:      Options{Timeout: time.Second},
			PollInterval: time.Millisecond,
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "v0.5.0", Requirements: map[string]string{"example.com/provider": "v0.2.0"}, Source: "test registry"}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if observation.Status != "released" || observation.After != "v0.5.0" || observation.Source != "test registry" ||
			observation.Reason != "published consumer release selects every current provider event" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("waits for a newer release", func(t *testing.T) {
		calls := 0
		observation, err := waitForPublishedGoRequirements(context.Background(), ReleaseObservation{
			Module: "example.com/adapter", Before: "v0.5.0", RequireNewer: true, ExpectedRequirements: expected,
		}, BumpOptions{
			Options:      Options{Timeout: 5 * time.Second},
			PollInterval: time.Millisecond,
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				calls++
				if calls == 1 {
					return PublishedGoRelease{Version: "v0.5.0", Requirements: map[string]string{"example.com/provider": "v0.2.0"}, Source: "test registry"}, nil
				}
				return PublishedGoRelease{Version: "v0.6.0", Requirements: map[string]string{"example.com/provider": "v0.2.0"}, Source: "test registry"}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || observation.Status != "released" || observation.After != "v0.6.0" {
			t.Fatalf("observation = %+v, calls = %d", observation, calls)
		}
	})

	t.Run("a missing baseline never blocks on version ordering", func(t *testing.T) {
		observation, err := waitForPublishedGoRequirements(context.Background(), ReleaseObservation{
			Module: "example.com/adapter", RequireNewer: true, ExpectedRequirements: expected,
		}, BumpOptions{
			PollInterval: time.Millisecond,
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "v0.5.0", Requirements: map[string]string{"example.com/provider": "v0.2.0"}, Source: "test registry"}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if observation.Status != "released" || observation.After != "v0.5.0" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("times out", func(t *testing.T) {
		observation, err := waitForPublishedGoRequirements(context.Background(), ReleaseObservation{
			Module: "example.com/adapter", Before: "v0.5.0", ExpectedRequirements: expected,
		}, BumpOptions{
			Options:      Options{Timeout: 10 * time.Millisecond},
			PollInterval: time.Millisecond,
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "v0.5.0", Requirements: map[string]string{"example.com/provider": "v0.1.0"}, Source: "test registry"}, nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "release for example.com/adapter did not publish expected dependency versions before timeout") {
			t.Fatalf("error = %v", err)
		}
		if observation.Status != "awaiting_release" || observation.Reason != "waiting for a published consumer release that selects every current provider event" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("registry failure", func(t *testing.T) {
		observation, err := waitForPublishedGoRequirements(context.Background(), ReleaseObservation{
			Module: "example.com/adapter", ExpectedRequirements: expected,
		}, BumpOptions{
			Options:      Options{Timeout: time.Second},
			PollInterval: time.Millisecond,
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{}, errors.New("registry unavailable")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
			t.Fatalf("error = %v", err)
		}
		if observation.Status != "failed" || observation.Reason != "registry unavailable" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		observation, err := waitForPublishedGoRequirements(ctx, ReleaseObservation{
			Module: "example.com/adapter", ExpectedRequirements: expected,
		}, BumpOptions{
			PollInterval: time.Millisecond,
			LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
				cancel()
				return PublishedGoRelease{Version: "v0.5.0", Requirements: map[string]string{"example.com/provider": "v0.1.0"}, Source: "test registry"}, nil
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if observation.Status != "awaiting_release" {
			t.Fatalf("observation = %+v", observation)
		}
	})
}

// TestDepsCovBumpReleaseRequirementsContainAndSortedObservations pins the
// exact-match requirement check and deterministic module ordering.
func TestDepsCovBumpReleaseRequirementsContainAndSortedObservations(t *testing.T) {
	if !requirementsContain(nil, nil) {
		t.Fatal("two empty requirement sets must match")
	}
	if !requirementsContain(map[string]string{"a": "1.0.0", "b": "2.0.0"}, map[string]string{"a": "1.0.0"}) {
		t.Fatal("a superset of the expected selections must match")
	}
	if requirementsContain(map[string]string{"a": "1.0.0"}, map[string]string{"a": "2.0.0"}) {
		t.Fatal("a different selected version must not match")
	}
	if requirementsContain(nil, map[string]string{"a": "1.0.0"}) {
		t.Fatal("a missing selection must not match an expected one")
	}

	sorted := sortedReleaseObservations(map[string]ReleaseObservation{
		"example.com/z": {Module: "example.com/z"},
		"example.com/a": {Module: "example.com/a"},
		"example.com/m": {Module: "example.com/m"},
	})
	if len(sorted) != 3 || sorted[0].Module != "example.com/a" || sorted[1].Module != "example.com/m" || sorted[2].Module != "example.com/z" {
		t.Fatalf("sorted = %+v", sorted)
	}
	if empty := sortedReleaseObservations(nil); len(empty) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

// TestDepsCovBumpReleaseRegistryDispatchers pins the ecosystem dispatch every
// shared wave helper relies on, the no-registry refusal, and the command
// description each ecosystem reports as release evidence.
func TestDepsCovBumpReleaseRegistryDispatchers(t *testing.T) {
	if _, err := latestReleaseVersion(context.Background(), "example.com/x", BumpOptions{NoRegistry: true}); err == nil || !strings.Contains(err.Error(), "registry lookup is disabled") {
		t.Fatalf("no-registry version error = %v", err)
	}
	if _, err := latestPublishedRelease(context.Background(), "example.com/x", BumpOptions{NoRegistry: true}); err == nil || !strings.Contains(err.Error(), "registry lookup is disabled") {
		t.Fatalf("no-registry release error = %v", err)
	}

	version, err := latestReleaseVersion(context.Background(), "@acme/core", BumpOptions{
		Ecosystem: EcosystemNPM,
		LatestNpmVersion: func(_ context.Context, module string) (string, error) {
			if module != "@acme/core" {
				return "", fmt.Errorf("unexpected module %s", module)
			}
			return "1.2.3", nil
		},
	})
	if err != nil || version != "1.2.3" {
		t.Fatalf("npm version = %q, err = %v", version, err)
	}

	version, err = latestReleaseVersion(context.Background(), "example.com/core", BumpOptions{
		LatestGoVersion: func(context.Context, string) (string, error) { return "v1.2.3", nil },
	})
	if err != nil || version != "v1.2.3" {
		t.Fatalf("go version = %q, err = %v", version, err)
	}

	release, err := latestPublishedRelease(context.Background(), "@acme/core", BumpOptions{
		Ecosystem: EcosystemNPM,
		LatestNpmRelease: func(context.Context, string) (PublishedGoRelease, error) {
			return PublishedGoRelease{Version: "1.2.3", Source: "npm resolver"}, nil
		},
	})
	if err != nil || release.Version != "1.2.3" || release.Source != "npm resolver" {
		t.Fatalf("npm release = %+v, err = %v", release, err)
	}

	release, err = latestPublishedRelease(context.Background(), "example.com/core", BumpOptions{
		LatestGoRelease: func(context.Context, string) (PublishedGoRelease, error) {
			return PublishedGoRelease{Version: "v1.2.3", Source: "go resolver"}, nil
		},
	})
	if err != nil || release.Version != "v1.2.3" || release.Source != "go resolver" {
		t.Fatalf("go release = %+v, err = %v", release, err)
	}

	if got, want := latestPublishedReleaseCommandDescription(EcosystemNPM, "@acme/core"), "pnpm view @acme/core@latest "+strings.Join(npmDependencyFieldNames, " "); got != want {
		t.Fatalf("npm release command = %q, want %q", got, want)
	}
	for _, ecosystem := range []Ecosystem{EcosystemGo, ""} {
		if got := latestPublishedReleaseCommandDescription(ecosystem, "example.com/core"); got != "go mod download example.com/core@latest" {
			t.Fatalf("%q release command = %q", ecosystem, got)
		}
	}
	if got := latestVersionCommandDescription(EcosystemNPM, "@acme/core"); got != "pnpm view @acme/core version" {
		t.Fatalf("npm version command = %q", got)
	}
	if got := latestVersionCommandDescription(EcosystemGo, "example.com/core"); got != "go list -m example.com/core@latest" {
		t.Fatalf("go version command = %q", got)
	}
}

// TestDepsCovBumpReleaseLatestNpmVersion pins the injected seam, the fake
// `pnpm view` path, and the invalid/command-failure outcomes.
func TestDepsCovBumpReleaseLatestNpmVersion(t *testing.T) {
	t.Run("injected resolver", func(t *testing.T) {
		version, err := latestNpmVersion(context.Background(), "@acme/core", BumpOptions{
			LatestNpmVersion: func(context.Context, string) (string, error) { return "1.2.3", nil },
		})
		if err != nil || version != "1.2.3" {
			t.Fatalf("version = %q, err = %v", version, err)
		}
		if _, err := latestNpmVersion(context.Background(), "@acme/core", BumpOptions{
			LatestNpmVersion: func(context.Context, string) (string, error) { return "", errors.New("resolver down") },
		}); err == nil || !strings.Contains(err.Error(), "resolver down") {
			t.Fatalf("resolver error = %v", err)
		}
	})

	t.Run("pnpm view", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "pnpm", "printf '1.2.3\\n'\nexit 0\n")
		version, err := latestNpmVersion(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err != nil || version != "1.2.3" {
			t.Fatalf("version = %q, err = %v", version, err)
		}
	})

	t.Run("invalid published version", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "pnpm", "printf 'banana\\n'\nexit 0\n")
		_, err := latestNpmVersion(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), `latest npm version for @acme/core is invalid: "banana"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("pnpm failure", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "pnpm", "echo 'pnpm: registry failure' >&2\nexit 1\n")
		_, err := latestNpmVersion(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "registry failure") {
			t.Fatalf("error = %v", err)
		}
	})
}

// depsCovBumpReleaseFakePnpmRelease installs a fake `pnpm` that answers the
// `pnpm view <module> version` lookup with version and every other `view`
// lookup with fields. The two calls are told apart by their third argument,
// exactly as the real command shapes differ.
func depsCovBumpReleaseFakePnpmRelease(t *testing.T, version, fields string) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "pnpm-fields")
	writeTestFile(t, output, fields)
	depsCovBumpReleaseFakeTool(t, "pnpm",
		"if [ \"$1\" = view ]; then\n"+
			"  if [ \"$3\" = version ]; then printf '%s\\n' '"+version+"'; exit 0; fi\n"+
			"  cat '"+output+"'; exit 0\n"+
			"fi\n"+
			"exit 1\n")
}

// TestDepsCovBumpReleaseLatestPublishedNpmRelease pins the injected seam and
// the real two-command `pnpm view` path, including parse and command failures.
func TestDepsCovBumpReleaseLatestPublishedNpmRelease(t *testing.T) {
	t.Run("injected resolver", func(t *testing.T) {
		release, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			LatestNpmRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "1.2.3", Requirements: map[string]string{"@acme/dep": "1.0.0"}}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if release.Version != "1.2.3" || release.Source != "injected release resolver for @acme/core" || release.Requirements["@acme/dep"] != "1.0.0" {
			t.Fatalf("release = %+v", release)
		}

		release, err = latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			LatestNpmRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "1.2.3", Source: "npm resolver"}, nil
			},
		})
		if err != nil || release.Source != "npm resolver" {
			t.Fatalf("release = %+v, err = %v", release, err)
		}

		if _, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			LatestNpmRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{Version: "banana"}, nil
			},
		}); err == nil || !strings.Contains(err.Error(), `latest npm version for @acme/core is invalid: "banana"`) {
			t.Fatalf("invalid-version error = %v", err)
		}

		if _, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			LatestNpmRelease: func(context.Context, string) (PublishedGoRelease, error) {
				return PublishedGoRelease{}, errors.New("resolver down")
			},
		}); err == nil || !strings.Contains(err.Error(), "resolver down") {
			t.Fatalf("resolver error = %v", err)
		}
	})

	t.Run("pnpm view returns the published fields", func(t *testing.T) {
		depsCovBumpReleaseFakePnpmRelease(t, "1.2.3", `{"dependencies":{"@acme/dep":"1.0.0"}}`)
		release, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err != nil {
			t.Fatal(err)
		}
		if release.Version != "1.2.3" || release.Requirements["@acme/dep"] != "1.0.0" ||
			release.Source != "pnpm view @acme/core@1.2.3 "+strings.Join(npmDependencyFieldNames, " ") {
			t.Fatalf("release = %+v", release)
		}
	})

	t.Run("undecodable published fields", func(t *testing.T) {
		depsCovBumpReleaseFakePnpmRelease(t, "1.2.3", "not json")
		_, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "decode published npm dependency fields for @acme/core@1.2.3") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("published-field lookup fails", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "pnpm",
			"if [ \"$1\" = view ]; then\n"+
				"  if [ \"$3\" = version ]; then printf '1.2.3\\n'; exit 0; fi\n"+
				"  echo 'pnpm: registry failure' >&2; exit 1\n"+
				"fi\n"+
				"exit 1\n")
		_, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "registry failure") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("version lookup failure", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "pnpm", "echo 'pnpm: registry failure' >&2\nexit 1\n")
		_, err := latestPublishedNpmRelease(context.Background(), "@acme/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "registry failure") {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestDepsCovBumpReleaseParsePublishedNpmRequirements pins every branch of the
// published-field decoder: empty input, merged fields, null fields, malformed
// JSON, a malformed section, and a conflicting selection.
func TestDepsCovBumpReleaseParsePublishedNpmRequirements(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		output  string
		want    map[string]string
		wantErr string
	}{
		{name: "blank output", output: "   ", want: map[string]string{}},
		{name: "undefined output", output: "undefined", want: map[string]string{}},
		{name: "merged dependency fields", output: `{"dependencies":{"a":"1.0.0"},"devDependencies":{"b":"2.0.0"}}`, want: map[string]string{"a": "1.0.0", "b": "2.0.0"}},
		{name: "null field", output: `{"dependencies":null}`, want: map[string]string{}},
		{name: "malformed json", output: "not json", wantErr: "invalid character"},
		{name: "malformed section", output: `{"dependencies":"oops"}`, wantErr: "decode dependencies"},
		{name: "conflicting selections", output: `{"dependencies":{"a":"1.0.0"},"peerDependencies":{"a":"2.0.0"}}`, wantErr: "conflicting published npm selections for a"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parsePublishedNpmRequirements(testCase.output)
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("requirements = %+v, want %+v", got, testCase.want)
			}
			for key, value := range testCase.want {
				if got[key] != value {
					t.Fatalf("requirements[%q] = %q, want %q", key, got[key], value)
				}
			}
		})
	}
}

// TestDepsCovBumpReleaseLatestGoVersion pins the injected seam and every
// outcome of `go list -m -json`, driven by a fake `go` on PATH.
func TestDepsCovBumpReleaseLatestGoVersion(t *testing.T) {
	t.Run("injected resolver", func(t *testing.T) {
		version, err := latestGoVersion(context.Background(), "example.com/core", BumpOptions{
			LatestGoVersion: func(context.Context, string) (string, error) { return "v1.2.3", nil },
		})
		if err != nil || version != "v1.2.3" {
			t.Fatalf("version = %q, err = %v", version, err)
		}
		if _, err := latestGoVersion(context.Background(), "example.com/core", BumpOptions{
			LatestGoVersion: func(context.Context, string) (string, error) { return "", errors.New("resolver down") },
		}); err == nil || !strings.Contains(err.Error(), "resolver down") {
			t.Fatalf("resolver error = %v", err)
		}
	})

	t.Run("go list succeeds", func(t *testing.T) {
		depsCovBumpReleaseFakeGoOutput(t, `{"Version":"v2.3.4"}`)
		version, err := latestGoVersion(context.Background(), "example.com/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err != nil || version != "v2.3.4" {
			t.Fatalf("version = %q, err = %v", version, err)
		}
	})

	t.Run("undecodable output", func(t *testing.T) {
		depsCovBumpReleaseFakeGoOutput(t, "not json")
		_, err := latestGoVersion(context.Background(), "example.com/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "decode latest Go version for example.com/core") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid published version", func(t *testing.T) {
		depsCovBumpReleaseFakeGoOutput(t, `{"Version":"1.2.3"}`)
		_, err := latestGoVersion(context.Background(), "example.com/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), `latest Go version for example.com/core is invalid: "1.2.3"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("go command failure", func(t *testing.T) {
		depsCovBumpReleaseFakeTool(t, "go", "echo 'go: network unreachable' >&2\nexit 1\n")
		_, err := latestGoVersion(context.Background(), "example.com/core", BumpOptions{
			Options: Options{GitHubDir: t.TempDir(), Timeout: time.Minute},
		})
		if err == nil || !strings.Contains(err.Error(), "network unreachable") {
			t.Fatalf("error = %v", err)
		}
	})
}

// TestDepsCovBumpReleaseWaitForGoRelease pins the wait loop's non-happy
// paths: a missing baseline releases immediately, registry failure fails the
// observation, and a version that never advances times out (or is cancelled
// by its context).
func TestDepsCovBumpReleaseWaitForGoRelease(t *testing.T) {
	t.Run("missing baseline releases immediately", func(t *testing.T) {
		observation, err := waitForGoRelease(context.Background(), ReleaseObservation{Module: "example.com/provider"}, BumpOptions{
			PollInterval: time.Millisecond,
			LatestGoVersion: func(context.Context, string) (string, error) {
				return "v1.0.0", nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if observation.Status != "released" || observation.After != "v1.0.0" || observation.Reason != "new published provider version observed after merge" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("registry failure", func(t *testing.T) {
		observation, err := waitForGoRelease(context.Background(), ReleaseObservation{Module: "example.com/provider", Before: "v1.0.0"}, BumpOptions{
			PollInterval: time.Millisecond,
			LatestGoVersion: func(context.Context, string) (string, error) {
				return "", errors.New("registry unavailable")
			},
		})
		if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
			t.Fatalf("error = %v", err)
		}
		if observation.Status != "failed" || observation.Reason != "registry unavailable" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("times out without a newer version", func(t *testing.T) {
		observation, err := waitForGoRelease(context.Background(), ReleaseObservation{Module: "example.com/provider", Before: "v1.0.0"}, BumpOptions{
			Options:      Options{Timeout: 10 * time.Millisecond},
			PollInterval: time.Millisecond,
			LatestGoVersion: func(context.Context, string) (string, error) {
				return "v1.0.0", nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "release for example.com/provider did not advance beyond v1.0.0 before timeout") {
			t.Fatalf("error = %v", err)
		}
		if observation.Status != "awaiting_release" || observation.Reason != "waiting for a version newer than v1.0.0" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		observation, err := waitForGoRelease(ctx, ReleaseObservation{Module: "example.com/provider", Before: "v1.0.0"}, BumpOptions{
			PollInterval: time.Millisecond,
			LatestGoVersion: func(context.Context, string) (string, error) {
				cancel()
				return "v1.0.0", nil
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if observation.Status != "awaiting_release" {
			t.Fatalf("observation = %+v", observation)
		}
	})
}

// TestDepsCovBumpReleaseMergeReleaseObservations pins every merge rule: cloned
// requirement maps, merged selections, released evidence folded in, a
// require-newer observation replacing the whole release state, and CheckedAt
// never moving backwards.
func TestDepsCovBumpReleaseMergeReleaseObservations(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	only := []ReleaseObservation{{
		Module: "example.com/b", Status: "baseline",
		ExpectedRequirements: map[string]string{"example.com/provider": "v0.2.0"}, CheckedAt: base,
	}}
	merged := mergeReleaseObservations(only, []ReleaseObservation{{Module: "example.com/a", Status: "released", After: "v0.3.0"}})
	if len(merged) != 2 || merged[0].Module != "example.com/a" || merged[1].Module != "example.com/b" {
		t.Fatalf("merged = %+v", merged)
	}
	merged[1].ExpectedRequirements["example.com/provider"] = "mutated"
	if only[0].ExpectedRequirements["example.com/provider"] != "v0.2.0" {
		t.Fatal("a merged observation must not alias the caller's requirement map")
	}

	merged = mergeReleaseObservations(
		[]ReleaseObservation{{
			Module: "example.com/adapter", Status: "awaiting_release", Before: "v0.1.0",
			ExpectedRequirements: map[string]string{"example.com/provider": "v0.2.0"}, CheckedAt: base,
		}},
		[]ReleaseObservation{{
			Module: "example.com/adapter", Status: "released", After: "v0.2.0", Reason: "published",
			ExpectedRequirements: map[string]string{"example.com/other": "v1.0.0"}, CheckedAt: base.Add(time.Minute),
		}},
	)
	if len(merged) != 1 {
		t.Fatalf("merged = %+v", merged)
	}
	folded := merged[0]
	if folded.Status != "released" || folded.After != "v0.2.0" || folded.Reason != "published" || folded.Before != "v0.1.0" {
		t.Fatalf("folded observation = %+v", folded)
	}
	if folded.ExpectedRequirements["example.com/provider"] != "v0.2.0" || folded.ExpectedRequirements["example.com/other"] != "v1.0.0" {
		t.Fatalf("folded requirements = %+v", folded.ExpectedRequirements)
	}
	if !folded.CheckedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("folded CheckedAt = %s, want the newer observation", folded.CheckedAt)
	}

	merged = mergeReleaseObservations(
		[]ReleaseObservation{{
			Module: "example.com/adapter", Status: "released", After: "v9.9.9", Before: "v0.1.0",
			Source: "old", Reason: "old", CheckedAt: base.Add(time.Hour),
		}},
		[]ReleaseObservation{{
			Module: "example.com/adapter", RequireNewer: true, Before: "v0.5.0", Source: "go list -m",
			Status: "awaiting_release", Reason: "waiting", ExpectedRequirements: map[string]string{"example.com/provider": "v0.2.0"},
			CheckedAt: base,
		}},
	)
	required := merged[0]
	if !required.RequireNewer || required.Before != "v0.5.0" || required.After != "" || required.Source != "go list -m" ||
		required.Status != "awaiting_release" || required.Reason != "waiting" {
		t.Fatalf("require-newer observation = %+v", required)
	}
	if !required.CheckedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("checked-at = %s, want an older observation to leave it untouched", required.CheckedAt)
	}

	// A released observation must not overwrite an existing require-newer one.
	merged = mergeReleaseObservations(
		[]ReleaseObservation{{Module: "example.com/adapter", RequireNewer: true, Before: "v0.1.0", Status: "awaiting_release"}},
		[]ReleaseObservation{{Module: "example.com/adapter", Status: "released", After: "v0.2.0"}},
	)
	if merged[0].After != "" || merged[0].Status != "awaiting_release" {
		t.Fatalf("require-newer observation was overwritten: %+v", merged[0])
	}

	if got := mergeReleaseObservations(); len(got) != 0 {
		t.Fatalf("empty merge = %+v", got)
	}
}

// TestDepsCovBumpReleasePersistBumpFailure pins the three persist outcomes: no
// persister, a successful persist that still returns the original cause, and a
// failed persist that joins both errors.
func TestDepsCovBumpReleasePersistBumpFailure(t *testing.T) {
	cause := errors.New("wave failed")
	report := BumpReport{SchemaVersion: 1, Operation: "deps-bump-go-cov", Status: "failed"}

	if err := persistBumpFailure(BumpOptions{}, report, cause); !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the original cause when nothing persists", err)
	}

	var persisted []BumpReport
	err := persistBumpFailure(BumpOptions{Persist: func(report BumpReport) error {
		persisted = append(persisted, report)
		return nil
	}}, report, cause)
	if !errors.Is(err, cause) || len(persisted) != 1 || persisted[0].Operation != report.Operation || persisted[0].Status != "failed" {
		t.Fatalf("err = %v, persisted = %+v", err, persisted)
	}

	persistErr := errors.New("disk full")
	err = persistBumpFailure(BumpOptions{Persist: func(BumpReport) error { return persistErr }}, report, cause)
	if !errors.Is(err, cause) || !errors.Is(err, persistErr) || !strings.Contains(err.Error(), "persist dependency bump state") {
		t.Fatalf("err = %v, want both the cause and the persist failure", err)
	}
}
