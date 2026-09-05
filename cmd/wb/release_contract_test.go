package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	if got := release["uses"]; got != "strongo/cicd/.github/workflows/release.yml@19adc5f9e479df1861aea3ee9e1037c746628e4c" {
		t.Fatalf("release uses=%v", got)
	}
	assert("release prerequisites", release["needs"], []any{"test", "release-eligibility"})
	assert("release gate", strings.Join(strings.Fields(fmt.Sprint(release["if"])), " "),
		"${{ !cancelled() && needs.test.result == 'success' && needs.release-eligibility.result == 'success' && needs.release-eligibility.outputs.eligible == 'true' }}")
	assert("release inputs", release["with"], map[string]any{
		"go_version": "1.27", "default_bump": "patch",
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
	assert("aggregate prerequisites", aggregate["needs"], []any{"release-eligibility", "validation-reuse", "source", "static", "lint", "coverage", "race"})
	assert("aggregate failure reporting", aggregate["if"], "${{ always() }}")
	for _, name := range []string{"source", "static", "lint", "coverage", "race"} {
		job, ok := jobs[name].(map[string]any)
		if !ok {
			t.Fatalf("validation job %s missing", name)
		}
		assert(name+" starts after eligibility and reuse", job["needs"], []any{"release-eligibility", "validation-reuse"})
		assert(name+" reuse condition", strings.Join(strings.Fields(fmt.Sprint(job["if"])), " "),
			"github.event_name != 'push' || needs.validation-reuse.outputs.reuse != 'true'")
	}
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
	assert("eligibility checkout action", checkout["uses"], "actions/checkout@v6")
	assert("eligibility history", checkout["with"], map[string]any{"fetch-depth": 0})
	assert("eligibility output", eligibility["outputs"], map[string]any{"eligible": "${{ steps.eligibility.outputs.eligible }}"})
	if _, err := os.Stat(filepath.Join(repoRoot, ".github", "workflows", "release.yml")); !os.IsNotExist(err) {
		t.Fatalf("independent release workflow must be removed, stat error=%v", err)
	}
	quickRace, _ := jobs["race"].(map[string]any)
	assert("quick race command", workflowContractTestCommands(t, quickRace), []string{
		"go test -race -timeout 15m ./internal/deps/... ./internal/githubobserver/... ./internal/fleetsync/...",
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
	if !ok || len(raceJobs) != 1 {
		t.Fatal("race jobs missing")
	}
	raceJob, ok := raceJobs["race"].(map[string]any)
	if !ok {
		t.Fatalf("race job=%v", raceJob)
	}
	assert("full race command", workflowContractTestCommands(t, raceJob), []string{"go test -count=1 -race -timeout 40m ./..."})
	assert("full race timeout", raceJob["timeout-minutes"], 45)
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

func TestGoCIRequiredChecksRejectIncompleteValidation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "go-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string
				Env map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["test"].Steps
	if len(steps) != 1 || steps[0].Run == "" {
		t.Fatal("required check must only summarize the validation results")
	}
	step := steps[0]
	if len(step.Env) != 8 {
		t.Fatalf("summary receives %d environment values, want the eight eligibility/reuse/validation values", len(step.Env))
	}
	runValues := func(values map[string]string) error {
		cmd := exec.Command("sh", "-c", step.Run)
		cmd.Env = os.Environ()
		for key := range step.Env {
			value := "success"
			if override, ok := values[key]; ok {
				value = override
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		return cmd.Run()
	}
	run := func(failedKey, result string) error {
		return runValues(map[string]string{failedKey: result})
	}
	if err := run("", ""); err != nil {
		t.Fatalf("all successful prerequisites rejected: %v", err)
	}
	for key := range step.Env {
		if key == "REUSE_RESULT" {
			continue
		}
		for _, result := range []string{"failure", "cancelled", "skipped", ""} {
			t.Run(key+"/"+result, func(t *testing.T) {
				if err := run(key, result); err == nil {
					t.Fatalf("summary accepted %s=%q", key, result)
				}
			})
		}
	}
	t.Run("trusted validation reuse", func(t *testing.T) {
		values := map[string]string{
			"ELIGIBILITY_RESULT": "success",
			"REUSE_RESULT":       "true",
			"REUSE_JOB_RESULT":   "success",
			"SOURCE_RESULT":      "skipped",
			"STATIC_RESULT":      "skipped",
			"LINT_RESULT":        "skipped",
			"COVERAGE_RESULT":    "skipped",
			"RACE_RESULT":        "skipped",
		}
		if err := runValues(values); err != nil {
			t.Fatalf("trusted reuse was rejected: %v", err)
		}
	})
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
	write(".github/workflows/go-ci.yml")
	git("add", ".")
	git("commit", "-m", "workflow")
	workflow := git("rev-parse", "HEAD")
	if got := run(head, workflow); got != "eligible=true" {
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
