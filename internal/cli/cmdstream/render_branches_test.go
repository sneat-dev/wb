package cmdstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams" // A guard that fired is a usage exit with its code and sanctioned command.
	"github.com/spf13/cobra"
)

func cwDepsStreamMember(repository string, pullRequest int, pullRequestError string) streams.Member {
	return streams.Member{Repository: repository, Role: streams.RoleConsumer, Worktree: "/tmp/" + repository, Branch: "stream/cw", PullRequest: pullRequest, PullRequestError: pullRequestError}
}
func TestStreamFailureRendersRefusalsAndFailures(t *testing.T) {
	t.Parallel()
	refusal := &streams.Refusal{Code: streams.RefusalUsage, Message: "stream cw is taken", Sanctioned: []string{"wb stream status cw", "wb stream end cw"}}
	func() {
		err := streamFailure(testRuntime(), cwDepsNewOutCommand(&bytes.Buffer{}), "stream start", "text", refusal)
		if code := exitCodeOf(t, err); code != exitUsage {
			t.Errorf("text refusal exit = %d", code)
		}
	}()

	var buffer bytes.Buffer
	command := cwDepsNewOutCommand(&buffer)
	err := streamFailure(testRuntime(), command, "stream start", "json", refusal)
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
	err = streamFailure(testRuntime(), cwDepsNewOutCommand(&buffer), "stream start", "json",
		&streams.Refusal{Code: streams.RefusalUsage, Message: "no remedy"})
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("remedy-less refusal exit = %d", code)
	}
	if strings.Contains(buffer.String(), "sanctioned_command") {
		t.Errorf("remedy-less refusal invented a sanctioned command: %s", buffer.String())
	}

	// A plain failure is redacted and returns a normal findings error.
	buffer.Reset()
	err = streamFailure(testRuntime(), cwDepsNewOutCommand(&buffer), "stream status", "json", errors.New("git exploded"))
	if err == nil || exitCodeOfSafe(err) != exitFindings {
		t.Fatalf("failure error = %v", err)
	}
	if !strings.Contains(buffer.String(), outcomeFindings) || !strings.Contains(buffer.String(), "git exploded") {
		t.Errorf("failure envelope = %s", buffer.String())
	}
	// A failing stdout write is surfaced, never swallowed.
	if err := streamFailure(testRuntime(), cwDepsNewOutCommand(cwDepsFailingWriter{}), "stream status", "json", errors.New("boom")); err == nil ||
		!strings.Contains(err.Error(), "write refused") {
		t.Fatalf("failing writer = %v", err)
	}
}

func TestPublicationFailureSummaryTruncates(t *testing.T) {
	t.Parallel()
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

func TestStreamListOutputShapes(t *testing.T) {
	t.Parallel()
	var all []streams.Stream
	var unreadable []streams.Unreadable

	// An empty store says so rather than printing nothing.
	var out bytes.Buffer
	if err := streamListOutput(cwDepsNewOutCommand(&out), "text", all, unreadable); err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if !strings.Contains(out.String(), "no streams") {
		t.Errorf("empty list output = %q", out.String())
	}

	all = []streams.Stream{{Name: "cw-one", CreatedAt: time.Now().UTC(), Members: []streams.Member{cwDepsStreamMember("acme/app", 1, "")}}}
	out.Reset()
	if err := streamListOutput(cwDepsNewOutCommand(&out), "text", all, unreadable); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "cw-one") || !strings.Contains(out.String(), "acme/app") {
		t.Errorf("list output = %q", out.String())
	}

	// A stream directory that cannot be decoded is listed as unreadable, never
	// silently dropped.
	unreadable = []streams.Unreadable{{Name: "cw-broken", Reason: "invalid json"}}
	out.Reset()
	if err := streamListOutput(cwDepsNewOutCommand(&out), "text", all, unreadable); err != nil {
		t.Fatalf("list with unreadable: %v", err)
	}
	if !strings.Contains(out.String(), "cw-broken") || !strings.Contains(out.String(), "unreadable:") {
		t.Errorf("unreadable stream missing from list:\n%s", out.String())
	}
	// JSON reports both halves of the listing.
	out.Reset()
	if err := streamListOutput(cwDepsNewOutCommand(&out), "json", all, unreadable); err != nil {
		t.Fatalf("json list: %v", err)
	}
	var envelope streamEnvelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("json list envelope: %v\n%s", err, out.String())
	}
}

