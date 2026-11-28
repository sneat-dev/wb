package cmdumbrella

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/umbrella"
)

type testExitError struct {
	code    int
	message string
}

func (e *testExitError) Error() string { return e.message }

func testRuntime(projectsRoot string) shared.Runtime {
	return shared.Runtime{
		Flags: func() shared.Flags {
			return shared.Flags{ProjectsRoot: projectsRoot}
		},
		ExitError: func(code int, message string) error {
			return &testExitError{code: code, message: message}
		},
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type nthFailWriter struct {
	failAt int
	calls  int
}

func (w *nthFailWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func fakeGitHubProviders(t *testing.T, state string, closeErr error, calls *[][]string) *umbrella.Registry {
	t.Helper()
	return umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		if calls != nil {
			*calls = append(*calls, append([]string(nil), args...))
		}
		if len(args) == 2 && args[0] == "api" {
			return umbrella.CommandResult{Stdout: []byte(`{"state":"` + state + `"}`)}
		}
		if closeErr != nil {
			return umbrella.CommandResult{Stderr: []byte(closeErr.Error()), Err: closeErr}
		}
		return umbrella.CommandResult{Stdout: []byte(`{"state":"closed"}`)}
	})
}

func TestUmbrellaShowDisplaysStateMembersAndCloseOffer(t *testing.T) {
	t.Parallel()
	members := []umbrella.Member{
		{
			Task:           "cross-repo-umbrella-issue",
			Repository:     "sneat-co/backstage",
			PullRequest:    846,
			State:          "landed",
			LandedSHA:      "0123456789abcdef0123456789abcdef01234567",
			ReadyToClose:   true,
			PullRequestURL: "https://github.com/sneat-co/backstage/pull/846",
		},
		{
			Task:           "cross-repo-umbrella-issue",
			Repository:     "trakhimenok/workbench",
			State:          "archived",
			RestorePointer: "trakhimenok/.retired:retired/workbench/x",
			ReadyToClose:   true,
		},
		{
			Task:         "cross-repo-umbrella-issue",
			Repository:   "sneat-dev/wb",
			State:        "archived",
			ArchiveRef:   "refs/tags/retired/wb",
			ReadyToClose: true,
		},
	}

	var out bytes.Buffer
	cmd := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members: func(projectsRoot string, ref umbrella.Ref) ([]umbrella.Member, error) {
			if projectsRoot != "/projects" || ref.String() != "github:sneat-co/backstage#845" {
				t.Fatalf("Members(%q, %q)", projectsRoot, ref.String())
			}
			return members, nil
		},
	})
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("umbrella show text: %v", err)
	}
	for _, want := range []string{
		"umbrella: github:sneat-co/backstage#845 (sneat-co/backstage#845)",
		"state:    open",
		"members:  3",
		"0123456789ab",
		"trakhimenok/.retired:retired/workbench/x",
		"refs/tags/retired/wb",
		"next: wb umbrella close sneat-co/backstage#845 --apply",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("show output missing %q:\n%s", want, out.String())
		}
	}

	// JSON output
	out.Reset()
	cmdJSON := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return members, nil },
	})
	cmdJSON.SetOut(&out)
	cmdJSON.SetArgs([]string{"show", "github:sneat-co/backstage#845", "--format", "json"})
	if err := cmdJSON.Execute(); err != nil {
		t.Fatalf("umbrella show json: %v", err)
	}
	var doc ShowDocument
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decode show json: %v", err)
	}
	if !doc.ReadyToClose || doc.CloseCommand != "wb umbrella close sneat-co/backstage#845 --apply" || len(doc.Members) != 3 {
		t.Fatalf("unexpected show json doc: %+v", doc)
	}

	// Empty members text output
	out.Reset()
	cmdEmpty := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return nil, nil },
	})
	cmdEmpty.SetOut(&out)
	cmdEmpty.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmdEmpty.Execute(); err != nil {
		t.Fatalf("umbrella show empty: %v", err)
	}
	if !strings.Contains(out.String(), "no recorded members") {
		t.Fatalf("expected 'no recorded members', got:\n%s", out.String())
	}

	// Show with active (not ready) member omits close offer.
	out.Reset()
	cmdActive := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members: func(string, umbrella.Ref) ([]umbrella.Member, error) {
			return []umbrella.Member{{Task: "t", Repository: "o/r", State: "active"}}, nil
		},
	})
	cmdActive.SetOut(&out)
	cmdActive.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmdActive.Execute(); err != nil {
		t.Fatalf("umbrella show active: %v", err)
	}
	if strings.Contains(out.String(), "next: wb umbrella close") {
		t.Fatalf("active member must not trigger close offer:\n%s", out.String())
	}
}

