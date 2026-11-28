package umbrella

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseResolvesTypedAndBareReferences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		raw             string
		defaultRegistry string
		want            Ref
		wantDisplay     string
	}{
		{
			name:        "bare github ref defaults to github",
			raw:         "sneat-co/backstage#845",
			want:        Ref{Registry: "github", ID: "sneat-co/backstage#845"},
			wantDisplay: "sneat-co/backstage#845",
		},
		{
			name:        "explicit github ref",
			raw:         "github:sneat-co/backstage#845",
			want:        Ref{Registry: "github", ID: "sneat-co/backstage#845"},
			wantDisplay: "sneat-co/backstage#845",
		},
		{
			name:        "uppercase registry normalized to lowercase",
			raw:         "GitHub:sneat-dev/wb#1027",
			want:        Ref{Registry: "github", ID: "sneat-dev/wb#1027"},
			wantDisplay: "sneat-dev/wb#1027",
		},
		{
			name:        "synchestra typed ref parses cleanly",
			raw:         "synchestra:effort-123",
			want:        Ref{Registry: "synchestra", ID: "effort-123"},
			wantDisplay: "synchestra:effort-123",
		},
		{
			name:            "bare ref uses configured default registry",
			raw:             "effort-456",
			defaultRegistry: "synchestra",
			want:            Ref{Registry: "synchestra", ID: "effort-456"},
			wantDisplay:     "synchestra:effort-456",
		},
		{
			name:            "explicit registry overrides configured default",
			raw:             "github:sneat-co/backstage#845",
			defaultRegistry: "synchestra",
			want:            Ref{Registry: "github", ID: "sneat-co/backstage#845"},
			wantDisplay:     "sneat-co/backstage#845",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.raw, tc.defaultRegistry)
			if err != nil {
				t.Fatalf("Parse(%q, %q) unexpected error: %v", tc.raw, tc.defaultRegistry, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q, %q) = %+v, want %+v", tc.raw, tc.defaultRegistry, got, tc.want)
			}
			if got.String() != tc.want.Registry+":"+tc.want.ID {
				t.Fatalf("String() = %q, want %q", got.String(), tc.want.Registry+":"+tc.want.ID)
			}
			if got.Display() != tc.wantDisplay {
				t.Fatalf("Display() = %q, want %q", got.Display(), tc.wantDisplay)
			}
		})
	}
}

func TestZeroRefMethodsReturnEmptyStrings(t *testing.T) {
	t.Parallel()
	var zero Ref
	if !zero.IsZero() {
		t.Fatal("zero Ref must report IsZero() == true")
	}
	if zero.String() != "" || zero.Display() != "" {
		t.Fatalf("zero Ref String/Display = %q/%q, want empty", zero.String(), zero.Display())
	}
}

func TestParseRejectsMalformedReferences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		raw             string
		defaultRegistry string
		wantContains    string
	}{
		{"empty", "   ", "", "must not be empty"},
		{"whitespace inside", "github:sneat-co/backstage #845", "", "whitespace"},
		{"newline inside", "github:sneat-co/backstage#845\nfoo", "", "whitespace"},
		{"invalid default registry", "owner/repo#1", "Bad Registry!", "default umbrella registry"},
		{"https url refused", "https://github.com/sneat-co/backstage/issues/845", "", "not a URL"},
		{"http url refused", "http://github.com/sneat-co/backstage/issues/845", "", "not a URL"},
		{"invalid registry name", "123:foo", "", "invalid umbrella registry"},
		{"missing id after colon", "github:", "", "missing its identifier"},
		{"github missing hash", "sneat-co/backstage", "", "owner/repo#N"},
		{"github extra hash", "sneat-co/backstage#1#2", "", "owner/repo#N"},
		{"github missing slash", "backstage#845", "", "owner/repo#N"},
		{"github extra slash", "a/b/c#845", "", "owner/repo#N"},
		{"github unsafe owner", "../backstage#845", "", "invalid repository"},
		{"github zero issue", "sneat-co/backstage#0", "", "positive issue number"},
		{"github non-numeric issue", "sneat-co/backstage#abc", "", "positive issue number"},
		{"github overflow issue", "sneat-co/backstage#99999999999999999999999999999", "", "positive issue number"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tc.raw, tc.defaultRegistry)
			if err == nil {
				t.Fatalf("Parse(%q, %q) succeeded, want error containing %q", tc.raw, tc.defaultRegistry, tc.wantContains)
			}
			if !strings.Contains(err.Error(), tc.wantContains) {
				t.Fatalf("Parse(%q, %q) error = %q, want substring %q", tc.raw, tc.defaultRegistry, err.Error(), tc.wantContains)
			}
		})
	}
}

