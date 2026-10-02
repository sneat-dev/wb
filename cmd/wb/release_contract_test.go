package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"gopkg.in/yaml.v3"
)

func TestReleaseSignsAndNotarizesMacOSArtifacts(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	releasePath := filepath.Join(repoRoot, ".github", "workflows", "go-ci.yml")
	releaseContents, err := os.ReadFile(releasePath)
	if err != nil {
		t.Fatal(err)
	}
	goreleaserPath := filepath.Join(repoRoot, ".goreleaser.yml")
	goreleaserContents, err := os.ReadFile(goreleaserPath)
	if err != nil {
		t.Fatal(err)
	}

	// A prior "containment" period disabled macOS code signing under a
	// diagnosis that has since been proven wrong. Exit-137 SIGKILLs were
	// blamed on Go 1.27 Mach-O output; the real cause was that the .p12
	// signing bundle had been rebuilt without the Apple Root CA. quill sorts
	// the cert chain root-first and emits `root` for whatever sits at index
	// 0; with only leaf + G2 intermediate present, the G2 CA landed at index
	// 0, so the designated requirement read
	// `certificate root[field.1.2.840.113635.100.6.2.6]` — an OID the actual
	// Apple Root CA (what macOS resolves `root` to) does not carry, so the
	// requirement was unsatisfiable and the binary was killed.
	//
	// The .p12 has been rebuilt with the full leaf + intermediate + root
	// chain and pushed to all org secret stores. Proof it works, on a real
	// published Go 1.27 artifact: ingitdb/ingitdb-cli v0.65.11 (2026-08-30)
	// satisfies its designated requirement, chains to the Apple Root CA,
	// passes `spctl --assess` as Notarized Developer ID, and executes
	// (rc=0). So signing and notarization must be wired up, not withheld.
	for _, required := range []string{"MACOS_SIGN_P12", "MACOS_SIGN_PASSWORD", "NOTARIZE_ISSUER_ID", "NOTARIZE_KEY_ID", "NOTARIZE_KEY"} {
		if !strings.Contains(string(releaseContents), required) {
			t.Errorf("%s must forward macOS signing/notarization secret %s", releasePath, required)
		}
	}
	if !strings.Contains(string(releaseContents), "require_notarized_macos: true") {
		t.Errorf("%s must set require_notarized_macos: true", releasePath)
	}
	if !strings.Contains(string(goreleaserContents), "notarize:") {
		t.Errorf("%s must enable the notarize: block for macOS artifacts", goreleaserPath)
	}
	if !strings.Contains(string(goreleaserContents), "homepage: https://sneat.work/bench") {
		t.Errorf("%s must publish the canonical Workbench homepage", goreleaserPath)
	}
	if strings.Contains(string(goreleaserContents), "com.apple.quarantine") {
		t.Errorf("%s must not bypass Gatekeeper for signed and notarized macOS artifacts", goreleaserPath)
	}
}

func TestPublicInstallDocumentationMatchesReleaseContract(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(readme)
	for _, command := range []string{
		selfUpdateHomebrewInstallCommand,
		"curl -fsSL https://sneat.work/bench/install/get-cli | sh",
		"go install github.com/sneat-dev/wb/cmd/wb@latest",
	} {
		if !strings.Contains(contents, command) {
			t.Errorf("README.md must document supported install command %q", command)
		}
	}
	if !strings.Contains(contents, "On macOS or Linux, install the published Homebrew cask") {
		t.Error("README.md must document the Homebrew cask for macOS and Linux")
	}
	if !strings.Contains(contents, "Native Windows releases are not currently published") {
		t.Error("README.md must state the current Windows release limitation")
	}
	if !strings.Contains(contents, "wsl --install") ||
		!strings.Contains(contents, "wsl sh -lc 'curl -fsSL https://sneat.work/bench/install/get-cli | sh'") ||
		!strings.Contains(contents, "WB running in WSL") {
		t.Error("README.md must document WSL as the supported Windows installation path")
	}
}

