package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func cwDepsStreamMember(repository string, pullRequest int, pullRequestError string) streams.Member {
	return streams.Member{Repository: repository, Role: streams.RoleConsumer, Worktree: "/tmp/" + repository,
		Branch: "stream/cw", PullRequest: pullRequest, PullRequestError: pullRequestError}
}

func TestCwDepsStreamFailureRendersRefusalsAndFailures(t *testing.T) {
	// A guard that fired is a usage exit with its code and sanctioned command.
	refusal := &streams.Refusal{Code: streams.RefusalUsage, Message: "stream cw is taken",
		Sanctioned: []string{"wb stream status cw", "wb stream end cw"}}
	out := cwCovCaptureStdout(t, func() {
		err := streamFailure(cwDepsNewOutCommand(&bytes.Buffer{}), "stream start", "text", refusal)
		if code := exitCodeOf(t, err); code != exitUsage {
			t.Errorf("text refusal exit = %d", code)
		}
	})
	_ = out

	var buffer bytes.Buffer
	command := cwDepsNewOutCommand(&buffer)
	err := streamFailure(command, "stream start", "json", refusal)
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("json refusal exit = %d", code)
	}
	var envelope streamEnvelope
	if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
		t.Fatalf("refusal envelope: %v\n%s", err, buffer.String())
	}
	if envelope.Outcome != outcomeRefused || envelope.RefusalCode != streams.RefusalUsage ||
		envelope.SanctionedCommand != "wb stream status cw" || len(envelope.SanctionedCommands) != 2 {
		t.Fatalf("refusal envelope = %+v", envelope)
	}

	// A refusal without a sanctioned command still renders the code.
	buffer.Reset()
	err = streamFailure(cwDepsNewOutCommand(&buffer), "stream start", "json",
		&streams.Refusal{Code: streams.RefusalUsage, Message: "no remedy"})
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("remedy-less refusal exit = %d", code)
	}
	if strings.Contains(buffer.String(), "sanctioned_command") {
		t.Errorf("remedy-less refusal invented a sanctioned command: %s", buffer.String())
	}

	// A plain failure is redacted and returns a normal findings error.
	buffer.Reset()
	err = streamFailure(cwDepsNewOutCommand(&buffer), "stream status", "json", errors.New("git exploded"))
	if err == nil || exitCodeOfSafe(err) != exitFindings {
		t.Fatalf("failure error = %v", err)
	}
	if !strings.Contains(buffer.String(), outcomeFindings) || !strings.Contains(buffer.String(), "git exploded") {
		t.Errorf("failure envelope = %s", buffer.String())
	}
	// A failing stdout write is surfaced, never swallowed.
	if err := streamFailure(cwDepsNewOutCommand(cwDepsFailingWriter{}), "stream status", "json", errors.New("boom")); err == nil ||
		!strings.Contains(err.Error(), "write refused") {
		t.Fatalf("failing writer = %v", err)
	}
}

// exitCodeOfSafe reports the WB exit code implied by an error, treating a
// plain error as findings.
func exitCodeOfSafe(err error) int {
	if err == nil {
		return exitOK
	}
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return exitFindings
}

func TestCwDepsPublicationFailureSummaryTruncates(t *testing.T) {
	if got := publicationFailureSummary("  first line\nsecond line  "); got != "first line" {
		t.Errorf("summary = %q", got)
	}
	long := strings.Repeat("x", 400)
	got := publicationFailureSummary(long)
	if len([]rune(got)) != 240 || !strings.HasSuffix(got, "...") {
		t.Errorf("long summary length = %d", len([]rune(got)))
	}
	if got := publicationFailureSummary(""); got != "" {
		t.Errorf("empty summary = %q", got)
	}
}

