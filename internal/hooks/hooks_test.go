package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadPolicyLayersGlobalAndRepositoryTemplates(t *testing.T) {
	repo := initRepo(t)
	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	globalDir := filepath.Join(configHome, "wb")
	mustMkdirAll(t, filepath.Join(globalDir, "templates"))
	mustWrite(t, filepath.Join(globalDir, "templates", "pre-push.sh"), "#!/bin/sh\necho global\n")
	mustWrite(t, filepath.Join(globalDir, "hooks.yaml"), `version: 1
hooks:
  pre-push:
    template: templates/pre-push.sh
metrics:
  enabled: false
`)

	repoConfigDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, filepath.Join(repoConfigDir, "templates"))
	mustWrite(t, filepath.Join(repoConfigDir, "templates", "pre-commit.sh"), "#!/bin/sh\necho repo\n")
	mustWrite(t, filepath.Join(repoConfigDir, "hooks.yaml"), `version: 1
hooks:
  pre-commit:
    template: templates/pre-commit.sh
  pre-push:
    disabled: true
metrics:
  enabled: true
  path: metrics/events.jsonl
  labels:
    developer: alex
    machine: laptop
`)

	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := policy.Hooks["pre-commit"].Template, filepath.Join(repoConfigDir, "templates", "pre-commit.sh"); got != want {
		t.Fatalf("pre-commit template = %q, want %q", got, want)
	}
	if !policy.Hooks["pre-push"].Disabled {
		t.Fatal("repository policy should disable global pre-push")
	}
	if !policy.Metrics.Enabled {
		t.Fatal("repository policy should re-enable metrics")
	}
	if got, want := policy.Metrics.Path, filepath.Join(repoConfigDir, "metrics", "events.jsonl"); got != want {
		t.Fatalf("metrics path = %q, want %q", got, want)
	}
	if policy.Metrics.Labels["developer"] != "alex" || policy.Metrics.Labels["machine"] != "laptop" {
		t.Fatalf("metrics labels = %#v", policy.Metrics.Labels)
	}
	if len(policy.ConfigPaths) != 2 {
		t.Fatalf("config paths = %v, want global + repository", policy.ConfigPaths)
	}
	if got, want := expectedHookNames(policy), []string{"post-commit", "pre-commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected hooks = %v, want %v", got, want)
	}
}

func TestLoadPolicyRejectsUnknownAndMissingTemplates(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	config := filepath.Join(t.TempDir(), "hooks.yaml")
	mustWrite(t, config, "version: 1\nunknown: true\n")
	if _, err := LoadPolicy(repo, config); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("unknown field error = %v", err)
	}
	mustWrite(t, config, "version: 1\nhooks:\n  pre-commit:\n    template: missing.sh\n")
	if _, err := LoadPolicy(repo, config); err == nil || !strings.Contains(err.Error(), "missing.sh") {
		t.Fatalf("missing template error = %v", err)
	}
}

func TestApplyCheckAndRepairManagedHooks(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := "/opt/wb test/bin/wb"
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Findings) != 0 {
		t.Fatalf("install findings = %#v", result.Report.Findings)
	}
	wantHooks := []string{"post-commit", "pre-commit", "pre-push"}
	if !reflect.DeepEqual(result.Report.Hooks, wantHooks) {
		t.Fatalf("installed hooks = %v, want %v", result.Report.Hooks, wantHooks)
	}
	configured := git(t, repo, "config", "--local", "--get", "core.hooksPath")
	if configured != result.Report.ManagedPath {
		t.Fatalf("core.hooksPath = %q, want %q", configured, result.Report.ManagedPath)
	}
	preCommit := filepath.Join(result.Report.ManagedPath, "pre-commit")
	data, err := os.ReadFile(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "exec '/opt/wb test/bin/wb' hooks run 'pre-commit' -- \"$@\"") {
		t.Fatalf("unexpected shim:\n%s", data)
	}
	info, _ := os.Stat(preCommit)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("pre-commit shim is not executable")
	}

	mustWrite(t, preCommit, "#!/bin/sh\n"+managedMarker+"\necho stale\n")
	stale := filepath.Join(result.Report.ManagedPath, "commit-msg")
	mustWrite(t, stale, "#!/bin/sh\n"+managedMarker+"\n")
	report, err := Check(repo, "", executable)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(report.Findings, "hook-stale") || !hasFinding(report.Findings, "hook-unexpected") {
		t.Fatalf("drift findings = %#v", report.Findings)
	}
	repaired, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired.Report.Findings) != 0 {
		t.Fatalf("repair findings = %#v", repaired.Report.Findings)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale managed hook still exists: %v", err)
	}
}

func TestApplyProtectsConflictingHooksAndForceBacksUp(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	legacy := filepath.Join(repo, ".git-hooks")
	mustMkdirAll(t, legacy)
	mustWrite(t, filepath.Join(legacy, "pre-commit"), "#!/bin/sh\necho legacy\n")
	git(t, repo, "config", "--local", "core.hooksPath", ".git-hooks")

	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: "wb"}); err == nil || !strings.Contains(err.Error(), "migrate those hooks") {
		t.Fatalf("conflict error = %v", err)
	}
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: "wb", Repair: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "config", "--local", "--get", "core.hooksPath"); got != result.Report.ManagedPath {
		t.Fatalf("forced core.hooksPath = %q, want %q", got, result.Report.ManagedPath)
	}
	if _, err := os.Stat(filepath.Join(legacy, "pre-commit")); err != nil {
		t.Fatalf("legacy hook should be preserved: %v", err)
	}

	unmanaged := filepath.Join(result.Report.ManagedPath, "pre-commit")
	mustWrite(t, unmanaged, "#!/bin/sh\necho unmanaged\n")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: "wb", Repair: true}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged hook") {
		t.Fatalf("unmanaged collision error = %v", err)
	}
	now := time.Date(2026, 7, 20, 12, 34, 56, 0, time.UTC)
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: "wb", Repair: true, Force: true, Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	backup := unmanaged + ".wb-backup-20260720T123456Z"
	if data, err := os.ReadFile(backup); err != nil || !strings.Contains(string(data), "echo unmanaged") {
		t.Fatalf("unmanaged hook backup = %q, error = %v", data, err)
	}
}