func TestReleaseEligibilityRestrictsPublicationRefs(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	script := filepath.Join(repoRoot, ".github", "scripts", "release-eligible.sh")
	for _, test := range []struct {
		name, event, ref, want string
	}{
		{"release tag push", "push", "refs/tags/v0.92.2", "true"},
		{"manual main", "workflow_dispatch", "refs/heads/main", "true"},
		{"manual release tag", "workflow_dispatch", "refs/tags/v0.92.2", "true"},
		{"pull request", "pull_request", "refs/pull/17/merge", "false"},
		{"pull request cannot claim main", "pull_request", "refs/heads/main", "false"},
		{"manual feature branch", "workflow_dispatch", "refs/heads/feature/test", "false"},
		{"feature push", "push", "refs/heads/feature/test", "false"},
		{"non-release tag", "push", "refs/tags/test", "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", script, test.event, test.ref, "", "")
			output, err := command.Output()
			if err != nil {
				t.Fatalf("run release eligibility: %v", err)
			}
			if got, want := strings.TrimSpace(string(output)), "eligible="+test.want; got != want {
				t.Fatalf("eligibility output = %q, want %q", got, want)
			}
		})
	}
}

func TestGoCICoordinatesTheOnlyPublisherAndRaceInventory(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	goCIPath := filepath.Join(repoRoot, ".github", "workflows", "go-ci.yml")
	rawGoCI, err := os.ReadFile(goCIPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(rawGoCI, &workflow); err != nil {
		t.Fatalf("parse %s: %v", goCIPath, err)
	}
	assert := func(label string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", label, got, want)
		}
	}
	assert("CI triggers", workflow["on"], map[string]any{
		"push":         map[string]any{"branches": []any{"main"}, "tags": []any{"v*"}},
		"pull_request": nil, "workflow_dispatch": nil,
	})
	assert("CI permissions", workflow["permissions"], map[string]any{
		"actions": "read", "contents": "read", "pull-requests": "read",
	})
	assert("CI concurrency", workflow["concurrency"], map[string]any{
		"group":              "go-ci-${{ github.workflow }}-${{ github.ref }}",
		"cancel-in-progress": "${{ github.event_name == 'pull_request' }}",
	})
	jobs, ok := workflow["jobs"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no jobs map", goCIPath)
	}
	release, ok := jobs["release"].(map[string]any)
	if !ok {
		t.Fatal("go-ci release job missing")
	}
	if got := release["uses"]; got != "strongo/cicd/.github/workflows/release.yml@5d96b1f3fbb3f12bb1e2762ff5ba54ccb9506504" {
		t.Fatalf("release uses=%v", got)
	}
	assert("release prerequisites", release["needs"], []any{"test", "release-eligibility", "go-scope"})
	assert("release gate", strings.Join(strings.Fields(fmt.Sprint(release["if"])), " "),
		"${{ !cancelled() && needs.test.result == 'success' && needs.go-scope.outputs.required == 'true' && needs.release-eligibility.result == 'success' && needs.release-eligibility.outputs.eligible == 'true' }}")
	assert("release inputs", release["with"], map[string]any{
		"go_version": "1.27", "node_version": "24.15.0", "default_bump": "patch",
		"require_notarized_macos": true, "allow_major_version_bump": false,
	})
	expectedSecrets := map[string]any{"GORELEASER_GITHUB_TOKEN": "${{ secrets.WB_GORELEASER_GITHUB_TOKEN }}"}
	for _, name := range []string{"MACOS_SIGN_P12", "MACOS_SIGN_PASSWORD", "NOTARIZE_ISSUER_ID", "NOTARIZE_KEY_ID", "NOTARIZE_KEY"} {
		expectedSecrets[name] = "${{ secrets." + name + " }}"
	}
	assert("release secrets", release["secrets"], expectedSecrets)
	assert("release permissions", release["permissions"], map[string]any{"contents": "write"})
	assert("release concurrency", release["concurrency"], map[string]any{
		"group": "wb-release-${{ github.ref }}", "cancel-in-progress": false,
	})
	aggregate, ok := jobs["test"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate=%v", aggregate)
	}
	assert("required check name", aggregate["name"], "Required checks passed")
	assert("aggregate prerequisites", aggregate["needs"], []any{"release-eligibility", "validation-reuse", "go-scope", "go-contract-inputs", "source", "static", "lint", "coverage", "race", "e2e", "windows"})
	assert("aggregate failure reporting", aggregate["if"], "${{ always() }}")
	for _, name := range []string{"source", "static", "lint", "race", "e2e"} {
		job, ok := jobs[name].(map[string]any)
		if !ok {
			t.Fatalf("validation job %s missing", name)
		}
		assert(name+" starts after eligibility, reuse and Go scope", job["needs"], []any{"release-eligibility", "validation-reuse", "go-scope"})
		assert(name+" scope and reuse condition", strings.Join(strings.Fields(fmt.Sprint(job["if"])), " "),
			"needs.go-scope.outputs.required == 'true' && (github.event_name != 'push' || needs.validation-reuse.outputs.reuse != 'true')")
	}
	// Coverage shares exact trusted PR reuse; nightly publishes full artifacts.
	coverageJob, ok := jobs["coverage"].(map[string]any)
	if !ok {
		t.Fatal("validation job coverage missing")
	}
	assert("coverage starts after eligibility, reuse and Go scope", coverageJob["needs"], []any{"release-eligibility", "validation-reuse", "go-scope"})
	assert("coverage scope and reuse condition", strings.Join(strings.Fields(fmt.Sprint(coverageJob["if"])), " "),
		"needs.go-scope.outputs.required == 'true' && (github.event_name != 'push' || needs.validation-reuse.outputs.reuse != 'true')")
	goScope, ok := jobs["go-scope"].(map[string]any)
	if !ok {
		t.Fatal("Go validation scope job missing")
	}
	goScopeSteps, ok := goScope["steps"].([]any)
	if !ok || len(goScopeSteps) != 2 {
		t.Fatalf("Go validation scope steps=%v", goScope["steps"])
	}
	goScopeCheckout, _ := goScopeSteps[0].(map[string]any)
	assert("Go scope checkout action", goScopeCheckout["uses"], "actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803")
	assert("Go scope fetches comparison history", goScopeCheckout["with"], map[string]any{"fetch-depth": 0})
	decide, _ := goScopeSteps[1].(map[string]any)
	if !strings.Contains(fmt.Sprint(decide["run"]), "bash .github/scripts/go-scope.sh") {
		t.Fatal("Go scope decision must execute the tested classifier")
	}
	assert("Go scope output", goScope["outputs"], map[string]any{"required": "${{ steps.decide.outputs.required }}", "contract_required": "${{ steps.decide.outputs.contract_required }}"})
	contract, ok := jobs["go-contract-inputs"].(map[string]any)
	if !ok {
		t.Fatal("non-Go contract inputs job missing")
	}
	assert("contract inputs gate", contract["if"], "needs.go-scope.outputs.contract_required == 'true'")
	assert("contract inputs commands", workflowContractTestCommands(t, contract), []string{
		"go test ./cmd/wb -count=1 -run '^(TestModuleArchiveIncludesCmdWBEmbedInputs|TestCapabilityManifestKeepsImplementationHelpAndSkillsInOne|TestWBMergeSkillIsOnePortableContract|TestWBChangeCompletionContractIsSharedAcrossHarnesses|TestPublicInstallDocumentationMatchesReleaseContract|TestAgentSkillsCoverPublicCommands)$'",
	})
	receipt, _ := jobs["validation-receipt"].(map[string]any)
	assert("scoped-out pull requests publish no receipt", receipt["if"],
		"${{ !cancelled() && github.event_name == 'pull_request' && needs.test.result == 'success' && needs.go-scope.outputs.required == 'true' }}")
	windowsScope, ok := jobs["windows-scope"].(map[string]any)
	if !ok {
		t.Fatal("native Windows scope job missing")
	}
	windowsScopeSteps, ok := windowsScope["steps"].([]any)
	if !ok || len(windowsScopeSteps) < 2 {
		t.Fatalf("native Windows scope steps=%v", windowsScope["steps"])
	}
	windowsScopeCheckout, _ := windowsScopeSteps[0].(map[string]any)
	assert("Windows scope checkout action", windowsScopeCheckout["uses"], "actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803")
	windows, ok := jobs["windows"].(map[string]any)
	if !ok {
		t.Fatal("native Windows validation job missing")
	}
	assert("Windows validation prerequisites", windows["needs"], []any{"windows-scope", "validation-reuse", "go-scope"})
	assert("Windows validation reuse condition", strings.Join(strings.Fields(fmt.Sprint(windows["if"])), " "),
		"needs.windows-scope.outputs.required == 'true' && needs.go-scope.outputs.required == 'true' && (github.event_name != 'push' || needs.validation-reuse.outputs.reuse != 'true')")
	assert("Windows validation commands", workflowContractTestCommands(t, windows), []string{
		"go build ./...",
		"go vet ./...",
		"go test ./internal/session -run '^TestLookupExactRefusesLinkedRecordsAndRequiresLivePID$'",
		"go test ./internal/lifecyclehooks -run '^TestWindowsTrust'",
		"go test ./internal/unixcompat ./internal/archiveprune ./cmd/wb -run '^(TestOpenNoFollowTransfersSingleHandleOwnership|TestFstatIdentityMatchesFstatat|TestWindowsPlanUntrackedSimpleFile|TestWindowsDaemon)'",
		"go test ./api/githubapp -count=1",
	})
	eligibility, ok := jobs["release-eligibility"].(map[string]any)
	if !ok {
		t.Fatal("eligibility job missing")
	}
	steps, ok := eligibility["steps"].([]any)
	if !ok || len(steps) == 0 {
		t.Fatal("eligibility steps missing")
	}
	checkout, _ := steps[0].(map[string]any)
	if _, conditional := checkout["if"]; conditional {
		t.Fatalf("eligibility checkout=%v", checkout)
	}
	assert("eligibility checkout action", checkout["uses"], "actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803")
	assert("eligibility history", checkout["with"], map[string]any{"fetch-depth": 0})
	assert("eligibility output", eligibility["outputs"], map[string]any{"eligible": "${{ steps.eligibility.outputs.eligible }}"})
	if _, err := os.Stat(filepath.Join(repoRoot, ".github", "workflows", "release.yml")); !os.IsNotExist(err) {
		t.Fatalf("independent release workflow must be removed, stat error=%v", err)
	}
	quickRace, _ := jobs["race"].(map[string]any)
	assert("quick race command", workflowContractTestCommands(t, quickRace), []string{
		"go test -race -timeout 15m ./internal/deps/... ./internal/githubobserver/... ./internal/lifecyclehooks/... ./internal/fleetsync/... ./internal/runqueue/...",
	})

	racePath := filepath.Join(repoRoot, ".github", "workflows", "race.yml")
	rawRace, err := os.ReadFile(racePath)
	if err != nil {
		t.Fatal(err)
	}
	var raceWorkflow map[string]any
	if err := yaml.Unmarshal(rawRace, &raceWorkflow); err != nil {
		t.Fatalf("parse %s: %v", racePath, err)
	}
	assert("full race triggers", raceWorkflow["on"], map[string]any{
		"schedule": []any{map[string]any{"cron": "0 3 * * *"}}, "workflow_dispatch": nil,
	})
	assert("full race permissions", raceWorkflow["permissions"], map[string]any{"contents": "read"})
	raceJobs, ok := raceWorkflow["jobs"].(map[string]any)
	// The full race sweep is sharded across internal/orchestrate,
	// internal/worktrees and everything else (issue #728: orchestrate hit
	// the shared 40m go-test timeout in run 35974691603; measured alone on
	// its own runner in run 36051478734, orchestrate took 2887s and
	// worktrees took 2399.873s — both packages simply take that long under
	// -race), plus a fast job that asserts the three shards, as
	// .github/scripts/race-shards.sh defines them, are disjoint and still
	// union back to exactly `go list ./...` (PR #731 review B1: the guard
	// must read the same shard-membership script the three jobs use, not
	// recompute its own copy that can never disagree with itself).
	if !ok || len(raceJobs) != 4 {
		t.Fatalf("race jobs = %v, want race-orchestrate, race-worktrees, race-rest and race-shards-cover-all-packages", raceJobs)
	}
	orchestrateJob, ok := raceJobs["race-orchestrate"].(map[string]any)
	if !ok {
		t.Fatalf("race-orchestrate job=%v", orchestrateJob)
	}
	assert("orchestrate race command", workflowContractTestCommands(t, orchestrateJob), []string{
		"set -euo pipefail packages=$(.github/scripts/race-shards.sh orchestrate) go test -count=1 -race -timeout 80m $packages",
	})
	assert("orchestrate race timeout", orchestrateJob["timeout-minutes"], 90)
	worktreesJob, ok := raceJobs["race-worktrees"].(map[string]any)
	if !ok {
		t.Fatalf("race-worktrees job=%v", worktreesJob)
	}
	assert("worktrees race command", workflowContractTestCommands(t, worktreesJob), []string{
		"set -euo pipefail packages=$(.github/scripts/race-shards.sh worktrees) go test -count=1 -race -timeout 70m $packages",
	})
	assert("worktrees race timeout", worktreesJob["timeout-minutes"], 75)
	restJob, ok := raceJobs["race-rest"].(map[string]any)
	if !ok {
		t.Fatalf("race-rest job=%v", restJob)
	}
	assert("rest race command", workflowContractTestCommands(t, restJob), []string{
		"set -euo pipefail packages=$(.github/scripts/race-shards.sh rest) go test -count=1 -race -timeout 40m $packages",
	})
	assert("rest race timeout", restJob["timeout-minutes"], 45)
	shardCoverageJob, ok := raceJobs["race-shards-cover-all-packages"].(map[string]any)
	if !ok {
		t.Fatalf("race-shards-cover-all-packages job=%v", shardCoverageJob)
	}
	assert("shard coverage command", workflowContractTestCommands(t, shardCoverageJob), []string{
		"set -euo pipefail go list ./... | sort > all.txt { .github/scripts/race-shards.sh orchestrate .github/scripts/race-shards.sh worktrees .github/scripts/race-shards.sh rest } > shards.txt duplicates=$(sort shards.txt | uniq -d || true) if [ -n \"$duplicates\" ]; then echo \"::error::race.yml shards overlap; a package must belong to exactly one shard:\" >&2 printf '%s\\n' \"$duplicates\" >&2 exit 1 fi sort -u shards.txt > union.txt if ! diff -u all.txt union.txt; then echo \"::error::race.yml shards do not cover every package; see the diff above\" >&2 exit 1 fi",
	})
	assert("shard coverage timeout", shardCoverageJob["timeout-minutes"], 10)
	var publishers []string
	files, err := os.ReadDir(filepath.Dir(goCIPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if ext := filepath.Ext(file.Name()); ext != ".yml" && ext != ".yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(goCIPath), file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var other struct {
			Jobs map[string]struct{ Uses string }
		}
		if err := yaml.Unmarshal(raw, &other); err != nil {
			t.Fatal(err)
		}
		for name, job := range other.Jobs {
			if strings.HasPrefix(job.Uses, "strongo/cicd/.github/workflows/release.yml@") {
				publishers = append(publishers, file.Name()+":"+name)
			}
		}
	}
	assert("only CLI publisher", publishers, []string{"go-ci.yml:release"})
}

