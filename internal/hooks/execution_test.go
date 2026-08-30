package hooks

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestBuiltInNodePrePushSelectsPackageManager(t *testing.T) {
	tests := []struct {
		name     string
		lockfile string
		manager  string
	}{
		{name: "npm by default", manager: "npm"},
		{name: "pnpm lockfile", lockfile: "pnpm-lock.yaml", manager: "pnpm"},
		{name: "yarn lockfile", lockfile: "yarn.lock", manager: "yarn"},
		{name: "bun lockfile", lockfile: "bun.lock", manager: "bun"},
		{name: "legacy bun lockfile", lockfile: "bun.lockb", manager: "bun"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, toolLog := prepareNodeProfileTest(t, test.lockfile, "lint,test", "node", "npm", "pnpm", "yarn", "bun")

			result, err := Run(RunOptions{
				RepoPath:     repo,
				Hook:         "pre-push",
				Stdin:        strings.NewReader("refs/heads/main\n"),
				Stdout:       &bytes.Buffer{},
				Stderr:       &bytes.Buffer{},
				WBExecutable: testPushTierExecutable(t, 2),
			})
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("run result = %#v, error = %v", result, err)
			}
			if got, want := readLogLines(t, toolLog), []string{
				"node:lint",
				test.manager + ":run lint",
				"node:test",
				test.manager + ":run test",
			}; !reflect.DeepEqual(got, want) {
				t.Fatalf("tool calls = %v, want %v", got, want)
			}
		})
	}
}

func TestBuiltInNodePrePushSkipsUndefinedScripts(t *testing.T) {
	repo, toolLog := prepareNodeProfileTest(t, "", "lint", "node", "npm")
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, WBExecutable: testPushTierExecutable(t, 2)})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("run result = %#v, error = %v", result, err)
	}
	if got, want := readLogLines(t, toolLog), []string{"node:lint", "npm:run lint", "node:test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool calls = %v, want %v", got, want)
	}
}

func TestBuiltInNodePrePushReportsMissingTools(t *testing.T) {
	for _, test := range []struct {
		name      string
		lockfile  string
		tools     []string
		wantError string
	}{
		{name: "package manager", lockfile: "pnpm-lock.yaml", tools: []string{"node"}, wantError: "Required Node package manager not found: pnpm"},
		{name: "runtime", tools: []string{"npm"}, wantError: "Required Node runtime not found: node"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := prepareNodeProfileTest(t, test.lockfile, "lint,test", test.tools...)
			var stderr bytes.Buffer
			result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdout: &bytes.Buffer{}, Stderr: &stderr})
			if err == nil || result.ExitCode != 1 || !strings.Contains(stderr.String(), test.wantError) {
				t.Fatalf("run result = %#v, error = %v, stderr = %q", result, err, stderr.String())
			}
		})
	}
}