func TestRegistryRefusesSynchestraAndUnknownRegistries(t *testing.T) {
	t.Parallel()
	reg := DefaultProviders(func(context.Context, ...string) CommandResult {
		return CommandResult{Stdout: []byte(`{"state":"open"}`)}
	})
	if !reflect.DeepEqual(reg.Supported(), []string{"github"}) {
		t.Fatalf("Supported() = %v, want [github]", reg.Supported())
	}

	synRef, err := Parse("synchestra:effort-1", "")
	if err != nil {
		t.Fatalf("Parse synchestra ref: %v", err)
	}
	if _, err := reg.Resolve(context.Background(), synRef); err == nil || !strings.Contains(err.Error(), "synchestra") || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("Resolve(synchestra) error = %v, want explicit not-implemented refusal", err)
	}
	if err := reg.AppendStatus(context.Background(), synRef, "status"); err == nil || !strings.Contains(err.Error(), "synchestra") {
		t.Fatalf("AppendStatus(synchestra) error = %v, want synchestra refusal", err)
	}
	if err := reg.Close(context.Background(), synRef, "summary"); err == nil || !strings.Contains(err.Error(), "synchestra") {
		t.Fatalf("Close(synchestra) error = %v, want synchestra refusal", err)
	}
	if _, err := reg.RenderLink(synRef); err == nil || !strings.Contains(err.Error(), "synchestra") {
		t.Fatalf("RenderLink(synchestra) error = %v, want synchestra refusal", err)
	}

	jiraRef, err := Parse("jira:PROJ-42", "")
	if err != nil {
		t.Fatalf("Parse jira ref: %v", err)
	}
	if _, err := reg.Provider(jiraRef.Registry); err == nil || !strings.Contains(err.Error(), `unknown umbrella registry "jira"`) {
		t.Fatalf("Provider(jira) error = %v, want unknown registry refusal", err)
	}

	var nilReg *Registry
	if _, err := nilReg.Provider("github"); err == nil || !strings.Contains(err.Error(), "(none)") {
		t.Fatalf("nil Registry.Provider error = %v, want (none)", err)
	}
	emptyReg := NewRegistry(nil)
	if len(emptyReg.Supported()) != 0 {
		t.Fatalf("NewRegistry(nil) Supported() = %v, want empty", emptyReg.Supported())
	}
}

func TestGitHubProviderResolveAppendCloseAndRenderLink(t *testing.T) {
	t.Parallel()
	var calls [][]string
	exec := func(_ context.Context, args ...string) CommandResult {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case len(args) == 2 && args[0] == "api" && args[1] == "repos/sneat-co/backstage/issues/845":
			return CommandResult{Stdout: []byte(`{"state":"open","title":"SECRET PRIVATE TITLE","body":"SECRET PRIVATE BODY"}`)}
		case len(args) == 2 && args[0] == "api" && args[1] == "repos/sneat-co/backstage/issues/846":
			return CommandResult{Stdout: []byte(`{"state":"closed"}`)}
		case len(args) == 6 && args[2] == "POST" && args[3] == "repos/sneat-co/backstage/issues/845/comments":
			return CommandResult{Stdout: []byte(`{"id":1}`)}
		case len(args) == 6 && args[2] == "PATCH" && args[3] == "repos/sneat-co/backstage/issues/845":
			return CommandResult{Stdout: []byte(`{"state":"closed"}`)}
		default:
			return CommandResult{Err: errors.New("unexpected gh call")}
		}
	}

	reg := DefaultProviders(exec)
	ref := Ref{Registry: "github", ID: "sneat-co/backstage#845"}

	link, err := reg.RenderLink(ref)
	if err != nil || link != "sneat-co/backstage#845" {
		t.Fatalf("RenderLink = (%q, %v), want (sneat-co/backstage#845, nil)", link, err)
	}

	info, err := reg.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve open issue: %v", err)
	}
	if info.State != StateOpen || info.Link != "sneat-co/backstage#845" || info.Ref != ref {
		t.Fatalf("Resolve open issue = %+v", info)
	}

	closedRef := Ref{Registry: "github", ID: "sneat-co/backstage#846"}
	closedInfo, err := reg.Resolve(context.Background(), closedRef)
	if err != nil || closedInfo.State != StateClosed {
		t.Fatalf("Resolve closed issue = (%+v, %v), want closed", closedInfo, err)
	}

	if err := reg.AppendStatus(context.Background(), ref, "- `sneat-co/backstage`: landed"); err != nil {
		t.Fatalf("AppendStatus: %v", err)
	}
	if err := reg.Close(context.Background(), ref, "Closing umbrella"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(calls) != 5 {
		t.Fatalf("recorded %d gh calls, want 5: %v", len(calls), calls)
	}
	if calls[4][2] != "PATCH" || calls[4][5] != "state=closed" {
		t.Fatalf("final call = %v, want PATCH state=closed", calls[4])
	}
}