func TestCwDepsStreamListOutputShapes(t *testing.T) {
	store := streams.OpenAt(t.TempDir())
	engine := &streams.Engine{Store: store}

	// An empty store says so rather than printing nothing.
	var out bytes.Buffer
	if err := streamListOutput(cwDepsNewOutCommand(&out), "text", engine); err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if !strings.Contains(out.String(), "no streams") {
		t.Errorf("empty list output = %q", out.String())
	}

	created, err := store.Create(streams.Stream{Name: "cw-one", CreatedAt: time.Now().UTC(),
		Members: []streams.Member{cwDepsStreamMember("acme/app", 1, "")}})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	out.Reset()
	if err := streamListOutput(cwDepsNewOutCommand(&out), "text", engine); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "cw-one") || !strings.Contains(out.String(), "acme/app") {
		t.Errorf("list output = %q", out.String())
	}
	_ = created

	// A stream directory that cannot be decoded is listed as unreadable, never
	// silently dropped.
	brokenDir := filepath.Join(store.Root, "cw-broken")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "stream.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := streamListOutput(cwDepsNewOutCommand(&out), "text", engine); err != nil {
		t.Fatalf("list with unreadable: %v", err)
	}
	if !strings.Contains(out.String(), "cw-broken") || !strings.Contains(out.String(), "unreadable:") {
		t.Errorf("unreadable stream missing from list:\n%s", out.String())
	}
	// JSON reports both halves of the listing.
	out.Reset()
	if err := streamListOutput(cwDepsNewOutCommand(&out), "json", engine); err != nil {
		t.Fatalf("json list: %v", err)
	}
	var envelope streamEnvelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("json list envelope: %v\n%s", err, out.String())
	}
}

func TestCwDepsStreamEndOutputShapes(t *testing.T) {
	// A dry run says nothing changed and re-runs with --apply.
	result := streams.EndResult{Stream: "cw-stream", Applied: false, Members: []streams.EndMemberResult{
		{Repository: "acme/app", WorktreeRemoved: false, DraftAction: "kept", Detail: "dry run", DraftPullRequest: 3},
	}}
	var out bytes.Buffer
	if err := streamEndOutput(cwDepsNewOutCommand(&out), "text", result); err != nil {
		t.Fatalf("dry-run end: %v", err)
	}
	for _, want := range []string{"would end stream cw-stream", "acme/app", "nothing was changed; re-run with --apply"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run end output missing %q:\n%s", want, out.String())
		}
	}

	// An applied end lists members, agent PRs, and any failures, and exits
	// findings when the failures are non-empty.
	result = streams.EndResult{Stream: "cw-stream", Applied: true,
		Members:           []streams.EndMemberResult{{Repository: "acme/app", WorktreeRemoved: true, DraftAction: "closed", Detail: "retired"}},
		AgentPullRequests: []streams.AgentPullRequestOutcome{{Repository: "acme/app", Number: 7, Action: "retargeted", Detail: "base moved"}},
		Errors:            []string{"acme/site: worktree busy"},
	}
	out.Reset()
	err := streamEndOutput(cwDepsNewOutCommand(&out), "text", result)
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("failed end exit = %v", err)
	}
	for _, want := range []string{"ended stream cw-stream", "agent PR acme/app #7", "! acme/site: worktree busy"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("applied end output missing %q:\n%s", want, out.String())
		}
	}

	// JSON carries the same result as evidence.
	out.Reset()
	if err := streamEndOutput(cwDepsNewOutCommand(&out), "json", streams.EndResult{Stream: "cw-stream", Applied: true}); err != nil {
		t.Fatalf("json end: %v", err)
	}
	if !json.Valid(out.Bytes()) {
		t.Errorf("json end output = %s", out.String())
	}
	// A failed write is surfaced.
	if err := streamEndOutput(cwDepsNewOutCommand(cwDepsFailingWriter{}), "json", streams.EndResult{}); err == nil {
		t.Error("a failed json write must be surfaced")
	}
}