func TestUmbrellaCloseDryRunApplyAndRefusals(t *testing.T) {
	t.Parallel()
	readyMembers := []umbrella.Member{
		{Task: "cross-repo-umbrella-issue", Repository: "sneat-co/backstage", PullRequest: 846, State: "landed", LandedSHA: "1111111111111111", ReadyToClose: true},
		{Task: "cross-repo-umbrella-issue", Repository: "trakhimenok/workbench", PullRequest: 33, State: "landed", LandedSHA: "2222222222222222", ReadyToClose: true},
	}

	// 1. Dry run (default): does not post comment or close issue.
	var dryCalls [][]string
	var out bytes.Buffer
	cmdDry := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, &dryCalls),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return readyMembers, nil },
	})
	cmdDry.SetOut(&out)
	cmdDry.SetArgs([]string{"close", "sneat-co/backstage#845"})
	if err := cmdDry.Execute(); err != nil {
		t.Fatalf("umbrella close dry-run: %v", err)
	}
	if len(dryCalls) != 1 {
		t.Fatalf("dry-run made %d gh calls, want 1 (GET only): %v", len(dryCalls), dryCalls)
	}
	if !strings.Contains(out.String(), "dry-run:") || !strings.Contains(out.String(), "apply with: wb umbrella close sneat-co/backstage#845 --apply") {
		t.Fatalf("unexpected dry-run output:\n%s", out.String())
	}

	// Dry run JSON
	out.Reset()
	cmdDryJSON := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return readyMembers, nil },
	})
	cmdDryJSON.SetOut(&out)
	cmdDryJSON.SetArgs([]string{"close", "sneat-co/backstage#845", "--format", "json"})
	if err := cmdDryJSON.Execute(); err != nil {
		t.Fatalf("umbrella close dry-run json: %v", err)
	}
	var dryDoc CloseDocument
	if err := json.Unmarshal(out.Bytes(), &dryDoc); err != nil || dryDoc.Applied || dryDoc.Closed || dryDoc.ApplyCommand == "" {
		t.Fatalf("unexpected dry-run json: %+v (%v)", dryDoc, err)
	}

	// 2. Apply posts closing summary and closes issue.
	var applyCalls [][]string
	out.Reset()
	cmdApply := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, &applyCalls),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return readyMembers, nil },
	})
	cmdApply.SetOut(&out)
	cmdApply.SetArgs([]string{"close", "sneat-co/backstage#845", "--apply"})
	if err := cmdApply.Execute(); err != nil {
		t.Fatalf("umbrella close --apply: %v", err)
	}
	if len(applyCalls) != 3 {
		t.Fatalf("apply made %d gh calls, want 3 (GET, POST comment, PATCH closed): %v", len(applyCalls), applyCalls)
	}
	if !strings.Contains(out.String(), "closed umbrella github:sneat-co/backstage#845") {
		t.Fatalf("unexpected apply output:\n%s", out.String())
	}

	// Apply JSON
	out.Reset()
	cmdApplyJSON := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return readyMembers, nil },
	})
	cmdApplyJSON.SetOut(&out)
	cmdApplyJSON.SetArgs([]string{"close", "sneat-co/backstage#845", "--apply", "--format", "json"})
	if err := cmdApplyJSON.Execute(); err != nil {
		t.Fatalf("umbrella close --apply json: %v", err)
	}
	var applyDoc CloseDocument
	if err := json.Unmarshal(out.Bytes(), &applyDoc); err != nil || !applyDoc.Closed || applyDoc.State != umbrella.StateClosed {
		t.Fatalf("unexpected apply json: %+v (%v)", applyDoc, err)
	}

	// 3. Refuses while any member is neither landed nor archived with restore pointer.
	blockingMembers := []umbrella.Member{
		readyMembers[0],
		{Task: "cross-repo-umbrella-issue", Repository: "trakhimenok/workbench", State: "active", BlockingReason: "still active"},
		{Task: "cross-repo-umbrella-issue", Repository: "sneat-dev/wb", State: "discarded"},
	}
	var blockedCalls [][]string
	out.Reset()
	cmdBlocked := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, &blockedCalls),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return blockingMembers, nil },
	})
	cmdBlocked.SetOut(&out)
	cmdBlocked.SetArgs([]string{"close", "sneat-co/backstage#845", "--apply"})
	err := cmdBlocked.Execute()
	var exitErr *testExitError
	if !errors.As(err, &exitErr) || exitErr.code != shared.ExitUsage {
		t.Fatalf("blocked close error = %v, want ExitUsage", err)
	}
	if len(blockedCalls) != 1 {
		t.Fatalf("blocked close must not mutate provider, made %d calls", len(blockedCalls))
	}
	for _, want := range []string{"trakhimenok/workbench", "sneat-dev/wb", "neither landed nor archived"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("blocked close error missing %q: %v", want, err)
		}
	}

	// 4. Refuses already-closed umbrella (JSON mode too).
	out.Reset()
	cmdAlreadyClosed := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "closed", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return readyMembers, nil },
	})
	cmdAlreadyClosed.SetOut(&out)
	cmdAlreadyClosed.SetArgs([]string{"close", "sneat-co/backstage#845", "--format", "json"})
	if err := cmdAlreadyClosed.Execute(); err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Fatalf("already closed error = %v", err)
	}

	// 5. Refuses umbrella with no recorded members.
	out.Reset()
	cmdNoMembers := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return nil, nil },
	})
	cmdNoMembers.SetOut(&out)
	cmdNoMembers.SetArgs([]string{"close", "sneat-co/backstage#845"})
	if err := cmdNoMembers.Execute(); err == nil || !strings.Contains(err.Error(), "no recorded members") {
		t.Fatalf("no members error = %v", err)
	}

	// 6. Propagates provider Close error on --apply.
	out.Reset()
	cmdCloseErr := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", errors.New("api boom"), nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return readyMembers, nil },
	})
	cmdCloseErr.SetOut(&out)
	cmdCloseErr.SetArgs([]string{"close", "sneat-co/backstage#845", "--apply"})
	if err := cmdCloseErr.Execute(); err == nil || !strings.Contains(err.Error(), "api boom") {
		t.Fatalf("provider close error = %v", err)
	}
}

