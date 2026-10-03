package hooks_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdhooks"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbexec"
	"github.com/spf13/cobra"
)

func newHooksCmd(inv *testInvocation) *cobra.Command {
	return cmdhooks.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: inv.projectsRoot, Filter: inv.filterFlag} }}, cmdhooks.GitOperations{Apply: hooks.Apply, Check: hooks.Check, LocalRepos: hooks.LocalRepos, Run: hooks.Run, Classify: hooks.ClassifyPendingPush, LoadPolicy: hooks.LoadPolicy, ReadEvents: hooks.ReadEvents, Executable: wbexec.HookExecutable, Now: time.Now, Exit: os.Exit}, cmdhooks.LifecycleOperations{}, cmdhooks.AgentOperations{})
}

type testInvocation struct{ projectsRoot, filterFlag string }

func selected(inv *testInvocation, path ...string) *cobra.Command {
	cmd := newHooksCmd(inv)
	for _, name := range path {
		for _, child := range cmd.Commands() {
			if child.Name() == name {
				cmd = child
				break
			}
		}
	}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if parent := cmd.Parent(); parent != nil {
		parent.RemoveCommand(cmd)
	}
	return cmd
}
func newHooksInstallCmd(inv *testInvocation, repair bool) *cobra.Command {
	verb := "install"
	if repair {
		verb = "repair"
	}
	return selected(inv, verb)
}
func newHooksCheckCmd(inv *testInvocation) *cobra.Command { return selected(inv, "check") }
func newHooksRunCmd(inv *testInvocation) *cobra.Command   { return selected(inv, "run") }

func run(args []string, out, errOut io.Writer) int {
	projects := ""
	clean := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--projects-root" {
			i++
			projects = args[i]
			continue
		}
		if args[i] == "--non-interactive" {
			continue
		}
		clean = append(clean, args[i])
	}
	cmd := newHooksCmd(&testInvocation{projectsRoot: projects})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	if len(clean) > 0 && clean[0] == "hooks" {
		clean = clean[1:]
	}
	cmd.SetArgs(clean)
	if cmd.Execute() != nil {
		return 1
	}
	return 0
}

const exitOK = 0

func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	testenv.Git(t, path, "init", "-b", "main")
	testenv.Git(t, path, "config", "user.email", "wb@example.test")
	testenv.Git(t, path, "config", "user.name", "WB Test")
	testenv.Git(t, path, "commit", "--allow-empty", "-m", "init")
	return path
}

func cwCovExec(t *testing.T, projects string, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	cmd := build()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}
func applyHooksFleet(inv *testInvocation, cmd *cobra.Command, config string, repair, force bool) error {
	op := newHooksInstallCmd(inv, repair)
	op.SetOut(cmd.OutOrStdout())
	op.SetErr(cmd.ErrOrStderr())
	args := []string{"--fleet", "--config", config}
	if force {
		args = append(args, "--force")
	}
	op.SetArgs(args)
	return op.Execute()
}
func checkHooksFleet(inv *testInvocation, cmd *cobra.Command, config string, jsonOut bool) error {
	op := newHooksCheckCmd(inv)
	op.SetOut(cmd.OutOrStdout())
	op.SetErr(cmd.ErrOrStderr())
	args := []string{"--fleet", "--config", config}
	if jsonOut {
		args = append(args, "--json")
	}
	op.SetArgs(args)
	return op.Execute()
}

func TestLocalHookReposFiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"z-org/beta/.git",
		"a-org/alpha/.git",
		"a-org/ignored",
	} {
		if err := os.MkdirAll(filepath.Join(root, relative), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	repos, err := hooks.LocalRepos(root, "a-org/")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Slug() != "a-org/alpha" {
		t.Fatalf("hooks.LocalRepos() = %#v, want only a-org/alpha", repos)
	}

	repos, err = hooks.LocalRepos(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Slug() != "a-org/alpha" || repos[1].Slug() != "z-org/beta" {
		t.Fatalf("hooks.LocalRepos() = %#v, want sorted repositories", repos)
	}
}
func TestApplyAndCheckHooksFleet(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{"acme/alpha", "acme/beta"} {
		repo := filepath.Join(root, relative)
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "init", "-b", "main")
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", repo, err, output)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	inv := &testInvocation{projectsRoot: root, filterFlag: "acme/"}

	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	if err := applyHooksFleet(inv, command, "", false, false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "acme/alpha") || !strings.Contains(got, "acme/beta") || !strings.Contains(got, "Processed 2 repositories; 0 failed") {
		t.Fatalf("fleet install output:\n%s", got)
	}

	output.Reset()
	if err := checkHooksFleet(inv, command, "", false); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Checked 2 repositories; 0 problems") {
		t.Fatalf("fleet check output:\n%s", got)
	}
}
func cgxc2BrokenHookRepoFixture(t *testing.T) (projectsRoot string) {
	t.Helper()
	projectsRoot = t.TempDir()
	repoPath := filepath.Join(projectsRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return projectsRoot
}
func cgxc2TestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "cgxc2"}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	return cmd
}
func TestApplyHooksFleetCountsAndReportsFailures(t *testing.T) {
	t.Parallel()
	projectsRoot := cgxc2BrokenHookRepoFixture(t)
	inv := &testInvocation{projectsRoot: projectsRoot}
	cmd := cgxc2TestCommand()
	err := applyHooksFleet(inv, cmd, "", false, false)
	if err == nil {
		t.Fatalf("expected a failures-summary error against a broken repo, got nil")
	}
	if !strings.Contains(err.Error(), "failed in") {
		t.Errorf("err = %v", err)
	}
}
func TestCheckHooksFleetCountsAndReportsProblems(t *testing.T) {
	t.Parallel()
	projectsRoot := cgxc2BrokenHookRepoFixture(t)
	inv := &testInvocation{projectsRoot: projectsRoot}
	cmd := cgxc2TestCommand()
	err := checkHooksFleet(inv, cmd, "", false)
	if err == nil {
		t.Fatalf("expected a *cmdhooks.CheckError against a broken repo, got nil")
	}
	if _, ok := err.(*cmdhooks.CheckError); !ok {
		t.Fatalf("err = %v (%T), want *cmdhooks.CheckError", err, err)
	}
}
func TestCwDepsHooksInstallAndCheckInProcess(t *testing.T) {
	root := t.TempDir()
	app := initTestRepository(t, filepath.Join(root, "acme", "app"))
	initTestRepository(t, filepath.Join(root, "acme", "other"))

	// check before install: unmanaged hooks are findings, not a crash.
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksCheckCmd(&testInvocation{projectsRoot: root}) }, app)
	if _, ok := err.(*cmdhooks.CheckError); !ok {
		t.Fatalf("check before install error = %v, want a hooks-check error\n%s", err, stdout)
	}
	// --format=json still exits non-zero for findings; the envelope is what
	// matters, so the error is expected here.
	jsonOut, _, jsonErr := cwCovExec(t, root, func() *cobra.Command { return newHooksCheckCmd(&testInvocation{projectsRoot: root}) }, app, "--format=json")
	if _, ok := jsonErr.(*cmdhooks.CheckError); !ok {
		t.Fatalf("check --format=json error = %v\n%s", jsonErr, jsonOut)
	}
	if !json.Valid([]byte(jsonOut)) {
		t.Fatalf("check --format=json wrote no JSON envelope: %s", jsonOut)
	}
	var checked hooks.CheckReport
	if err := json.Unmarshal([]byte(jsonOut), &checked); err != nil {
		t.Fatalf("check JSON: %v\n%s", err, jsonOut)
	}
	if len(checked.Findings) == 0 {
		t.Error("an unmanaged repository must report at least one finding")
	}

	// install: the shims land and the report names the repository.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(&testInvocation{projectsRoot: root}, false) }, app)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "hooks ready for") {
		t.Errorf("install report = %q", stdout)
	}
	if checked.ManagedPath == "" {
		t.Fatal("the check report named no managed hooks path")
	}
	if _, statErr := os.Stat(filepath.Join(checked.ManagedPath, "pre-commit")); statErr != nil {
		t.Errorf("install did not write the managed pre-commit shim under %s: %v", checked.ManagedPath, statErr)
	}
	// The same repository now validates clean.
	if stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksCheckCmd(&testInvocation{projectsRoot: root}) }, app); err != nil {
		t.Fatalf("check after install: %v\n%s", err, stdout)
	}

	// A repository path with --fleet is refused rather than silently ignored.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(&testInvocation{projectsRoot: root}, false) }, app, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("install --fleet with a path = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksCheckCmd(&testInvocation{projectsRoot: root}) }, app, "--fleet"); err == nil ||
		!strings.Contains(err.Error(), "cannot be used with --fleet") {
		t.Fatalf("check --fleet with a path = %v", err)
	}

	// Fleet install processes every local repository and reports the count.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(&testInvocation{projectsRoot: root}, false) }, "--fleet")
	if err != nil {
		t.Fatalf("fleet install: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "Processed 2 repositories; 0 failed") {
		t.Errorf("fleet install report = %q", stdout)
	}
	// Fleet check is then clean, in text and JSON.
	if stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksCheckCmd(&testInvocation{projectsRoot: root}) }, "--fleet"); err != nil {
		t.Fatalf("fleet check: %v\n%s", err, stdout)
	}
	fleetJSON, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksCheckCmd(&testInvocation{projectsRoot: root}) }, "--fleet", "--format=json")
	if err != nil || !json.Valid([]byte(fleetJSON)) {
		t.Fatalf("fleet check JSON = %v\n%s", err, fleetJSON)
	}
	var fleetResults []struct {
		Repository string             `json:"repository"`
		Report     *hooks.CheckReport `json:"report"`
		Error      string             `json:"error"`
	}
	if err := json.Unmarshal([]byte(fleetJSON), &fleetResults); err != nil || len(fleetResults) != 2 {
		t.Fatalf("fleet results = %+v, %v", fleetResults, err)
	}
	// The repair spelling reuses the same installer.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newHooksInstallCmd(&testInvocation{projectsRoot: root}, true) }, app)
	if err != nil || !strings.Contains(stdout, "hooks ready for") {
		t.Fatalf("repair: %v\n%s", err, stdout)
	}

	// A hidden hook runner refuses a malformed hook name outright and a
	// well-formed but unconfigured one without executing anything.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksRunCmd(&testInvocation{projectsRoot: root}) }, "../evil"); err == nil ||
		!strings.Contains(err.Error(), "invalid hook name") {
		t.Fatalf("malformed hook = %v", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newHooksRunCmd(&testInvocation{projectsRoot: root}) }, "not-a-real-hook"); err == nil ||
		!strings.Contains(err.Error(), "disabled or not configured") {
		t.Fatalf("unconfigured hook = %v", err)
	}
}
func TestCwDepsHooksMetricsAndMeasureCommandsInProcess(t *testing.T) {
	root := t.TempDir()
	app := initTestRepository(t, filepath.Join(root, "acme", "app"))
	metricsFile := filepath.Join(t.TempDir(), "metrics.jsonl")
	now := time.Now().UTC()
	events := []hooks.Event{
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now.Add(-2 * time.Hour), Repository: "acme/app",
			Hook: "post-commit", Action: "commit", Outcome: "passed", DurationMS: 120, OS: "linux", Arch: "amd64"},
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now.Add(-1 * time.Hour), Repository: "acme/app",
			Hook: "pre-push", Action: "push-attempt", Outcome: "failed", DurationMS: 900, Ref: "refs/heads/main", OS: "linux", Arch: "amd64"},
	}
	var lines bytes.Buffer
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	if err := os.WriteFile(metricsFile, lines.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", metricsFile, "--days", "30", "--repo", "acme")
	if err != nil {
		t.Fatalf("hooks metrics: %v\n%s", err, stdout)
	}
	for _, want := range []string{"Local hook metrics", "Totals:", "Events:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("metrics output missing %q:\n%s", want, stdout)
		}
	}
	jsonOut, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", metricsFile, "--format=json")
	if err != nil || !json.Valid([]byte(jsonOut)) {
		t.Fatalf("metrics JSON = %v\n%s", err, jsonOut)
	}
	stdout, _, err = cwCovExec(t, root, newHooksMeasureCmd, app, "--file", metricsFile, "--days", "30", "--repo", "acme")
	if err != nil {
		t.Fatalf("hooks measure: %v\n%s", err, stdout)
	}
	for _, want := range []string{"Hook profile cost", "commit", "stream push", "other push", "source:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("measure output missing %q:\n%s", want, stdout)
		}
	}
	measureJSON, _, err := cwCovExec(t, root, newHooksMeasureCmd, app, "--file", metricsFile, "--format=json")
	if err != nil || !json.Valid([]byte(measureJSON)) {
		t.Fatalf("measure JSON = %v\n%s", err, measureJSON)
	}
	// A missing events file is an empty window, not a failure.
	absent := filepath.Join(t.TempDir(), "absent.jsonl")
	if stdout, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", absent); err != nil {
		t.Fatalf("metrics over an absent file: %v\n%s", err, stdout)
	}
	if stdout, _, err := cwCovExec(t, root, newHooksMeasureCmd, app, "--file", absent); err != nil {
		t.Fatalf("measure over an absent file: %v\n%s", err, stdout)
	}
	// A malformed line is refused rather than silently ignored.
	broken := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(broken, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cwCovExec(t, root, newHooksMetricsCmd, app, "--file", broken); err == nil {
		t.Fatal("a malformed metrics line must be refused")
	}
}
func writeHookEvents(t *testing.T, path string, events []hooks.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := hooks.AppendEvents(path, events); err != nil {
		t.Fatal(err)
	}
}
func TestHooksMeasureShowsTheStreamProfileDelta(t *testing.T) {
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeHookEvents(t, path, []hooks.Event{
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now, Repository: "acme/app",
			Hook: "pre-commit", Action: "commit-check", Outcome: "passed", DurationMS: 800, Branch: "stream/x"},
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now, Repository: "acme/app",
			Hook: "pre-push", Action: "push-attempt", Outcome: "passed", DurationMS: 20, Branch: "stream/x"},
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now, Repository: "acme/app",
			Hook: "pre-push", Action: "push-attempt", Outcome: "passed", DurationMS: 60000, Branch: "feature/y"},
	})

	outputs := make(map[string][]byte)
	var delta hooks.ProfileDelta
	for name, flag := range map[string][]string{
		"canonical": {"--format=json"},
		"shortcut":  {"--json"},
	} {
		var stdout, stderr bytes.Buffer
		args := []string{"hooks", "measure", ".", "--file", path, "--non-interactive"}
		args = append(args, flag...)
		if code := run(args, &stdout, &stderr); code != exitOK {
			t.Fatalf("%s exit code = %d; stderr=%s", name, code, stderr.String())
		}
		outputs[name] = stdout.Bytes()
		if err := json.Unmarshal(stdout.Bytes(), &delta); err != nil {
			t.Fatalf("parse %s output %q: %v", name, stdout.String(), err)
		}
	}
	if !bytes.Equal(outputs["canonical"], outputs["shortcut"]) {
		t.Errorf("--format=json output differs from --json\ncanonical: %s\nshortcut: %s", outputs["canonical"], outputs["shortcut"])
	}
	if delta.Commit.Runs != 1 || delta.Commit.MaxDurationMS != 800 {
		t.Errorf("commit = %#v", delta.Commit)
	}
	if delta.StreamPush.Runs != 1 || delta.OtherPush.Runs != 1 {
		t.Fatalf("stream=%#v other=%#v", delta.StreamPush, delta.OtherPush)
	}
	if delta.SavedRuns != 1 || delta.SavedBasisMS != 60000 || delta.SavedDurationMS != 60000 {
		t.Errorf("saving = %d × %d = %d", delta.SavedRuns, delta.SavedBasisMS, delta.SavedDurationMS)
	}

	var textOut, textErr bytes.Buffer
	if code := run([]string{"hooks", "measure", ".", "--file", path, "--non-interactive"}, &textOut, &textErr); code != exitOK {
		t.Fatalf("text exit code = %d; stderr=%s", code, textErr.String())
	}
	for _, want := range []string{"stream push", "other push", "budget ms", "ran no local verification"} {
		if !strings.Contains(textOut.String(), want) {
			t.Errorf("text report does not contain %q:\n%s", want, textOut.String())
		}
	}
}
func TestHooksPushTierExitsSkipForAStreamBranch(t *testing.T) {
	classification := hooks.ClassifyPushTier([]hooks.RefUpdate{{
		LocalRef: "refs/heads/stream/x", LocalSHA: "a",
		RemoteRef: "refs/heads/stream/x", RemoteSHA: "b",
	}}, "main", nil)
	if classification.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0 so the hook template skips both blocks", classification.ExitCode())
	}
}