func TestCwDepsPrintStreamFindingsNamesTheStreamScope(t *testing.T) {
	var out bytes.Buffer
	if err := printStreamFindings(&out, []streams.PreflightFinding{
		{Repository: "acme/app", Check: "hooks-check", Status: streams.PreflightFail, Detail: "a shim is missing"},
		{Repository: "", Check: "red-main", Status: streams.PreflightUnknown, Detail: "the base is red"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"! acme/app hooks-check", "a shim is missing", "! (stream) red-main", "the base is red"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("findings output missing %q:\n%s", want, out.String())
		}
	}
	if err := printStreamFindings(cwDepsFailingWriter{}, []streams.PreflightFinding{{Repository: "acme/app"}}); err == nil {
		t.Error("a failed findings write must be surfaced")
	}
}

func TestCwDepsStreamStatusOutputRendersEveryGap(t *testing.T) {
	occurred := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	status := streams.Status{
		Stream: "cw-stream", Open: true, Phase: streams.PhaseOpen, Branch: "stream/cw-stream",
		Members: []streams.MemberStatus{
			{Repository: "acme/missing", Role: streams.RoleLibrary, Worktree: "/tmp/acme/missing", Unabsorbed: 2, LiveLinks: 1,
				LeaseHolder: "session-1", PullRequestMissing: "no draft pull request exists",
				LastPublicationError: &streams.PublicationFailure{Detail: "gh refused", OccurredAt: &occurred}},
			{Repository: "acme/blocked", Role: streams.RoleConsumer, Worktree: "/tmp/acme/blocked",
				PullRequestMissing: "no draft pull request exists", PullRequestBlocked: "the shared branch diverged"},
			{Repository: "acme/unrecorded", Role: streams.RoleConsumer, Worktree: "/tmp/acme/unrecorded",
				PullRequest: 42, PullRequestURL: "https://example.test/pr/42", PullRequestUnrecorded: true},
			{Repository: "acme/healthy", Role: streams.RoleConsumer, Worktree: "/tmp/acme/healthy", PullRequest: 9},
		},
		LinkedConsumers: []streams.LinkedConsumer{{Repository: "acme/app", Worktree: "/tmp/acme/app", Library: "acme/library",
			Mechanism: "go.work", Identity: "github.com/acme/library", PreviousVersion: "v1.0.0", ContentHash: "abc"}},
		MergedUntagged: &streams.MergedUntagged{Repository: "acme/library", Base: "main", LatestTag: "v1.0.0", Commits: []string{"aaa", "bbb"}},
		ConsumersBehind: []streams.ConsumerBehind{{Repository: "acme/app", Identity: "github.com/acme/library",
			Manifest: "go.mod", Declared: "v1.0.0", Published: "v1.1.0"}},
		OpenAgentPullRequests: []streams.AgentPullRequest{{Repository: "acme/app", Number: 5, Title: "fix", Head: "stream/cw-stream"}},
		Unknowns:              []string{"could not read the npm registry"},
	}
	var out bytes.Buffer
	err := streamStatusOutput(cwDepsNewOutCommand(&out), "text", status)
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("status with missing pull requests exit = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"stream cw-stream (open) on stream/cw-stream",
		"missing member pull requests:",
		"last publication attempt failed at 2026-09-14T12:00:00Z: gh refused",
		"blocked: the shared branch diverged",
		"open pull request #42 exists at https://example.test/pr/42 but is not recorded in stream state",
		"recover: wb stream join cw-stream acme/missing",
		"linked consumers (gap 1):",
		"acme/app → acme/library via go.work",
		"merged but untagged (gap 2):",
		"2 commit(s) on main after v1.0.0",
		"consumers behind (gap 3):",
		"open agent pull requests against the stream branch:",
		"could not establish:",
		"? could not read the npm registry",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status output missing %q:\n%s", want, text)
		}
	}

	// A publication failure with no recorded time still renders, naming the
	// unknown time rather than dropping the evidence.
	out.Reset()
	withoutTime := status
	withoutTime.Members = []streams.MemberStatus{{Repository: "acme/missing", Worktree: "/tmp/acme/missing",
		PullRequestMissing:   "no draft pull request exists",
		LastPublicationError: &streams.PublicationFailure{Detail: "gh refused"}}}
	if err := streamStatusOutput(cwDepsNewOutCommand(&out), "text", withoutTime); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("status exit = %v", err)
	}
	if !strings.Contains(out.String(), "failed at an unknown time") {
		t.Errorf("undated publication failure missing:\n%s", out.String())
	}

	// A clean status says every gap is empty and exits zero.
	out.Reset()
	clean := streams.Status{Stream: "cw-stream", Phase: streams.PhaseOpen, Branch: "stream/cw-stream",
		Members: []streams.MemberStatus{{Repository: "acme/app", PullRequest: 3}}}
	if err := streamStatusOutput(cwDepsNewOutCommand(&out), "text", clean); err != nil {
		t.Fatalf("clean status: %v", err)
	}
	if strings.Count(out.String(), "none") != 3 {
		t.Errorf("clean status should say every gap is none:\n%s", out.String())
	}

	// JSON reports the same findings and exits findings for an unrecorded PR.
	out.Reset()
	err = streamStatusOutput(cwDepsNewOutCommand(&out), "json", streams.Status{Stream: "cw-stream",
		Members: []streams.MemberStatus{{Repository: "acme/app", PullRequestUnrecorded: true}}})
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("json status exit = %v", err)
	}
	var envelope streamEnvelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("status envelope: %v\n%s", err, out.String())
	}
	if envelope.Outcome != outcomeFindings {
		t.Errorf("status envelope = %+v", envelope)
	}
	// A clean JSON status is a success with no error.
	out.Reset()
	if err := streamStatusOutput(cwDepsNewOutCommand(&out), "json", clean); err != nil {
		t.Fatalf("clean json status: %v", err)
	}
	// A failed write is surfaced.
	if err := streamStatusOutput(cwDepsNewOutCommand(cwDepsFailingWriter{}), "json", clean); err == nil {
		t.Error("a failed json write must be surfaced")
	}
}