func TestUmbrellaCommandsRefuseUnknownRegistryAndErrors(t *testing.T) {
	t.Parallel()
	// Unknown registry (synchestra) refused clearly on both show and close.
	for _, sub := range []string{"show", "close"} {
		cmd := New(testRuntime("/projects"), Dependencies{
			DefaultRegistry: func() (string, error) { return "github", nil },
			Providers:       fakeGitHubProviders(t, "open", nil, nil),
			Members:         func(string, umbrella.Ref) ([]umbrella.Member, error) { return nil, nil },
		})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs([]string{sub, "synchestra:effort-1"})
		err := cmd.Execute()
		var exitErr *testExitError
		if !errors.As(err, &exitErr) || exitErr.code != shared.ExitUsage || !strings.Contains(err.Error(), "synchestra") {
			t.Fatalf("%s synchestra error = %v, want ExitUsage naming synchestra", sub, err)
		}
	}

	// Invalid format, malformed ref, DefaultRegistry error, Resolve error, Members error, default nil Providers.
	for _, sub := range []string{"show", "close"} {
		cmdBadFmt := New(testRuntime("/projects"), Dependencies{})
		cmdBadFmt.SetArgs([]string{sub, "sneat-co/backstage#845", "--format", "xml"})
		if err := cmdBadFmt.Execute(); err == nil {
			t.Fatalf("%s accepted invalid format", sub)
		}
	}

	cmdDefErr := New(testRuntime("/projects"), Dependencies{
		DefaultRegistry: func() (string, error) { return "", errors.New("bad config") },
	})
	cmdDefErr.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmdDefErr.Execute(); err == nil || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("DefaultRegistry error = %v", err)
	}

	// Explicit typed reference bypasses DefaultRegistry resolution.
	var explicitOut bytes.Buffer
	cmdExplicit := New(testRuntime("/projects"), Dependencies{
		DefaultRegistry: func() (string, error) { return "", errors.New("should not be called") },
		Providers:       fakeGitHubProviders(t, "open", nil, nil),
		Members:         func(string, umbrella.Ref) ([]umbrella.Member, error) { return nil, nil },
	})
	cmdExplicit.SetOut(&explicitOut)
	cmdExplicit.SetArgs([]string{"show", "github:sneat-co/backstage#845"})
	if err := cmdExplicit.Execute(); err != nil {
		t.Fatalf("explicit typed ref failed despite bypassing default registry: %v", err)
	}

	cmdBadRef := New(testRuntime("/projects"), Dependencies{})
	cmdBadRef.SetArgs([]string{"show", "not-a-valid-ref"})
	if err := cmdBadRef.Execute(); err == nil {
		t.Fatal("malformed ref accepted")
	}

	cmdResolveErr := New(testRuntime("/projects"), Dependencies{
		Providers: umbrella.DefaultProviders(func(context.Context, ...string) umbrella.CommandResult {
			return umbrella.CommandResult{Stderr: []byte("404 Not Found"), Err: errors.New("exit 1")}
		}),
	})
	cmdResolveErr.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmdResolveErr.Execute(); err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("Resolve error = %v", err)
	}

	cmdMembersErr := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return nil, errors.New("members failed") },
	})
	cmdMembersErr.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmdMembersErr.Execute(); err == nil || !strings.Contains(err.Error(), "members failed") {
		t.Fatalf("Members error = %v", err)
	}

	// Default Providers fallback when nil (runs against canceled context so no real network call).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmdNilProviders := New(testRuntime("/projects"), Dependencies{})
	cmdNilProviders.SetArgs([]string{"show", "sneat-co/backstage#845"})
	if err := cmdNilProviders.ExecuteContext(ctx); err == nil {
		t.Fatal("expected canceled context error with default providers")
	}

	// Writer error paths for show, close, refusal.
	ready := []umbrella.Member{{Task: "t", Repository: "o/r", State: "landed", LandedSHA: "abc", ReadyToClose: true}}
	for failAt := 1; failAt <= 4; failAt++ {
		w := &nthFailWriter{failAt: failAt}
		cmd := New(testRuntime("/projects"), Dependencies{
			Providers: fakeGitHubProviders(t, "open", nil, nil),
			Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return ready, nil },
		})
		cmd.SetOut(w)
		cmd.SetArgs([]string{"show", "o/r#1"})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("show failAt=%d succeeded", failAt)
		}
	}
	for failAt := 1; failAt <= 3; failAt++ {
		w := &nthFailWriter{failAt: failAt}
		cmd := New(testRuntime("/projects"), Dependencies{
			Providers: fakeGitHubProviders(t, "open", nil, nil),
			Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return ready, nil },
		})
		cmd.SetOut(w)
		cmd.SetArgs([]string{"close", "o/r#1"})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("close dry-run failAt=%d succeeded", failAt)
		}
	}
	cmdApplyWriteErr := New(testRuntime("/projects"), Dependencies{
		Providers: fakeGitHubProviders(t, "open", nil, nil),
		Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return ready, nil },
	})
	cmdApplyWriteErr.SetOut(failingWriter{})
	cmdApplyWriteErr.SetArgs([]string{"close", "o/r#1", "--apply"})
	if err := cmdApplyWriteErr.Execute(); err == nil {
		t.Fatal("close apply write error ignored")
	}
	for _, fmtName := range []string{"text", "json"} {
		cmdRefuseWriteErr := New(testRuntime("/projects"), Dependencies{
			Providers: fakeGitHubProviders(t, "closed", nil, nil),
			Members:   func(string, umbrella.Ref) ([]umbrella.Member, error) { return ready, nil },
		})
		cmdRefuseWriteErr.SetOut(failingWriter{})
		cmdRefuseWriteErr.SetArgs([]string{"close", "o/r#1", "--format", fmtName})
		if err := cmdRefuseWriteErr.Execute(); err == nil {
			t.Fatalf("refusal write error (%s) ignored", fmtName)
		}
	}
}
