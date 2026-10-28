package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestParseLanePinAcceptsABranchShaAndCommitComment(t *testing.T) {
	t.Parallel()
	pin, err := ParseLanePin("Acme/App", "feat/x@ABCDEF1=https://github.com/acme/app/commit/abcdef1234#commitcomment-42")
	if err != nil || pin.Branch != "feat/x" || pin.SHA != "abcdef1" || pin.ReviewCommentID != 42 {
		t.Fatalf("pin = %+v err = %v", pin, err)
	}
	for name, raw := range map[string]string{
		"no review":          "feat/x@abcdef1",
		"option branch":      "-x@abcdef1=https://github.com/acme/app/commit/abcdef1#commitcomment-1",
		"dotted branch":      "a..b@abcdef1=https://github.com/acme/app/commit/abcdef1#commitcomment-1",
		"issue comment":      "x@abcdef1=https://github.com/acme/app/pull/7#issuecomment-1",
		"other repository":   "x@abcdef1=https://github.com/acme/other/commit/abcdef1#commitcomment-1",
		"other commit":       "x@abcdef1=https://github.com/acme/app/commit/1234567#commitcomment-1",
		"overflowing id":     "x@abcdef1=https://github.com/acme/app/commit/abcdef1#commitcomment-99999999999999999999",
		"trailing slash":     "x/@abcdef1=https://github.com/acme/app/commit/abcdef1#commitcomment-1",
		"lock suffix branch": "x.lock@abcdef1=https://github.com/acme/app/commit/abcdef1#commitcomment-1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseLanePin("acme/app", raw); err == nil {
				t.Fatalf("%q parsed", raw)
			}
		})
	}
}

func TestIntegrationResultExitCodesAndText(t *testing.T) {
	t.Parallel()
	for outcome, want := range map[LandOutcome]int{LandSuccess: 0, LandRefused: 2, LandFindings: 1} {
		if got := (IntegrationResult{Outcome: outcome}).ExitCode(); got != want {
			t.Errorf("%s exit %d, want %d", outcome, got, want)
		}
	}
	raw, err := MarshalIntegrationResult(IntegrationResult{Verb: "integration merge", Integration: "public-demo"})
	if err != nil || !strings.Contains(string(raw), `"integration": "public-demo"`) {
		t.Fatalf("json = %s err = %v", raw, err)
	}
	text := IntegrationText(IntegrationResult{Outcome: LandRefused, Reason: "no", RefusalCode: "x", Command: "rerun",
		Refusals: []BatchIssue{{Code: "c", Source: "s", Reason: "r"}},
		Lanes:    []LaneMerge{{Branch: "b", SHA: "0123456789abcdef", State: LaneStateConflicted, ConflictPaths: []string{"a.go", "b.go"}}}})
	for _, want := range []string{"refused: no", "refusal: x", "c s: r", "b@0123456789ab conflicted conflicts: a.go, b.go", "next: rerun"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestClaimSessionAndDeclaredReviewer(t *testing.T) {
	t.Parallel()
	claims := []worktrees.ActiveClaimSummary{{Repository: "Acme/App", Branch: "lane", WBSessionID: "s1"}}
	if claimSessionIn(claims, "acme/app", "lane") != "s1" || claimSessionIn(claims, "acme/app", "other") != "" {
		t.Fatal("claim session lookup is wrong")
	}
	if claimSessionForBranch(t.TempDir(), "acme/app", "lane") != "" {
		t.Fatal("an empty projects root has a claim")
	}
	if got := declaredReviewer("**Review by opus@codex@s9**\nok"); got.Session != "s9" || !identityComplete(got) {
		t.Fatalf("reviewer = %+v", got)
	}
	if identityComplete(declaredReviewer("no header")) || identityComplete(ParseReviewerIdentity("opus@unknown@s")) {
		t.Fatal("an incomplete identity counted as complete")
	}
}

func TestIntegrationDefaultsResolveProductionPorts(t *testing.T) {
	t.Parallel()
	options := IntegrationOptions{Worktree: t.TempDir()}
	if options.resolveGitHub() == nil || options.checkoutGit().run == nil {
		t.Fatal("a default port is nil")
	}
	if _, err := options.resolveCheckout()(options.Worktree); err == nil {
		t.Fatal("a directory without a WB manifest was accepted")
	}
	if options.resolveLaneSession()(t.TempDir(), "acme/app", "lane") != "" {
		t.Fatal("a lane session came from nowhere")
	}
	_ = options.resolveOperator()
	// No Go module or Node project: the recipe has nothing to run.
	if report := options.resolveVerify()(context.Background(), "acme/app", options.Worktree, []quality.Check{quality.CheckLint}, 0); report.Status != quality.StatusSkipped {
		t.Fatalf("empty checkout status = %s", report.Status)
	}
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".wb", "quality.yaml"), []byte("nope: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if report := localChecks(context.Background(), "acme/app", broken, []quality.Check{quality.CheckLint}, 0); report.Status != quality.StatusFailed {
		t.Fatalf("malformed policy status = %s", report.Status)
	}
}

func TestGitHubIntegrationReadsACommitComment(t *testing.T) {
	t.Parallel()
	var asked []string
	adapter := githubIntegration{gh: func(_ context.Context, args ...string) ([]byte, error) {
		asked = args
		return []byte(`{"body":"Reviewed-Head: abc","commit_id":"abc"}`), nil
	}}
	comment, err := adapter.CommitComment(context.Background(), "acme/app", 42)
	if err != nil || comment.CommitID != "abc" || comment.Body != "Reviewed-Head: abc" || strings.Join(asked, " ") != "api repos/acme/app/comments/42" {
		t.Fatalf("comment = %+v err = %v args = %v", comment, err, asked)
	}
	broken := githubIntegration{gh: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("404") }}
	if _, err := broken.CommitComment(context.Background(), "acme/app", 1); err == nil {
		t.Fatal("a failed read was swallowed")
	}
	if DefaultIntegrationGitHub() == nil {
		t.Fatal("no production adapter")
	}
}

