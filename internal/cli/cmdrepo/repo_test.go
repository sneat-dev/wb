package cmdrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type codedError struct {
	code    int
	message string
}

func (e *codedError) Error() string { return e.message }
func runtimeFor(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func fakeDependencies() Dependencies {
	return Dependencies{
		Status: statusview.Dependencies{
			Collect: func(reposelection.Request, repostatus.Observer) (repostatus.Index, error) {
				return repostatus.Index{SchemaVersion: 1}, nil
			},
			WriteReports: func(repostatus.Index, string, bool, string) error { return nil },
			Interactive:  func(io.Writer, bool) bool { return false },
		},
		SetSkipSync: func(string) error { return nil }, UnsetSkipSync: func(string) error { return nil },
		InitRemote: func(string, func(gitops.InitRemoteEvent)) error { return nil },
		RecoverTransfer: func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
			return worktrees.RepositoryTransferCleanupResult{Eligible: true}, nil
		},
	}
}
func execute(cmd *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRepoInitRemoteIsRegisteredUnderRepoCommand(t *testing.T) {
	t.Parallel()
	repo := New(runtimeFor(&shared.Flags{}), fakeDependencies())
	for _, path := range [][]string{{"init-remote"}, {"status"}, {"ignore"}, {"transfer", "cleanup"}} {
		command, remaining, err := repo.Find(path)
		if err != nil || len(remaining) != 0 || command == nil || command.Name() != path[len(path)-1] {
			t.Errorf("repo command %v = (%v, %v, %v)", path, command, remaining, err)
		}
	}
}
func TestRepoInitRemoteCommandDefaultsToCurrentDirectoryAndAcceptsExplicitPath(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		path string
	}{{nil, "."}, {[]string{"/fixture/repo"}, "/fixture/repo"}} {
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			var paths []string
			deps.InitRemote = func(path string, notice func(gitops.InitRemoteEvent)) error { paths = append(paths, path); return nil }
			_, err := execute(New(runtimeFor(&shared.Flags{}), deps), append([]string{"init-remote"}, test.args...)...)
			if err != nil || !reflect.DeepEqual(paths, []string{test.path}) {
				t.Fatalf("path handoff=%v error=%v", paths, err)
			}
		})
	}
}
func TestIgnoreDelegatesBothModesAndPreservesOutputAndErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation")
	for _, test := range []struct{ unset, explicit, fail bool }{{}, {unset: true}, {explicit: true}, {fail: true}, {unset: true, fail: true}} {
		t.Run(strings.Join([]string{string(rune('0' + boolInt(test.unset))), string(rune('0' + boolInt(test.explicit))), string(rune('0' + boolInt(test.fail)))}, ""), func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			var calls []string
			op := func(mode string) func(string) error {
				return func(path string) error {
					calls = append(calls, mode+":"+path)
					if test.fail {
						return sentinel
					}
					return nil
				}
			}
			deps.SetSkipSync = op("set")
			deps.UnsetSkipSync = op("unset")
			args := []string{"ignore"}
			path := "."
			mode := "set"
			message := "ignored by wb sync"
			if test.unset {
				args = append(args, "--unset")
				mode = "unset"
				message = "wb sync re-enabled"
			}
			if test.explicit {
				path = "/fixture"
				args = append(args, path)
			}
			out, err := execute(New(runtimeFor(&shared.Flags{}), deps), args...)
			if !reflect.DeepEqual(calls, []string{mode + ":" + path}) {
				t.Fatal(calls)
			}
			if test.fail {
				if err != sentinel || out != "" {
					t.Fatalf("out=%q err=%v", out, err)
				}
			} else if err != nil || out != path+": "+message+"\n" {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func TestInitRemoteNoticesUseCurrentWriterWithoutStoppingPublication(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("push")
	for _, fail := range []bool{false, true} {
		t.Run(strings.ToLower(string(rune('A'+boolInt(fail)))), func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			deps.InitRemote = func(path string, notice func(gitops.InitRemoteEvent)) error {
				notice(gitops.InitRemoteEvent{Kind: gitops.CreatedInitialCommit, Path: path, Branch: "main"})
				notice(gitops.InitRemoteEvent{Kind: gitops.Published, Path: path, Branch: "main"})
				if fail {
					return sentinel
				}
				return nil
			}
			out, err := execute(New(runtimeFor(&shared.Flags{}), deps), "init-remote", "/repo")
			want := "/repo: created an empty initial commit on main\n/repo: pushed main to origin and set it as upstream\n"
			if out != want || fail && err != sentinel || !fail && err != nil {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
	deps := fakeDependencies()
	calls := 0
	deps.InitRemote = func(_ string, notice func(gitops.InitRemoteEvent)) error {
		notice(gitops.InitRemoteEvent{Kind: gitops.CreatedInitialCommit})
		calls++
		notice(gitops.InitRemoteEvent{Kind: gitops.Published})
		return sentinel
	}
	cmd := New(runtimeFor(&shared.Flags{}), deps)
	cmd.SetOut(failedWriter{errors.New("writer")})
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"init-remote"})
	if err := cmd.Execute(); err != sentinel || calls != 1 {
		t.Fatalf("notice interrupted operation: %v calls=%d", err, calls)
	}
}
func TestIgnoreWriteFailureDoesNotUndoSuccessfulMutation(t *testing.T) {
	t.Parallel()
	for _, unset := range []bool{false, true} {
		t.Run(string(rune('A'+boolInt(unset))), func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			calls := 0
			op := func(string) error { calls++; return nil }
			deps.SetSkipSync = op
			deps.UnsetSkipSync = op
			cmd := New(runtimeFor(&shared.Flags{}), deps)
			cmd.SetOut(failedWriter{errors.New("writer")})
			cmd.SetErr(io.Discard)
			args := []string{"ignore"}
			if unset {
				args = append(args, "--unset")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
func TestRepoTransferCleanupRequiresReceiptAsUsage(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.RecoverTransfer = func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
		t.Fatal("operation called")
		return worktrees.RepositoryTransferCleanupResult{}, nil
	}
	_, err := execute(New(runtimeFor(&shared.Flags{}), deps), "transfer", "cleanup")
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != 2 || !strings.Contains(err.Error(), "--receipt is required") {
		t.Fatalf("usage refusal=%v", err)
	}
}
func TestTransferUsesLazyFlagsContextAndDefaultOptions(t *testing.T) {
	t.Parallel()
	for _, apply := range []bool{false, true} {
		t.Run(string(rune('A'+boolInt(apply))), func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before"}
			deps := fakeDependencies()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var got worktrees.RepositoryTransferCleanupOptions
			deps.RecoverTransfer = func(actual context.Context, options worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
				if actual != ctx {
					t.Fatal("context replaced")
				}
				got = options
				return worktrees.RepositoryTransferCleanupResult{Eligible: true}, nil
			}
			cmd := New(runtimeFor(&flags), deps)
			cmd.PersistentFlags().StringVar(&flags.ProjectsRoot, "projects-root", "before", "")
			cmd.SetContext(ctx)
			args := []string{"transfer", "cleanup", "--receipt", "receipt", "--projects-root", "parsed"}
			if apply {
				args = append(args, "--apply")
			}
			_, err := execute(cmd, args...)
			want := worktrees.RepositoryTransferCleanupOptions{ProjectsRoot: "parsed", ReceiptPath: "receipt", Apply: apply}
			if err != nil || got != want {
				t.Fatalf("options=%+v err=%v", got, err)
			}
		})
	}
}
func TestTransferTextAndJSONKeepSingleResultAndRefusalPolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		result worktrees.RepositoryTransferCleanupResult
		args   []string
	}{
		{name: "planned", result: worktrees.RepositoryTransferCleanupResult{Eligible: true}},
		{name: "completed", result: worktrees.RepositoryTransferCleanupResult{Eligible: true, Applied: true, Outcome: "retired"}},
		{name: "refused", result: worktrees.RepositoryTransferCleanupResult{Reason: "unsafe"}},
		{name: "json", result: worktrees.RepositoryTransferCleanupResult{Eligible: true, ReceiptPath: "receipt", QuarantineDir: "quarantine"}, args: []string{"--format=json"}},
		{name: "json refusal", result: worktrees.RepositoryTransferCleanupResult{Reason: "unsafe"}, args: []string{"--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			deps.RecoverTransfer = func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
				return test.result, nil
			}
			out, err := execute(New(runtimeFor(&shared.Flags{}), deps), append([]string{"transfer", "cleanup", "--receipt", "receipt"}, test.args...)...)
			if test.result.Eligible && err != nil || !test.result.Eligible && (err == nil || err.Error() != "replacement cleanup refused: unsafe") {
				t.Fatalf("err=%v", err)
			}
			if len(test.args) > 0 {
				var got worktrees.RepositoryTransferCleanupResult
				decoder := json.NewDecoder(strings.NewReader(out))
				if e := decoder.Decode(&got); e != nil || got != test.result {
					t.Fatalf("JSON=%q result=%+v error=%v", out, got, e)
				}
				var extra any
				if e := decoder.Decode(&extra); e != io.EOF {
					t.Fatalf("extra output=%v", e)
				}
			} else {
				state := test.name
				want := "Replacement cleanup  " + state + "\nQuarantine          \nReceipt             \n"
				if test.result.Outcome != "" {
					want += "Outcome             " + test.result.Outcome + "\n"
				}
				if test.result.Reason != "" {
					want += "Reason              " + test.result.Reason + "\n"
				}
				if out != want {
					t.Fatalf("out=%q want=%q", out, want)
				}
			}
		})
	}
}
func TestTransferOperationAndWriterFailuresHaveOriginalPrecedence(t *testing.T) {
	t.Parallel()
	operation := errors.New("operation")
	writer := errors.New("writer")
	deps := fakeDependencies()
	deps.RecoverTransfer = func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
		return worktrees.RepositoryTransferCleanupResult{}, operation
	}
	out, err := execute(New(runtimeFor(&shared.Flags{}), deps), "transfer", "cleanup", "--receipt", "x")
	if err != operation || out != "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	deps.RecoverTransfer = func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
		return worktrees.RepositoryTransferCleanupResult{Reason: "unsafe"}, nil
	}
	for _, jsonOut := range []bool{false, true} {
		cmd := New(runtimeFor(&shared.Flags{}), deps)
		cmd.SetOut(failedWriter{writer})
		cmd.SetErr(io.Discard)
		args := []string{"transfer", "cleanup", "--receipt", "x"}
		if jsonOut {
			args = append(args, "--json")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		if jsonOut && err != writer || !jsonOut && (err == nil || err.Error() != "replacement cleanup refused: unsafe") {
			t.Fatalf("JSON=%t err=%v", jsonOut, err)
		}
	}
}
func TestInvalidArgumentsNeverCallOperations(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"ignore", "a", "b"}, {"init-remote", "a", "b"}, {"status", "a", "b"}, {"transfer", "cleanup", "extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			deps := fakeDependencies()
			calls := 0
			deps.SetSkipSync = func(string) error { calls++; return nil }
			deps.InitRemote = func(string, func(gitops.InitRemoteEvent)) error { calls++; return nil }
			deps.RecoverTransfer = func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
				calls++
				return worktrees.RepositoryTransferCleanupResult{}, nil
			}
			deps.Status.Collect = func(reposelection.Request, repostatus.Observer) (repostatus.Index, error) {
				calls++
				return repostatus.Index{}, nil
			}
			_, err := execute(New(runtimeFor(&shared.Flags{}), deps), args...)
			if err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