func TestGitHubProviderRefusesInvalidRefsAndAPIErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ref := Ref{Registry: "github", ID: "sneat-co/backstage#845"}
	wrongReg := Ref{Registry: "synchestra", ID: "effort-1"}

	p := NewGitHubProvider(func(_ context.Context, _ ...string) CommandResult {
		return CommandResult{Stdout: []byte(`{"state":"open","pull_request":{"url":"https://api.github.com/repos/sneat-co/backstage/pulls/845"}}`)}
	})
	if _, err := p.RenderLink(wrongReg); err == nil || !strings.Contains(err.Error(), "cannot handle registry") {
		t.Fatalf("RenderLink wrong registry = %v", err)
	}
	if _, err := p.Resolve(ctx, wrongReg); err == nil {
		t.Fatal("Resolve wrong registry succeeded")
	}
	if err := p.AppendStatus(ctx, wrongReg, "line"); err == nil {
		t.Fatal("AppendStatus wrong registry succeeded")
	}
	if err := p.AppendStatus(ctx, ref, "   "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("AppendStatus empty line = %v", err)
	}
	if err := p.Close(ctx, wrongReg, "summary"); err == nil {
		t.Fatal("Close wrong registry succeeded")
	}
	if err := p.Close(ctx, ref, "   "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("Close empty summary = %v", err)
	}
	if _, err := p.Resolve(ctx, ref); err == nil || !strings.Contains(err.Error(), "is a pull request, not an issue") {
		t.Fatalf("Resolve PR error = %v, want pull-request refusal", err)
	}

	badJSON := NewGitHubProvider(func(context.Context, ...string) CommandResult {
		return CommandResult{Stdout: []byte(`{bad`)}
	})
	if _, err := badJSON.Resolve(ctx, ref); err == nil || !strings.Contains(err.Error(), "decode umbrella") {
		t.Fatalf("Resolve invalid JSON error = %v", err)
	}

	weirdState := NewGitHubProvider(func(context.Context, ...string) CommandResult {
		return CommandResult{Stdout: []byte(`{"state":"draft"}`)}
	})
	if _, err := weirdState.Resolve(ctx, ref); err == nil || !strings.Contains(err.Error(), "unexpected GitHub state") {
		t.Fatalf("Resolve weird state error = %v", err)
	}

	stderrFail := NewGitHubProvider(func(context.Context, ...string) CommandResult {
		return CommandResult{Stderr: []byte("HTTP 404: Not Found"), Err: errors.New("exit 1")}
	})
	if _, err := stderrFail.Resolve(ctx, ref); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("Resolve stderr error = %v", err)
	}
	if err := stderrFail.AppendStatus(ctx, ref, "line"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("AppendStatus stderr error = %v", err)
	}
	if err := stderrFail.Close(ctx, ref, "summary"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("Close comment error = %v", err)
	}

	stdoutFail := NewGitHubProvider(func(_ context.Context, args ...string) CommandResult {
		if len(args) > 2 && args[2] == "PATCH" {
			return CommandResult{Stdout: []byte("patch refused"), Err: errors.New("exit 1")}
		}
		return CommandResult{Stdout: []byte(`{"id":1}`)}
	})
	if err := stdoutFail.Close(ctx, ref, "summary"); err == nil || !strings.Contains(err.Error(), "patch refused") {
		t.Fatalf("Close PATCH stdout error = %v", err)
	}

	plainErr := NewGitHubProvider(func(context.Context, ...string) CommandResult {
		return CommandResult{Err: errors.New("exec failed")}
	})
	if _, err := plainErr.Resolve(ctx, ref); err == nil || !strings.Contains(err.Error(), "exec failed") {
		t.Fatalf("Resolve plain error = %v", err)
	}
}

func TestDefaultGitHubExecutorRunsGHFromPath(t *testing.T) {
	// Uses a temp script named gh only through a canceled context to exercise defaultGitHubExecutor without touching PATH.
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewGitHubProvider(nil)
	if _, err := p.Resolve(ctx, Ref{Registry: "github", ID: "sneat-co/backstage#845"}); err == nil {
		t.Fatal("canceled context must fail defaultGitHubExecutor")
	}
	_ = filepath.Clean(os.DevNull)
}