// raceShardCoverageGuard is exactly the disjointness/coverage check
// race.yml's race-shards-cover-all-packages job runs, reimplemented in Go so
// it can be pointed at a real (or deliberately broken) race-shards.sh copy
// and asserted against. It intentionally mirrors the workflow's shell
// step, not `go list`'s own behaviour, so a change to the script's shard
// membership can be size-checked here first.
func raceShardCoverageGuard(t *testing.T, repoRoot, script string) error {
	t.Helper()
	all := workflowContractTestGoList(t, repoRoot, "./...")
	var shards []string
	for _, name := range []string{"orchestrate", "worktrees", "rest"} {
		command := exec.Command("sh", script, name)
		command.Dir = repoRoot
		out, err := command.Output()
		if err != nil {
			return fmt.Errorf("race-shards.sh %s: %w", name, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				shards = append(shards, line)
			}
		}
	}
	seen := map[string]bool{}
	for _, pkg := range shards {
		if seen[pkg] {
			return fmt.Errorf("package %s appears in more than one shard", pkg)
		}
		seen[pkg] = true
	}
	allSet := map[string]bool{}
	for _, pkg := range all {
		allSet[pkg] = true
		if !seen[pkg] {
			return fmt.Errorf("package %s is in `go list ./...` but in no shard", pkg)
		}
	}
	for _, pkg := range shards {
		if !allSet[pkg] {
			return fmt.Errorf("package %s is in a shard but not in `go list ./...`", pkg)
		}
	}
	return nil
}