func TestGeneratedHookRunsUserSectionsAroundManagedDispatcher(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	toolLog := filepath.Join(t.TempDir(), "hook.log")
	t.Setenv("WB_TEST_LOG", toolLog)
	fakeWB := filepath.Join(t.TempDir(), "wb test")
	mustWriteExecutable(t, fakeWB, `#!/bin/sh
printf 'wb' >> "$WB_TEST_LOG"
for arg in "$@"; do
    printf '|%s' "$arg" >> "$WB_TEST_LOG"
done
printf '\n' >> "$WB_TEST_LOG"
exit "${WB_TEST_EXIT:-0}"
`)

	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: fakeWB})
	if err != nil {
		t.Fatal(err)
	}
	prePush := filepath.Join(result.Report.ManagedPath, "pre-push")
	installed, err := os.ReadFile(prePush)
	if err != nil {
		t.Fatal(err)
	}
	withUserSections := strings.Replace(string(installed), "#!/bin/sh\nset -eu\n\n", "#!/bin/sh\nset -eu\nprintf 'user-pre\\n' >> \"$WB_TEST_LOG\"\n\n", 1) +
		"printf 'user-post\\n' >> \"$WB_TEST_LOG\"\n"
	mustWrite(t, prePush, withUserSections)

	t.Run("success preserves arguments and order", func(t *testing.T) {
		mustWrite(t, toolLog, "")
		command := exec.Command(prePush, "origin", "ssh://example.invalid/repo with spaces")
		command.Env = hookEnvironment(map[string]string{"WB_EXECUTABLE": fakeWB, "WB_TEST_EXIT": "0"})
		if output, runErr := command.CombinedOutput(); runErr != nil {
			t.Fatalf("run generated hook: %v\n%s", runErr, output)
		}
		if got, want := readLogLines(t, toolLog), []string{
			"user-pre",
			"wb|hooks|run|pre-push|--|origin|ssh://example.invalid/repo with spaces",
			"user-post",
		}; !reflect.DeepEqual(got, want) {
			t.Fatalf("hook execution = %v, want %v", got, want)
		}
	})

	t.Run("failure preserves status and skips user post section", func(t *testing.T) {
		mustWrite(t, toolLog, "")
		command := exec.Command(prePush, "origin")
		command.Env = hookEnvironment(map[string]string{"WB_EXECUTABLE": fakeWB, "WB_TEST_EXIT": "17"})
		output, runErr := command.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 17 {
			t.Fatalf("generated hook error = %v, output = %s", runErr, output)
		}
		if got, want := readLogLines(t, toolLog), []string{"user-pre", "wb|hooks|run|pre-push|--|origin"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("hook execution = %v, want %v", got, want)
		}
	})

	repaired, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: fakeWB, Repair: true})
	if err != nil || len(repaired.Report.Findings) != 0 {
		t.Fatalf("repair result = %#v, error = %v", repaired, err)
	}
	mustWrite(t, toolLog, "")
	command := exec.Command(prePush, "origin")
	command.Env = hookEnvironment(map[string]string{"WB_EXECUTABLE": fakeWB, "WB_TEST_EXIT": "0"})
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("run repaired hook: %v\n%s", runErr, output)
	}
	if got, want := readLogLines(t, toolLog), []string{"user-pre", "wb|hooks|run|pre-push|--|origin", "user-post"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repaired hook execution = %v, want %v", got, want)
	}
}