func TestIntegrationRefusesAnInvalidIntegrationBranch(t *testing.T) {
	t.Parallel()
	for _, verb := range []func(context.Context, IntegrationOptions) (IntegrationResult, error){AddToBatch, MergeIntegration} {
		result, err := verb(context.Background(), IntegrationOptions{Repository: "acme/app", Integration: "-x"})
		if err != nil || result.RefusalCode != BatchRefusalPinInvalid {
			t.Fatalf("result = %+v err = %v", result, err)
		}
	}
	result, err := AddToBatch(context.Background(), IntegrationOptions{Repository: "acme/app", Integration: "main", Pins: []string{"bad"}})
	if err != nil || result.RefusalCode != BatchRefusalPinInvalid {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestParseLaneTrailersNeedsEveryTrailer(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	good := "Merge x\n\nWb-Lane: feat/x@" + sha + "\nWb-Review: https://github.com/acme/app/commit/" + sha + "#commitcomment-7\n" +
		"Wb-Review-Digest: d1\nWb-Reviewer: opus@codex@r\nWb-Merged-By: opus@claude-code@m\n"
	record, ok := parseLaneTrailers(good)
	if !ok || record.Branch != "feat/x" || record.SHA != sha || record.CommentID != 7 || record.MergedBy.Session != "m" {
		t.Fatalf("record = %+v ok = %v", record, ok)
	}
	unknownModel := strings.Replace(good, "opus@claude-code@m", "unknown@claude-code@m", 1)
	if record, ok := parseLaneTrailers(unknownModel); !ok || record.MergedBy.Model != "unknown" {
		t.Fatalf("truthful unknown merger model was lost: record = %+v ok = %v", record, ok)
	}
	for name, message := range map[string]string{
		"no lane":           strings.Replace(good, "Wb-Lane: feat/x@"+sha, "Wb-Lane: feat/x", 1),
		"short sha":         strings.Replace(good, "@"+sha+"\n", "@abc\n", 1),
		"issue comment":     strings.Replace(good, "commit/"+sha+"#commitcomment-7", "pull/1#issuecomment-7", 1),
		"no digest":         strings.Replace(good, "Wb-Review-Digest: d1\n", "", 1),
		"incomplete merger": strings.Replace(good, "opus@claude-code@m", "opus@unknown@m", 1),
		"missing model":     strings.Replace(good, "opus@claude-code@m", "@claude-code@m", 1),
		"missing session":   strings.Replace(good, "opus@claude-code@m", "unknown@codex@", 1),
		"unknown session":   strings.Replace(good, "opus@claude-code@m", "unknown@codex@unknown", 1),
	} {
		if _, ok := parseLaneTrailers(message); ok {
			t.Errorf("%s: parsed", name)
		}
	}
}

// TestOperatorIdentityMustNameKnownHarnessAndSession pins the merger rule: an
// unknown model is recorded truthfully, but a guessed harness or session is
// refused before any merge exists.
func TestOperatorIdentityMustNameKnownHarnessAndSession(t *testing.T) {
	t.Parallel()
	for name, operator := range map[string]ReviewerIdentity{
		"missing model":   {Harness: "codex", Session: "operator"},
		"missing harness": {Model: "unknown", Session: "operator"},
		"unknown harness": {Model: "unknown", Harness: "unknown", Session: "operator"},
		"missing session": {Model: "unknown", Harness: "codex"},
		"unknown session": {Model: "opus", Harness: "codex", Session: "unknown"},
	} {
		options := IntegrationOptions{operator: func() ReviewerIdentity { return operator }}
		if issue := options.checkOperatorIdentity(); issue == nil || issue.Code != BatchRefusalOperatorIdentityIncomplete {
			t.Errorf("%s: issue = %+v", name, issue)
		}
	}
	for _, operator := range []ReviewerIdentity{
		{Model: "unknown", Harness: "codex", Session: "operator"},
		{Model: "opus", Harness: "claude-code", Session: "operator"},
	} {
		options := IntegrationOptions{operator: func() ReviewerIdentity { return operator }}
		if issue := options.checkOperatorIdentity(); issue != nil {
			t.Errorf("%s was refused: %+v", operator.String(), issue)
		}
	}
}

func TestGitHubPromoteAdapter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var asked [][]string
	reply := func(out string, err error) githubPromote {
		return githubPromote{githubIntegration{gh: func(_ context.Context, args ...string) ([]byte, error) {
			asked = append(asked, args)
			return []byte(out), err
		}}}
	}
	if branch, err := reply(`{"default_branch":"trunk"}`, nil).DefaultBranch(ctx, "acme/app"); err != nil || branch != "trunk" {
		t.Fatalf("branch = %q err = %v", branch, err)
	}
	if _, err := reply(`{}`, nil).DefaultBranch(ctx, "acme/app"); err == nil {
		t.Fatal("an empty default branch was accepted")
	}
	if number, body, err := reply(`[{"number":5,"body":"b"}]`, nil).OpenPullRequest(ctx, "acme/app", "public-demo", "main"); err != nil || number != 5 || body != "b" {
		t.Fatalf("open = %d %q %v", number, body, err)
	}
	if !strings.Contains(strings.Join(asked[len(asked)-1], " "), "head=acme:public-demo&base=main") {
		t.Fatalf("open asked %v", asked[len(asked)-1])
	}
	if number, _, err := reply(`[]`, nil).OpenPullRequest(ctx, "acme/app", "x", "main"); err != nil || number != 0 {
		t.Fatalf("none = %d %v", number, err)
	}
	if number, err := reply(`{"number":9}`, nil).CreatePullRequest(ctx, "acme/app", "x", "main", "t", "b"); err != nil || number != 9 {
		t.Fatalf("create = %d %v", number, err)
	}
	if err := reply(`{}`, nil).UpdatePullRequestBody(ctx, "acme/app", 9, "b"); err != nil || !strings.Contains(strings.Join(asked[len(asked)-1], " "), "PATCH repos/acme/app/pulls/9") {
		t.Fatalf("update err = %v asked %v", err, asked[len(asked)-1])
	}
	broken := reply("", errors.New("502"))
	if _, err := broken.DefaultBranch(ctx, "acme/app"); err == nil {
		t.Fatal("default branch error swallowed")
	}
	if _, _, err := broken.OpenPullRequest(ctx, "acme/app", "x", "main"); err == nil {
		t.Fatal("open error swallowed")
	}
	if _, err := broken.CreatePullRequest(ctx, "acme/app", "x", "main", "t", "b"); err == nil {
		t.Fatal("create error swallowed")
	}
	if err := broken.UpdatePullRequestBody(ctx, "acme/app", 9, "b"); err == nil {
		t.Fatal("update error swallowed")
	}
	if DefaultPromoteGitHub() == nil || (PromoteOptions{}).resolveGitHub() == nil {
		t.Fatal("no production promote adapter")
	}
	for outcome, want := range map[LandOutcome]int{LandSuccess: 0, LandRefused: 2, LandFindings: 1} {
		if got := (PromoteResult{Outcome: outcome}).ExitCode(); got != want {
			t.Errorf("%s exit %d", outcome, got)
		}
	}
}

func TestLocalChecksPreferExplicitThenRecipeThenDefault(t *testing.T) {
	t.Parallel()
	if !slices.Contains(defaultIntegrationChecks, quality.CheckTypecheck) || !slices.Contains(defaultIntegrationChecks, quality.CheckTest) {
		t.Fatalf("defaults = %v", defaultIntegrationChecks)
	}
	root := t.TempDir()
	for name, body := range map[string]string{"package.json": `{"scripts":{}}`, "nx.json": "{}", ".wb/quality.yaml": "version: 1\nintegration_checks: [typecheck]\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The recipe names only typecheck, which this workspace does not define:
	// one skipped entry, and no Nx target is started.
	report := localChecks(context.Background(), "acme/app", root, nil, 0)
	if len(report.Results) != 1 || report.Results[0].Check != quality.CheckTypecheck {
		t.Fatalf("recipe report = %+v", report)
	}
	if report := localChecks(context.Background(), "acme/app", root, []quality.Check{quality.CheckSpec}, 0); len(report.Results) != 1 || report.Results[0].Check != quality.CheckSpec {
		t.Fatalf("explicit report = %+v", report)
	}
	// No project and no recipe: the defaults have nothing to run.
	if report := localChecks(context.Background(), "acme/app", t.TempDir(), nil, 0); report.Status != quality.StatusSkipped {
		t.Fatalf("default report = %+v", report)
	}
}