func workflowContractTestGoList(t *testing.T, dir, pattern string) []string {
	t.Helper()
	command := exec.Command("go", "list", pattern)
	command.Dir = dir
	out, err := command.Output()
	if err != nil {
		t.Fatalf("go list %s: %v", pattern, err)
	}
	var packages []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			packages = append(packages, line)
		}
	}
	return packages
}

// TestRaceShardsScriptIsDisjointAndCompleteAndTheGuardCatchesDrift proves,
// against the real repository, both that today's .github/scripts/race-shards.sh
// passes race.yml's guard and that the guard genuinely fails when the
// script drifts (PR #731 review B1: a guard that only recomputes its own
// copy of the shard lists can never actually fail).
func TestRaceShardsScriptIsDisjointAndCompleteAndTheGuardCatchesDrift(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repoRoot, ".github", "scripts", "race-shards.sh")
	if err := raceShardCoverageGuard(t, repoRoot, script); err != nil {
		t.Fatalf("today's race-shards.sh must satisfy the guard: %v", err)
	}

	writeBrokenScript := func(t *testing.T, contents string) string {
		t.Helper()
		broken := filepath.Join(t.TempDir(), "race-shards.sh")
		if err := testenv.WriteExecutableFile(broken, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
		return broken
	}

	t.Run("rest excluding an extra package leaves it in no shard", func(t *testing.T) {
		broken := writeBrokenScript(t, `#!/bin/sh
set -eu
shard=${1:?}
case "$shard" in
  orchestrate) go list ./internal/orchestrate/... ;;
  worktrees) go list ./internal/worktrees/... ;;
  rest) go list ./... | grep -v -E '^github\.com/sneat-dev/wb/internal/(orchestrate|worktrees|deps)(/|$)' ;;
esac
`)
		err := raceShardCoverageGuard(t, repoRoot, broken)
		if err == nil {
			t.Fatal("guard passed against a script that drops internal/deps from every shard")
		}
		if !strings.Contains(err.Error(), "internal/deps") {
			t.Fatalf("guard error = %v, want it to name internal/deps", err)
		}
	})

	t.Run("rest forgetting to exclude orchestrate duplicates it", func(t *testing.T) {
		broken := writeBrokenScript(t, `#!/bin/sh
set -eu
shard=${1:?}
case "$shard" in
  orchestrate) go list ./internal/orchestrate/... ;;
  worktrees) go list ./internal/worktrees/... ;;
  rest) go list ./... | grep -v -E '^github\.com/sneat-dev/wb/internal/worktrees(/|$)' ;;
esac
`)
		err := raceShardCoverageGuard(t, repoRoot, broken)
		if err == nil {
			t.Fatal("guard passed against a script that runs internal/orchestrate in two shards")
		}
		if !strings.Contains(err.Error(), "internal/orchestrate") || !strings.Contains(err.Error(), "more than one shard") {
			t.Fatalf("guard error = %v, want it to name the internal/orchestrate duplication", err)
		}
	})
}