func TestRunPlacesVerboseHookReportsBesidePrivateMetricsState(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	stateDir := filepath.Join(t.TempDir(), "wb-state")
	metricsPath := filepath.Join(stateDir, "events.jsonl")
	reportPath := filepath.Join(t.TempDir(), "report-root")
	mustWrite(t, filepath.Join(configDir, "report-root.sh"), "#!/bin/sh\nprintf '%s\\n' \"$WB_HOOK_REPORT_ROOT\" > \"$WB_TEST_REPORT_ROOT\"\n")
	if err := os.Chmod(filepath.Join(configDir, "report-root.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nhooks:\n  pre-commit:\n    template: report-root.sh\nmetrics:\n  path: "+metricsPath+"\n")
	git(t, repo, "add", ".wb")
	git(t, repo, "commit", "-m", "configure hook report test")
	t.Setenv("WB_TEST_REPORT_ROOT", reportPath)

	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("hook result = %#v, error = %v", result, err)
	}
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := ResolveExecutionLayout(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	want := layout.ReportRoot
	if got := strings.TrimSpace(string(contents)); got != want {
		t.Fatalf("hook report root = %q, want %q", got, want)
	}
	if strings.Contains(string(contents), repo) {
		t.Fatalf("hook report root unexpectedly points into repository: %q", contents)
	}
	output, err := gitOutput(repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("inspect repository status: %v", err)
	}
	if output != "" {
		t.Fatalf("hook report setup dirtied the repository: %q", output)
	}
}

func TestRunProvidesPrivateRuntimeAndGoCachesOutsideRepository(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	home := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv(wbhome.EnvOverride, home)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	envPath := filepath.Join(t.TempDir(), "hook-env.txt")
	mustWrite(t, filepath.Join(configDir, "runtime-env.sh"), `#!/bin/sh
set -eu
printf '%s\n' "$WB_HOOK_RUNTIME_ROOT" > "$WB_TEST_ENV_PATH"
printf '%s\n' "$WB_HOOK_CACHE_ROOT" >> "$WB_TEST_ENV_PATH"
printf '%s\n' "$WB_HOOK_REPORT_ROOT" >> "$WB_TEST_ENV_PATH"
printf '%s\n' "$GOPATH" >> "$WB_TEST_ENV_PATH"
printf '%s\n' "$GOCACHE" >> "$WB_TEST_ENV_PATH"
printf '%s\n' "$GOMODCACHE" >> "$WB_TEST_ENV_PATH"
printf '%s\n' "$GOTMPDIR" >> "$WB_TEST_ENV_PATH"
printf '%s\n' "$XDG_CACHE_HOME" >> "$WB_TEST_ENV_PATH"
`)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nhooks:\n  pre-commit:\n    template: runtime-env.sh\nmetrics:\n  enabled: false\n")
	git(t, repo, "add", ".wb")
	git(t, repo, "commit", "-m", "configure runtime env hook")
	t.Setenv("WB_TEST_ENV_PATH", envPath)

	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("hook result = %#v, error = %v", result, err)
	}
	layout, err := ResolveExecutionLayout(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	lines := readLogLines(t, envPath)
	want := []string{
		layout.Root,
		layout.CacheRoot,
		layout.ReportRoot,
		layout.GoPath,
		layout.GoCache,
		layout.GoModCache,
		layout.GoTmpDir,
		layout.XDGCacheHome,
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("hook runtime environment = %v, want %v", lines, want)
	}
	for _, path := range want {
		if strings.HasPrefix(path, repo+string(filepath.Separator)) || path == repo {
			t.Fatalf("hook runtime path points into repository: %s", path)
		}
		if info, statErr := os.Stat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("hook runtime directory %s stat=%v err=%v", path, info, statErr)
		}
	}
	if output, statusErr := gitOutput(repo, "status", "--porcelain"); statusErr != nil || output != "" {
		t.Fatalf("runtime hook dirtied repository: status=%q err=%v", output, statusErr)
	}
}

func prepareNodeProfileTest(t *testing.T, lockfile, scripts string, tools ...string) (repo, toolLog string) {
	t.Helper()
	repo = initRepo(t)
	isolateConfig(t)
	declaredScripts := make([]string, 0, 2)
	for _, script := range strings.Split(scripts, ",") {
		if script != "" {
			declaredScripts = append(declaredScripts, `"`+script+`":"ignored"`)
		}
	}
	mustWrite(t, filepath.Join(repo, "package.json"), `{"scripts":{`+strings.Join(declaredScripts, ",")+`}}`+"\n")
	if lockfile != "" {
		mustWrite(t, filepath.Join(repo, lockfile), "test\n")
	}
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "noop.sh"), "#!/bin/sh\nexit 0\n")
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), `version: 1
hooks:
  pre-push:
    template: noop.sh
profiles:
  auto: true
metrics:
  enabled: false
`)

	toolLog = filepath.Join(t.TempDir(), "tools.log")
	toolDir := t.TempDir()
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitExecutable, filepath.Join(toolDir, "git")); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		content := "#!/bin/sh\nprintf '" + tool + ":%s\\n' \"$*\" >> \"$WB_TEST_LOG\"\n"
		if tool == "node" {
			content = `#!/bin/sh
script_name=
for arg in "$@"; do
    script_name="$arg"
done
printf 'node:%s\n' "$script_name" >> "$WB_TEST_LOG"
case ",${WB_TEST_NODE_SCRIPTS:-}," in
    *,"$script_name",*) exit 0 ;;
    *) exit 1 ;;
esac
`
		}
		mustWriteExecutable(t, filepath.Join(toolDir, tool), content)
	}
	t.Setenv("PATH", toolDir)
	t.Setenv("WB_TEST_LOG", toolLog)
	t.Setenv("WB_TEST_NODE_SCRIPTS", scripts)
	return repo, toolLog
}

func mustWriteExecutable(t *testing.T, path, content string) {
	t.Helper()
	mustWrite(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(content)), "\n")
}