func TestCwDepsStreamStartOutputShapesAndFindings(t *testing.T) {
	result := streams.StartResult{
		Stream: streams.Stream{Name: "cw-stream", Members: []streams.Member{
			cwDepsStreamMember("acme/library", 12, ""),
			cwDepsStreamMember("acme/app", 0, "draft PR creation was refused"),
		}},
		Reported:            []streams.PreflightFinding{{Repository: "acme/app", Check: "red-main", Status: streams.PreflightUnknown, Detail: "base is red"}},
		TransitiveOmissions: []string{"acme/site"},
	}
	var out bytes.Buffer
	err := streamStartOutput(cwDepsNewOutCommand(&out), "stream start", "text", result)
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("start with findings exit = %v", err)
	}
	for _, want := range []string{
		"stream cw-stream on stream/cw-stream",
		"#12",
		"no draft PR: draft PR creation was refused",
		"! acme/app red-main",
		"acme/site consumes a stream member but is not in the stream",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("start output missing %q:\n%s", want, out.String())
		}
	}

	// A clean start succeeds and says nothing about findings.
	out.Reset()
	clean := streams.StartResult{Stream: streams.Stream{Name: "cw-stream", Members: []streams.Member{
		cwDepsStreamMember("acme/app", 4, ""),
	}}}
	if err := streamStartOutput(cwDepsNewOutCommand(&out), "stream start", "text", clean); err != nil {
		t.Fatalf("clean start: %v", err)
	}
	if strings.Contains(out.String(), "!") {
		t.Errorf("clean start rendered a finding marker:\n%s", out.String())
	}
	// JSON carries the result as evidence and still exits findings.
	out.Reset()
	err = streamStartOutput(cwDepsNewOutCommand(&out), "stream start", "json", result)
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("json start exit = %v", err)
	}
	if !json.Valid(out.Bytes()) {
		t.Errorf("json start output = %s", out.String())
	}
	// A failed write is surfaced on both paths.
	if err := streamStartOutput(cwDepsNewOutCommand(cwDepsFailingWriter{}), "stream start", "json", clean); err == nil {
		t.Error("a failed json write must be surfaced")
	}
	if err := streamStartOutput(cwDepsNewOutCommand(cwDepsFailingWriter{}), "stream start", "text", clean); err == nil {
		t.Error("a failed text write must be surfaced")
	}
}

