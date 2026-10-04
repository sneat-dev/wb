package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"strings"
	"testing"
	"time"
)

type activeCodeError struct {
	code    int
	message string
}

func (err *activeCodeError) Error() string { return err.message }
func activeRuntime(flags func() shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: flags, ExitError: func(code int, message string) error { return &activeCodeError{code: code, message: message} }}
}

type activeListProvider struct {
	remotestate.Provider
	entries []remotestate.Entry
}

func (provider activeListProvider) List(context.Context) ([]remotestate.Entry, error) {
	return provider.entries, nil
}
func activeCommandDeps(now time.Time) worktreerun.ActiveDependencies {
	return worktreerun.ActiveDependencies{Claims: func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil }, Sessions: func(string) ([]session.View, error) { return nil, nil }, Remote: worktreerun.ActiveRemoteDependencies{Load: func(string) (remotestate.Config, remotestate.Provider, error) {
		return remotestate.Config{Machine: "local"}, activeListProvider{}, nil
	}, Login: func() (string, error) { return "me", nil }, Now: func() time.Time { return now }}}
}
func TestActiveCommandReadsParsedRuntimeFlagsAndPreservesFindingOutput(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	for _, style := range []string{"text", "json"} {
		for _, finding := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%t", style, finding), func(t *testing.T) {
				t.Parallel()
				flags := shared.Flags{ProjectsRoot: "before", Filter: "before"}
				deps := activeCommandDeps(now)
				deps.Claims = func(root, filter string) ([]worktrees.ActiveClaimSummary, error) {
					if root != "parsed-root" || filter != "parsed-filter" {
						t.Fatalf("flags captured before parsing: %q %q", root, filter)
					}
					if finding {
						return []worktrees.ActiveClaimSummary{{Task: "old", RecordedAt: now.Add(-72 * time.Hour)}}, nil
					}
					return nil, nil
				}
				command := NewActive(activeRuntime(func() shared.Flags { return flags }), deps)
				flags = shared.Flags{ProjectsRoot: "parsed-root", Filter: "parsed-filter"}
				command.SetArgs([]string{"--format", style, "--local-only"})
				command.SilenceErrors = true
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(&bytes.Buffer{})
				err := command.Execute()
				if finding {
					var code *activeCodeError
					if !errors.As(err, &code) || code.code != shared.ExitFindings {
						t.Fatalf("finding=%v", err)
					}
					if !strings.Contains(out.String(), "incomplete") {
						t.Fatalf("finding report absent: %q", out.String())
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if out.Len() == 0 {
					t.Fatal("missing report")
				}
			})
		}
	}
}
func TestActiveCommandPreservesFormatCollectionAndWriterErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("active boundary failed")
	for _, phase := range []string{"format", "collect", "text writer", "json writer"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			deps := activeCommandDeps(time.Now())
			if phase == "collect" {
				deps.Claims = func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, boom }
			}
			command := NewActive(activeRuntime(func() shared.Flags { return shared.Flags{ProjectsRoot: t.TempDir()} }), deps)
			format := "text"
			if phase == "format" {
				format = "invalid"
			}
			if phase == "json writer" {
				format = "json"
			}
			command.SetArgs([]string{"--format", format, "--local-only"})
			command.SilenceErrors = true
			command.SetOut(receiptFailWriter{err: boom})
			command.SetErr(&bytes.Buffer{})
			if err := command.Execute(); err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
}
func TestActiveCommandStaleRemoteAndUnsafeSummaryRetainOriginalClauses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 18, 0, 0, 0, time.UTC)
	deps := activeCommandDeps(now)
	deps.Remote.Load = func(string) (remotestate.Config, remotestate.Provider, error) {
		return remotestate.Config{Machine: "local"}, activeListProvider{entries: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "vm", PublishedAt: now.Add(-48 * time.Hour), Worktrees: []remotestate.WorktreeState{{Task: "unsafe-control", TaskSummary: "line one\nline two", OwnerState: "active"}, {Task: "unsafe-long", TaskSummary: strings.Repeat("x", worktrees.MaxTaskSummaryRunes+1), OwnerState: "active"}}}}}}, nil
	}
	for _, style := range []string{"text", "json"} {
		caseDeps := deps
		t.Run(style, func(t *testing.T) {
			t.Parallel()
			command := NewActive(activeRuntime(func() shared.Flags { return shared.Flags{ProjectsRoot: t.TempDir()} }), caseDeps)
			command.SetArgs([]string{"--format", style})
			command.SilenceErrors = true
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&bytes.Buffer{})
			err := command.Execute()
			var finding *activeCodeError
			if !errors.As(err, &finding) || finding.code != shared.ExitFindings {
				t.Fatalf("finding=%v", err)
			}
			if strings.Contains(out.String(), "line one") || strings.Contains(out.String(), "line two") || strings.Contains(out.String(), strings.Repeat("x", worktrees.MaxTaskSummaryRunes+1)) {
				t.Fatalf("unsafe text output=%q err=%v", out.String(), err)
			}
		})
	}
	// Original stale-report assertion uses a snapshot without invalid summaries.
	deps.Remote.Load = func(string) (remotestate.Config, remotestate.Provider, error) {
		return remotestate.Config{Machine: "local"}, activeListProvider{entries: []remotestate.Entry{{Snapshot: remotestate.Snapshot{Login: "alice", Machine: "vm", PublishedAt: now.Add(-48 * time.Hour)}}}}, nil
	}
	command := NewActive(activeRuntime(func() shared.Flags { return shared.Flags{ProjectsRoot: t.TempDir()} }), deps)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	command.SilenceErrors = true
	command.SetArgs([]string{"--format", "json"})
	err := command.Execute()
	var finding *activeCodeError
	if !errors.As(err, &finding) || finding.code != shared.ExitFindings || !strings.Contains(output.String(), `"status":"stale"`) {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}