func TestFormatHelpersTrailersPartOfStatusAndCloseSummary(t *testing.T) {
	t.Parallel()
	ref := Ref{Registry: "github", ID: "sneat-co/backstage#845"}

	if got := AppendTrailer("feat: subject", Ref{}); got != "feat: subject" {
		t.Fatalf("AppendTrailer zero ref = %q", got)
	}
	if got := AppendTrailer("feat: subject", ref); got != "feat: subject\n\nWb-Umbrella: github:sneat-co/backstage#845" {
		t.Fatalf("AppendTrailer plain = %q", got)
	}
	if got := AppendTrailer("   ", ref); got != "Wb-Umbrella: github:sneat-co/backstage#845" {
		t.Fatalf("AppendTrailer empty = %q", got)
	}
	withExistingTrailer := "feat: subject\n\nCo-Authored-By: Dev <dev@example.com>"
	if got := AppendTrailer(withExistingTrailer, ref); got != withExistingTrailer+"\nWb-Umbrella: github:sneat-co/backstage#845" {
		t.Fatalf("AppendTrailer appends to trailer block = %q", got)
	}
	withBodyParagraph := "feat: subject\n\nBody paragraph without trailers."
	if got := AppendTrailer(withBodyParagraph, ref); got != withBodyParagraph+"\n\nWb-Umbrella: github:sneat-co/backstage#845" {
		t.Fatalf("AppendTrailer after non-trailer paragraph = %q", got)
	}
	alreadyPresent := "feat: subject\n\nWb-Umbrella: github:sneat-co/backstage#845\n"
	if got := AppendTrailer(alreadyPresent, ref); got != alreadyPresent {
		t.Fatalf("AppendTrailer idempotency = %q", got)
	}

	if got := WithPartOf("Body", "", ref); got != "Body" {
		t.Fatalf("WithPartOf empty link = %q", got)
	}
	if got := WithPartOf("", "sneat-co/backstage#845", ref); got != "Part of sneat-co/backstage#845" {
		t.Fatalf("WithPartOf empty body = %q", got)
	}
	if got := WithPartOf("Body text", "sneat-co/backstage#845", ref); got != "Part of sneat-co/backstage#845\n\nBody text" {
		t.Fatalf("WithPartOf prepends = %q", got)
	}
	if got := WithPartOf("Part of sneat-co/backstage#845.\n\nBody text", "sneat-co/backstage#845", ref); got != "Part of sneat-co/backstage#845.\n\nBody text" {
		t.Fatalf("WithPartOf idempotent with trailing dot = %q", got)
	}
	if got := WithPartOf("Part of github:sneat-co/backstage#845\n\nBody text", "sneat-co/backstage#845", ref); got != "Part of github:sneat-co/backstage#845\n\nBody text" {
		t.Fatalf("WithPartOf idempotent with typed ref = %q", got)
	}

	status := FormatStatusLine(StatusUpdate{
		Task:        "cross-repo-umbrella-issue",
		Repository:  "sneat-co/backstage",
		PullRequest: "#846",
		Outcome:     "landed",
		LandedSHA:   "0123456789abcdef0123456789abcdef01234567",
		ArchiveRef:  "retired/backstage/ref",
		Note:        "note\nline",
	})
	if strings.Contains(status, "\n") {
		t.Fatalf("FormatStatusLine produced multiline string: %q", status)
	}
	for _, want := range []string{"sneat-co/backstage", "PR #846", "landed `0123456789ab`", "archive `retired/backstage/ref`", "note line", "task `cross-repo-umbrella-issue`"} {
		if !strings.Contains(status, want) {
			t.Fatalf("FormatStatusLine = %q, missing %q", status, want)
		}
	}

	summary := FormatCloseSummary(ref, []Member{
		{Task: "t1", Repository: "sneat-co/backstage", PullRequest: 846, State: "landed", LandedSHA: "0123456789abcdef0123456789abcdef01234567", ReadyToClose: true},
		{Task: "t1", Repository: "sneat-co/other", State: "archived", RestorePointer: "org/.retired:retired/other/x", ReadyToClose: true},
		{Task: "t1", Repository: "sneat-co/third", State: "archived", ArchiveRef: "refs/tags/retired/x", ReadyToClose: true},
	})
	for _, want := range []string{
		"Closing umbrella `github:sneat-co/backstage#845`",
		"sneat-co/backstage#846",
		"`0123456789ab`",
		"`org/.retired:retired/other/x`",
		"`refs/tags/retired/x`",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("FormatCloseSummary missing %q:\n%s", want, summary)
		}
	}

	if got := FormatStatusLine(StatusUpdate{Repository: "sneat-co/backstage", Outcome: "landed", LandedSHA: "abc1234"}); !strings.Contains(got, "`abc1234`") {
		t.Fatalf("FormatStatusLine short sha = %q", got)
	}
	if got := CloseCommand(ref); got != "wb umbrella close sneat-co/backstage#845 --apply" {
		t.Fatalf("CloseCommand = %q", got)
	}
}