func TestRunCustomTemplatePassesContextAndRecordsEvents(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	git(t, repo, "remote", "add", "origin", "git@github.com:acme/widgets.git")
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, filepath.Join(configDir, "templates"))
	template := filepath.Join(configDir, "templates", "pre-commit.sh")
	mustWrite(t, template, `#!/bin/sh
printf '%s|%s|%s|%s\n' "$WB_HOOK" "$1" "$WB_REPO_SLUG" "$WB_BRANCH"
cat
`)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), `version: 1
hooks:
  pre-commit:
    template: templates/pre-commit.sh
metrics:
  enabled: true
  path: metrics/events.jsonl
  labels:
    developer: dev-17
    machine: laptop-a
`)
	var stdout bytes.Buffer
	result, err := Run(RunOptions{
		RepoPath: repo,
		Hook:     "pre-commit",
		Args:     []string{"hello"},
		Stdin:    strings.NewReader("payload\n"),
		Stdout:   &stdout,
		Stderr:   &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.MetricsError != nil {
		t.Fatalf("run result = %#v", result)
	}
	if got := stdout.String(); !strings.Contains(got, "pre-commit|hello|acme/widgets|main\npayload") {
		t.Fatalf("template output = %q", got)
	}
	events, err := ReadEvents(filepath.Join(configDir, "metrics", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "commit-check" || events[0].Outcome != "passed" || events[0].Repository != "acme/widgets" {
		t.Fatalf("events = %#v", events)
	}
	if events[0].OS == "" || events[0].Arch == "" || events[0].Labels["developer"] != "dev-17" || events[0].Labels["machine"] != "laptop-a" {
		t.Fatalf("event environment = %#v", events[0])
	}
}

func TestRunMetricsFailureNeverBlocksSuccessfulHook(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	blockedParent := filepath.Join(repo, "blocked")
	mustWrite(t, blockedParent, "not a directory\n")
	config := filepath.Join(repo, ".wb", "hooks.yaml")
	mustMkdirAll(t, filepath.Dir(config))
	mustWrite(t, config, "version: 1\nmetrics:\n  enabled: true\n  path: ../blocked/events.jsonl\n")

	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("successful hook was blocked: result = %#v, error = %v", result, err)
	}
	if result.MetricsError == nil {
		t.Fatal("expected a non-blocking metrics error")
	}
}

func TestRunFailurePreservesExitCodeAndRecordsFailure(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	template := filepath.Join(configDir, "fail.sh")
	mustWrite(t, template, "#!/bin/sh\nexit 7\n")
	metricsPath := filepath.Join(configDir, "events.jsonl")
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nhooks:\n  pre-push:\n    template: fail.sh\nmetrics:\n  enabled: true\n  path: events.jsonl\n")
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || result.ExitCode != 7 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	events, readErr := ReadEvents(metricsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 1 || events[0].Action != "push-attempt" || events[0].Outcome != "failed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestAppendReadAndSummarizeMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	zone := time.FixedZone("test", 2*60*60)
	now := time.Date(2026, 7, 20, 18, 0, 0, 0, zone)
	events := []Event{
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 19, 10, 0, 0, 0, zone), Repository: "acme/widgets", Action: "commit", Outcome: "passed", DurationMS: 10},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 10, 0, 0, 0, zone), Repository: "acme/widgets", Action: "push-attempt", Outcome: "passed", DurationMS: 30},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 11, 0, 0, 0, zone), Repository: "acme/widgets", Action: "commit-check", Outcome: "failed", DurationMS: 20},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 12, 0, 0, 0, zone), Repository: "other/repo", Action: "commit", Outcome: "passed", DurationMS: 999},
	}
	for _, event := range events {
		if err := AppendEvent(path, event); err != nil {
			t.Fatal(err)
		}
	}
	read, err := ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != len(events) {
		t.Fatalf("read %d events, want %d", len(read), len(events))
	}
	summary := Summarize(read, 3, "widgets", now)
	if summary.Commits != 1 || summary.PushAttempts != 1 || summary.CommitChecks != 1 || summary.HookFailures != 1 || summary.HookRuns != 3 || summary.AverageDurationMS != 20 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.Days) != 3 || summary.Days[1].Commits != 1 || summary.Days[2].PushAttempts != 1 {
		t.Fatalf("daily summary = %#v", summary.Days)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	var first Event
	if err := decoder.Decode(&first); err != nil || first.SchemaVersion != EventSchemaVersion {
		t.Fatalf("first event = %#v, error = %v", first, err)
	}
}

func TestReadEventsRejectsUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	mustWrite(t, path, `{"schema_version":99,"timestamp":"2026-07-20T00:00:00Z"}`+"\n")
	if _, err := ReadEvents(path); err == nil || !strings.Contains(err.Error(), "schema version 99") {
		t.Fatalf("schema error = %v", err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.name", "WB Tests")
	git(t, repo, "config", "user.email", "wb-tests@example.invalid")
	mustWrite(t, filepath.Join(repo, "README.md"), "test\n")
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