func TestCwDepsStreamWorkLogRefusals(t *testing.T) {
	command := cwDepsNewOutCommand(&bytes.Buffer{})
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	// An unsupported mode is named rather than defaulted.
	if _, _, err := streamWorkLog(command, "cw", workLogFlags{mode: "telepathy"}); err == nil ||
		!strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatalf("unsupported mode = %v", err)
	}
	// Manual mode requires an initiator so a non-agent mutation is auditable.
	if _, _, err := streamWorkLog(command, "cw", workLogFlags{mode: "manual"}); err == nil ||
		!strings.Contains(err.Error(), "requires --initiator") {
		t.Fatalf("manual without initiator = %v", err)
	}
	// Agent mode requires a live registered session.
	if _, _, err := streamWorkLog(command, "cw", workLogFlags{mode: "agent", agentID: "agent-1"}); err == nil ||
		!strings.Contains(err.Error(), "agent-mode stream creation requires a live registered session") {
		t.Fatalf("agent mode without a session = %v", err)
	}
	// Reading the prompt from stdin still enforces the Work Log requirements.
	stdinCommand := cwDepsNewOutCommand(&bytes.Buffer{})
	stdinCommand.SetIn(strings.NewReader("the exact task request\n"))
	prepared, agentMode, err := streamWorkLog(stdinCommand, "cw", workLogFlags{
		mode: "manual", initiator: "me@example.com", model: "unknown", originalPrompt: "-",
	})
	if err != nil {
		t.Fatalf("manual stdin prompt: %v", err)
	}
	if agentMode {
		t.Error("manual mode was reported as agent mode")
	}
	if prepared.OriginalPrompt == "" {
		t.Error("the archived prompt is empty")
	}
	// A handover with no prompt at all is refused: the archive is mandatory.
	if _, _, err := streamWorkLog(command, "cw", workLogFlags{mode: "manual", initiator: "me@example.com", model: "unknown"}); err == nil ||
		!strings.Contains(err.Error(), "prompt") {
		t.Fatalf("a stream Work Log without an archived prompt must be refused: %v", err)
	}
}

// TestCwDepsStreamCommandRefusalsInProcess drives the stream verbs' guard
// paths, which fail before any worktree, branch, or GitHub call is made.
func TestCwDepsStreamCommandRefusalsInProcess(t *testing.T) {
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	tests := map[string]struct {
		build func() *cobra.Command
		args  []string
		want  string
	}{
		"start needs two arguments": {newStreamStartCmd, []string{"only-one"}, "requires at least 2 arg"},
		"start rejects a bad name":  {newStreamStartCmd, []string{"bad/name", "acme/app"}, "stream name"},
		"join rejects a bad name":   {newStreamJoinCmd, []string{"bad/name", "acme/app"}, "stream name"},
		"join rejects a bad role": {newStreamJoinCmd, []string{"cw-stream", "acme/app", "--role", "nonsense"},
			"unsupported role"},
		"join needs a work log prompt": {newStreamJoinCmd, []string{"cw-stream", "acme/app"}, "--model is required"},
		"join refuses an unknown format": {newStreamJoinCmd, []string{"cw-stream", "acme/app", "--format", "toml"},
			"unsupported format"},
		"status refuses an unknown format": {newStreamStatusCmd, []string{"--format", "toml"}, "unsupported format"},
		"status reports an unknown stream": {newStreamStatusCmd, []string{"cw-absent"}, ""},
		"delete reports an unknown stream": {newStreamDeleteCmd, []string{"cw-absent"}, ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, _, err := cwCovExec(t, root, test.build, test.args...)
			if err == nil {
				t.Fatalf("%v was accepted:\n%s", test.args, stdout)
			}
			if test.want != "" && !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%v error = %v, want %q", test.args, err, test.want)
			}
		})
	}
	// With no name and an empty store, status lists nothing rather than
	// failing.
	stdout, _, err := cwCovExec(t, root, newStreamStatusCmd)
	if err != nil || !strings.Contains(stdout, "no streams") {
		t.Fatalf("empty status = %v\n%s", err, stdout)
	}
}