func TestGoCIRequiredChecksRejectIncompleteValidation(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string
				Run  string
				Env  map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["test"].Steps
	if len(steps) != 2 || steps[0].Uses != "actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803" || steps[1].Run != "bash .github/scripts/go-ci-required.sh" {
		t.Fatalf("required check must checkout and execute the tested aggregate: %#v", steps)
	}
	if len(steps[1].Env) != 15 {
		t.Fatalf("aggregate receives %d environment values, want fifteen", len(steps[1].Env))
	}
	for _, name := range []string{"test-go-scope.sh", "test-go-ci-required.sh"} {
		command := exec.Command("bash", filepath.Join(".github", "scripts", name))
		command.Dir = repoRoot
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, output)
		}
	}
}

func workflowContractTestCommands(t *testing.T, job map[string]any) []string {
	t.Helper()
	if _, ignored := job["continue-on-error"]; ignored {
		t.Fatal("race job must report failures")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("race steps missing")
	}
	var commands []string
	for _, rawStep := range steps {
		step, ok := rawStep.(map[string]any)
		if !ok {
			t.Fatal("race step is not a mapping")
		}
		if _, ignored := step["continue-on-error"]; ignored {
			t.Fatal("race step must report failures")
		}
		if command, ok := step["run"].(string); ok {
			commands = append(commands, strings.Join(strings.Fields(command), " "))
		}
	}
	return commands
}