func TestStreamEndOutputShapes(t *testing.T) {
	t.Parallel()
	// A dry run says nothing changed and re-runs with --apply.
	result := streams.EndResult{Stream: "cw-stream", Applied: false, Members: []streams.EndMemberResult{
		{Repository: "acme/app", WorktreeRemoved: false, DraftAction: "kept", Detail: "dry run", DraftPullRequest: 3},
	}}
	var out bytes.Buffer
	if err := streamEndOutput(testRuntime(), cwDepsNewOutCommand(&out), "text", result); err != nil {
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
	err := streamEndOutput(testRuntime(), cwDepsNewOutCommand(&out), "text", result)
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
	if err := streamEndOutput(testRuntime(), cwDepsNewOutCommand(&out), "json", streams.EndResult{Stream: "cw-stream", Applied: true}); err != nil {
		t.Fatalf("json end: %v", err)
	}
	if !json.Valid(out.Bytes()) {
		t.Errorf("json end output = %s", out.String())
	}
	// A failed write is surfaced.
	if err := streamEndOutput(testRuntime(), cwDepsNewOutCommand(cwDepsFailingWriter{}), "json", streams.EndResult{}); err == nil {
		t.Error("a failed json write must be surfaced")
	}
}

func TestPrintStreamFindingsNamesTheStreamScope(t *testing.T) {
	t.Parallel()
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

func TestStreamStatusOutputRendersEveryGap(t *testing.T) {
	t.Parallel()
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
	err := streamStatusOutput(testRuntime(), cwDepsNewOutCommand(&out), "text", status)
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
	if err := streamStatusOutput(testRuntime(), cwDepsNewOutCommand(&out), "text", withoutTime); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("status exit = %v", err)
	}
	if !strings.Contains(out.String(), "failed at an unknown time") {
		t.Errorf("undated publication failure missing:\n%s", out.String())
	}

	// A clean status says every gap is empty and exits zero.
	out.Reset()
	clean := streams.Status{Stream: "cw-stream", Phase: streams.PhaseOpen, Branch: "stream/cw-stream",
		Members: []streams.MemberStatus{{Repository: "acme/app", PullRequest: 3}}}
	if err := streamStatusOutput(testRuntime(), cwDepsNewOutCommand(&out), "text", clean); err != nil {
		t.Fatalf("clean status: %v", err)
	}
	if strings.Count(out.String(), "none") != 3 {
		t.Errorf("clean status should say every gap is none:\n%s", out.String())
	}

	// JSON reports the same findings and exits findings for an unrecorded PR.
	out.Reset()
	err = streamStatusOutput(testRuntime(), cwDepsNewOutCommand(&out), "json", streams.Status{Stream: "cw-stream",
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
	if err := streamStatusOutput(testRuntime(), cwDepsNewOutCommand(&out), "json", clean); err != nil {
		t.Fatalf("clean json status: %v", err)
	}
	// A failed write is surfaced.
	if err := streamStatusOutput(testRuntime(), cwDepsNewOutCommand(cwDepsFailingWriter{}), "json", clean); err == nil {
		t.Error("a failed json write must be surfaced")
	}
}

func TestStreamStartOutputShapesAndFindings(t *testing.T) {
	t.Parallel()
	result := streams.StartResult{
		Stream: streams.Stream{Name: "cw-stream", Members: []streams.Member{
			cwDepsStreamMember("acme/library", 12, ""),
			cwDepsStreamMember("acme/app", 0, "draft PR creation was refused"),
		}},
		Reported:            []streams.PreflightFinding{{Repository: "acme/app", Check: "red-main", Status: streams.PreflightUnknown, Detail: "base is red"}},
		TransitiveOmissions: []string{"acme/site"},
	}
	var out bytes.Buffer
	err := streamStartOutput(testRuntime(), cwDepsNewOutCommand(&out), "stream start", "text", result)
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
	if err := streamStartOutput(testRuntime(), cwDepsNewOutCommand(&out), "stream start", "text", clean); err != nil {
		t.Fatalf("clean start: %v", err)
	}
	if strings.Contains(out.String(), "!") {
		t.Errorf("clean start rendered a finding marker:\n%s", out.String())
	}
	// JSON carries the result as evidence and still exits findings.
	out.Reset()
	err = streamStartOutput(testRuntime(), cwDepsNewOutCommand(&out), "stream start", "json", result)
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("json start exit = %v", err)
	}
	if !json.Valid(out.Bytes()) {
		t.Errorf("json start output = %s", out.String())
	}
	// A failed write is surfaced on both paths.
	if err := streamStartOutput(testRuntime(), cwDepsNewOutCommand(cwDepsFailingWriter{}), "stream start", "json", clean); err == nil {
		t.Error("a failed json write must be surfaced")
	}
	if err := streamStartOutput(testRuntime(), cwDepsNewOutCommand(cwDepsFailingWriter{}), "stream start", "text", clean); err == nil {
		t.Error("a failed text write must be surfaced")
	}
}

func TestStreamCommandRefusalsInProcess(t *testing.T) {
	t.Parallel()
	root := "/fixture"

	tests := map[string]struct {
		build func() *cobra.Command
		args  []string
		want  string
	}{
		"start needs two arguments": {func() *cobra.Command { return newStreamStartCmd(testRuntime(), testDependencies()) }, []string{"only-one"}, "requires at least 2 arg"},
		"start rejects a bad name":  {func() *cobra.Command { return newStreamStartCmd(testRuntime(), testDependencies()) }, []string{"bad/name", "acme/app"}, "stream name"},
		"join rejects a bad name":   {func() *cobra.Command { return newStreamJoinCmd(testRuntime(), testDependencies()) }, []string{"bad/name", "acme/app"}, "stream name"},
		"join rejects a bad role": {func() *cobra.Command { return newStreamJoinCmd(testRuntime(), testDependencies()) }, []string{"cw-stream", "acme/app", "--role", "nonsense"},
			"unsupported role"},
		"join needs a work log prompt": {func() *cobra.Command { return newStreamJoinCmd(testRuntime(), testDependencies()) }, []string{"cw-stream", "acme/app"}, "--model is required"},
		"join refuses an unknown format": {func() *cobra.Command { return newStreamJoinCmd(testRuntime(), testDependencies()) }, []string{"cw-stream", "acme/app", "--format", "toml"},
			"unsupported format"},
		"status refuses an unknown format": {func() *cobra.Command { return newStreamStatusCmd(testRuntime(), testDependencies()) }, []string{"--format", "toml"}, "unsupported format"},
		"status reports an unknown stream": {func() *cobra.Command { return newStreamStatusCmd(testRuntime(), testDependencies()) }, []string{"cw-absent"}, ""},
		"delete reports an unknown stream": {func() *cobra.Command { return newStreamDeleteCmd(testRuntime(), testDependencies()) }, []string{"cw-absent"}, ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newStreamStatusCmd(testRuntime(), testDependencies()) })
	if err != nil || !strings.Contains(stdout, "no streams") {
		t.Fatalf("empty status = %v\n%s", err, stdout)
	}
}