// TestPushTierDecisionReportsTheTierForAPublicationPush covers the branch whose
// exit code is the answer a Git hook acts on.
func TestPushTierDecisionReportsTheTierForAPublicationPush(t *testing.T) {
	stdin := strings.NewReader(
		"refs/tags/v1.0.0 1111111111111111111111111111111111111111 refs/tags/v1.0.0 2222222222222222222222222222222222222222\n")
	code, message := classifyDecision(stdin)
	if code != int(hooks.TierPublication) {
		t.Fatalf("code = %d, want %d", code, int(hooks.TierPublication))
	}
	want := "WB hook: tier 2 — refs/tags/v1.0.0 is a tag: publication push\n"
	if message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
}

// TestPushTierDecisionDefaultsToTheFastLaneOnMalformedInput covers the branch
// that must never block a push: an unparseable ref list degrades to tier 1
// instead of failing closed.
func TestPushTierDecisionDefaultsToTheFastLaneOnMalformedInput(t *testing.T) {
	code, message := classifyDecision(strings.NewReader("not-a-ref-update\n"))
	if code != int(hooks.TierLint) {
		t.Fatalf("code = %d, want %d", code, int(hooks.TierLint))
	}
	want := "WB hook: tier 1 — classification failed " +
		"(malformed pushed-ref line \"not-a-ref-update\": want 4 fields, got 1); " +
		"defaulting to the fast lane, CI is the real gate\n"
	if message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
}

// failingWriter always fails, so a test can observe whether a write error is
// propagated rather than swallowed.
func classifyDecision(stdin io.Reader) (int, string) {
	code := 0
	cmd := cmdhooks.New(shared.Runtime{}, cmdhooks.GitOperations{Classify: hooks.ClassifyPendingPush, Exit: func(value int) { code = value }}, cmdhooks.LifecycleOperations{}, cmdhooks.AgentOperations{})
	cmd.SetArgs([]string{"push-tier"})
	cmd.SetIn(stdin)
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.Execute(); err != nil {
		panic(err)
	}
	return code, output.String()
}
func newHooksMetricsCmd() *cobra.Command { return selected(&testInvocation{}, "metrics") }
func newHooksMeasureCmd() *cobra.Command { return selected(&testInvocation{}, "measure") }