func TestReleaseEligibilityUsesGitChangeSets(t *testing.T) {
	script := filepath.Join(filepath.Clean(filepath.Join("..", "..")), ".github", "scripts", "release-eligible.sh")
	script, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(name string) {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	git("config", "user.email", "wb-test@example.invalid")
	git("config", "user.name", "WB test")
	write("docs/base.md")
	write("go.mod")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	write("cmd/wb/main.go")
	git("add", ".")
	git("commit", "-m", "cli")
	cli := git("rev-parse", "HEAD")
	write("docs/last.md")
	git("add", ".")
	git("commit", "-m", "docs")
	head := git("rev-parse", "HEAD")
	run := func(before, sha string) string {
		command := exec.Command("sh", script, "push", "refs/heads/main", before, sha)
		command.Dir = repo
		output, err := command.Output()
		if err != nil {
			t.Fatalf("eligibility: %v", err)
		}
		return strings.TrimSpace(string(output))
	}
	if got := run(base, head); got != "eligible=true" {
		t.Fatalf("multi-commit CLI then docs = %q", got)
	}
	if got := run(cli, head); got != "eligible=false" {
		t.Fatalf("docs-only = %q", got)
	}
	write("api/githubapp/provider.go")
	git("add", ".")
	git("commit", "-m", "github app api")
	api := git("rev-parse", "HEAD")
	if got := run(head, api); got != "eligible=true" {
		t.Fatalf("github app api = %q", got)
	}
	write("docs/after-api.md")
	git("add", ".")
	git("commit", "-m", "docs after api")
	docsAfterAPI := git("rev-parse", "HEAD")
	if got := run(api, docsAfterAPI); got != "eligible=false" {
		t.Fatalf("docs-only after api = %q", got)
	}
	write(".github/workflows/go-ci.yml")
	git("add", ".")
	git("commit", "-m", "workflow")
	workflow := git("rev-parse", "HEAD")
	if got := run(docsAfterAPI, workflow); got != "eligible=true" {
		t.Fatalf("workflow-only = %q", got)
	}
	write(".github/scripts/release-eligible.sh")
	git("add", ".")
	git("commit", "-m", "script")
	scriptHead := git("rev-parse", "HEAD")
	if got := run(workflow, scriptHead); got != "eligible=true" {
		t.Fatalf("script-only = %q", got)
	}
	if got := run("0000000000000000000000000000000000000000", base); got != "eligible=true" {
		t.Fatalf("zero-before actual root commit = %q", got)
	}
	command := exec.Command("sh", script, "push", "refs/heads/main", "missing", scriptHead)
	command.Dir = repo
	if err := command.Run(); err == nil {
		t.Fatal("missing base must fail closed")
	}
}