func TestActiveTextPropagatesRowSummaryStaleAndFinalWriteFailures(t *testing.T) {
	t.Parallel()
	report := worktreerun.ActiveReport{Local: worktreerun.ActiveLocalStatus{Status: "incomplete", OmittedUnresolvedClaims: 2}, Remote: worktreerun.ActiveRemoteStatus{Status: "stale", Error: "one snapshot is stale"}, Worktrees: []worktreerun.ActiveRow{{Summary: "summary", SnapshotStale: true}}}
	for allow := 7; allow < 10; allow++ {
		writer := &activeLimitedWriter{Allow: allow}
		if err := writeActiveWorktreeText(writer, report); err == nil {
			t.Fatalf("write failure after %d writes was lost", allow)
		}
	}
}

func TestActiveCommandIncompleteClaimsPreserveOriginalTextAndJSONClauses(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	deps := activeCommandDeps(now)
	deps.Claims = func(string, string) ([]worktrees.ActiveClaimSummary, error) {
		return []worktrees.ActiveClaimSummary{{Task: "old", Repository: "acme/app", RecordedAt: now.Add(-72 * time.Hour)}}, nil
	}
	execute := func(format string) (string, error) {
		t.Helper()
		command := NewActive(activeRuntime(func() shared.Flags { return shared.Flags{ProjectsRoot: t.TempDir()} }), deps)
		command.SetArgs([]string{"--format", format})
		command.SilenceErrors = true
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&bytes.Buffer{})
		err := command.Execute()
		return output.String(), err
	}
	stdout, err := execute("text")
	var code *activeCodeError
	if !errors.As(err, &code) || code.code != shared.ExitFindings {
		t.Fatalf("active with an omitted local claim exit = %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "local: incomplete") {
		t.Fatalf("active incomplete stdout = %q", stdout)
	}
	stdout, err = execute("json")
	if !errors.As(err, &code) || code.code != shared.ExitFindings {
		t.Fatalf("active json exit = %v", err)
	}
	if !strings.Contains(stdout, "\"omitted_unresolved_claims\":1") {
		t.Fatalf("active json stdout = %q", stdout)
	}
}
