package cmdrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/operationreceipt"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/spf13/cobra"
	"strings"
)

type Dependencies struct {
	Execute  func(context.Context, runexec.ExecuteRequest) (runexec.ExecuteResult, error)
	Changed  func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error)
	History  func(context.Context, runexec.HistoryRequest) (runexec.HistoryResult, error)
	Queue    func(runexec.QueueRequest) runqueue.QueueListing
	Recipes  func(context.Context, runexec.RecipeRequest) (runexec.RecipeResult, error)
	Submit   func(context.Context, runexec.SubmitRequest) (operationreceipt.Receipt, error)
	LoadHint func(string) string
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	usage := func(message string) error { return runtime.ExitError(shared.ExitUsage, message) }
	var (
		apply              bool
		async              bool
		configPath         string
		days               int
		history            bool
		jsonOut            bool
		list               bool
		idempotencyKey     string
		workerID           string
		allowSaturatedHost bool
		quiet              bool
		queueFlag          bool
		changed            bool
		changedTarget      string
	)
	cmd := &cobra.Command{
		Use:   "run [recipe] | run -- <command> [args...]",
		Short: "Run a fleet recipe or execute one command through WB",
		Long: `Run a configured fleet recipe, or use -- to execute one command through
WB while preserving its standard streams and exit code.

Recipe mode is a dry-run by default; --apply lands the recipe. Command mode
records privacy-safe receipts and admits CPU-heavy work. On a machine with
fewer than 8 CPUs, every governed command follows the original spec table
unchanged, admitted against a machine-wide CPUCount-1 budget (focused 1,
broad 2, coverage/race the whole budget). On a machine with 8 or more CPUs,
that fixed budget does not apply: a focused job (a single-package Go
test/vet, or a light lint) is always admitted immediately at max(1,
NumCPU/8) and never waits behind a heavy one, and a "heavy" job (a broad
Go/Node test or build, or any coverage/race run) is instead admitted
adaptively out of the full NumCPU: when k heavy jobs (itself included) are
running or waiting at the moment it is admitted, it gets min(share(k),
1.5xNumCPU minus the sum of already-running heavy jobs' own allocations) —
share(k) is NumCPU at k=1, 2/3 of NumCPU at k=2, and half of NumCPU at
k>=3 — so one heavy job alone gets the whole machine, and a burst of them
share it, never exceeding 150% of NumCPU in total. A candidate below
NumCPU/4 waits instead, in strict FIFO order among heavy waiters. Each
job's own GOMAXPROCS and Go -p equal its allocation once admitted, fixed
for its whole run because a running process's GOMAXPROCS cannot change
after the fact — so a later arrival admitted at a smaller share does not
shrink an already-running job, and the machine can briefly run above 100%
(bounded by the 150% cap above) until the earlier job finishes; this is
accepted because these commands mostly wait on I/O and subprocesses rather
than pure CPU. It is synchronous by default; --async
submits through the authenticated durable local daemon queue for the
explicitly selected sandboxed wb worker connect process to execute. Command
arguments are durable journal data; pass secrets through the worker's
inherited environment, never argv. The client uses the protected local
socket first and reports when sandbox transport denial selects the
authenticated project-root file bridge.

A CPU-heavy command mode invocation reports its place in the shared CPU
budget on stderr: a queued line naming its position and what it is waiting
on, a heartbeat at most every 10s while it keeps waiting, and admitted/done
receipt lines — even without a terminal, so a redirected log still shows
progress instead of going silent for minutes. --quiet silences these lines;
--queue lists who currently holds or is waiting for CPU capacity.

--changed scopes command mode to the Go packages a local diff touches
against --target (default: the repository's detected default branch,
issue #570 / spec/plans/coverage-to-100/README.md task-19): staged,
unstaged, and already-committed changes since the merge base, combined,
scoped to the Go module that contains the current directory (a nested
module such as <product>/backend/go.mod works the same as a repository
whose module sits at its root; a DIFFERENT Go module nested further inside
that one, its own go.mod below the current directory, is excluded, since
the go tool cannot build a package belonging to a different module) —
untracked files are never included, because ` + "`git diff`" + ` itself
never reports them. The command runs once with those package patterns
appended (for example "go test ./internal/foo ./cmd/wb"); when nothing
changed, WB prints that and exits 0 without running the command at all.
This is a smoke check scoped to what changed, never a prediction of a
full/merged coverage or vet run. The package patterns are always the last
arguments: --changed is rejected outright if the command itself contains
-args, --args, or a nested --, rather than silently sending them to the
wrong place.`,
		Example: `# Discover configured recipes
wb run --list

# Preview, then apply one reusable fleet recipe
wb run refresh-ci
wb run refresh-ci --apply

# Run a command through WB
wb run -- go test ./internal/worktrees -run TestCreate

# Submit without waiting; inspect with wb daemon operation get/wait/cancel
wb run --async --worker codex-local -- go test ./internal/worktrees -run TestCreate

# Give intentionally identical submissions distinct durable identities
wb run --async --worker codex-local --idempotency-key test-create-2 -- go test ./internal/worktrees -run TestCreate

# Inspect command cost in this worktree
wb run --history --days 7

# See who currently holds or is waiting for CPU capacity
wb run --queue

# Silence the queued/admitted/done receipt lines
wb run --quiet -- go test ./internal/worktrees -run TestCreate

# Run only the packages the local diff touched against the default branch
wb run --changed -- go test

# ...against an explicit target instead of the detected default branch
wb run --changed --target origin/main -- go vet`,
		Args: func(cmd *cobra.Command, args []string) error {
			if history || queueFlag {
				return cobra.NoArgs(cmd, args)
			}
			if cmd.ArgsLenAtDash() == 0 {
				if len(args) == 0 {
					return fmt.Errorf("command is required after --")
				}
				return nil
			}
			return cobra.MaximumNArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if queueFlag {
				if apply || async || configPath != "" || idempotencyKey != "" || list || history || quiet || changed || changedTarget != "" {
					return usage("--apply, --async, --changed, --config, --history, --idempotency-key, --list, --quiet, and --target cannot be used with --queue")
				}
				return printQueue(cmd.OutOrStdout(), deps.Queue(runexec.QueueRequest{ProjectsRoot: runtime.Flags().ProjectsRoot}), jsonOut)
			}
			if history {
				if apply || async || configPath != "" || idempotencyKey != "" || list || quiet || changed || changedTarget != "" {
					return usage("--apply, --async, --changed, --config, --idempotency-key, --list, --quiet, and --target cannot be used with --history")
				}
				if days < 1 {
					return usage("--days must be at least 1")
				}
				result, err := deps.History(cmd.Context(), runexec.HistoryRequest{Days: days})
				if err != nil {
					return err
				}
				return printHistory(cmd.OutOrStdout(), result, jsonOut)
			}
			if cmd.ArgsLenAtDash() == 0 {
				if apply || configPath != "" || list || days != 14 || shared.OutputFormatChanged(cmd) {
					return usage("--apply, --config, --days, --format, --history, --json, and --list belong to WB modes and cannot be used with run --")
				}
				if changed {
					if async {
						return usage("--changed cannot be combined with --async")
					}
					expanded, err := expandChanged(cmd, args, changedTarget, deps.Changed, usage)
					if err != nil {
						return err
					}
					if expanded == nil {
						return nil
					}
					args = expanded
				} else if changedTarget != "" {
					return usage("--target requires --changed")
				}
				if async {
					if strings.TrimSpace(workerID) == "" {
						return usage("--async requires --worker <stable-id>; WB never guesses which sandbox may execute the job")
					}
					result, err := deps.Submit(cmd.Context(), runexec.SubmitRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Argv: append([]string(nil), args...), WorkerID: strings.TrimSpace(workerID), IdempotencyKey: strings.TrimSpace(idempotencyKey), Stderr: cmd.ErrOrStderr()})
					if err != nil {
						return err
					}
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(result)
				}
				if workerID != "" {
					return usage("--worker requires --async command mode")
				}
				if idempotencyKey != "" {
					return usage("--idempotency-key requires --async command mode")
				}
				progress := newQueueProgress(cmd.ErrOrStderr(), !quiet, configPath, deps.LoadHint)
				result, err := deps.Execute(cmd.Context(), runexec.ExecuteRequest{Argv: append([]string(nil), args...), ProjectsRoot: runtime.Flags().ProjectsRoot, ConfigPath: configPath, AllowSaturatedHost: allowSaturatedHost, Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(), Observe: progress.report})
				if err != nil {
					return err
				}
				if result.ChildFailed {
					return runtime.ExitError(result.ExitCode, fmt.Sprintf("%s exited with status %d", args[0], result.ExitCode))
				}
				return nil
			}
			if async {
				return usage("--async requires command mode with run --")
			}
			if workerID != "" {
				return usage("--worker requires --async command mode")
			}
			if idempotencyKey != "" {
				return usage("--idempotency-key requires --async command mode")
			}
			if allowSaturatedHost {
				return usage("--allow-saturated-host requires command mode with run --")
			}
			if quiet {
				return usage("--quiet requires command mode with run --")
			}
			if changed || changedTarget != "" {
				return usage("--changed and --target require command mode with run --")
			}
			if days != 14 || shared.OutputFormatChanged(cmd) {
				return usage("--days, --format=json, and --json require --history")
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			flags := runtime.Flags()
			result, err := deps.Recipes(cmd.Context(), runexec.RecipeRequest{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, ExtraOrgs: append([]string(nil), flags.ExtraOrgs...), ConfigPath: configPath, Name: name, List: list, Apply: apply, Observe: func(event runexec.RecipeEvent) error {
				return printRecipeEvent(cmd.OutOrStdout(), cmd.ErrOrStderr(), event)
			}})
			if err != nil {
				var failure *runexec.RecipeFailure
				if !errors.As(err, &failure) {
					return err
				}
				if failure.Discovery {
					if _, writeErr := fmt.Fprintln(cmd.ErrOrStderr(), "discovery error:", err); writeErr != nil {
						return writeErr
					}
				} else {
					if _, writeErr := fmt.Fprintln(cmd.ErrOrStderr(), err); writeErr != nil {
						return writeErr
					}
				}
				return runtime.ExitError(shared.ExitFindings, "the recipe reported errors, or drift that --apply would land; see the per-repository lines above")
			}
			if err := printRecipeResult(cmd.OutOrStdout(), result, list || name == ""); err != nil {
				return err
			}
			if result.Findings {
				return runtime.ExitError(shared.ExitFindings, "the recipe reported errors, or drift that --apply would land; see the per-repository lines above")
			}

			return nil
		},
	}
	cmd.Annotations = map[string]string{"wb.dev/discovery-terms": "run recipe reusable fleet change apply dry run automation repeat command"}
	cmd.Flags().BoolVar(&apply, "apply", false, "commit & push changes (default: dry-run report)")
	cmd.Flags().BoolVar(&async, "async", false, "submit for a sandboxed worker and return the durable queue receipt")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable caller key for an intentional submission or exact retry")
	cmd.Flags().StringVar(&workerID, "worker", "", "stable ID of the sandbox worker that must execute the async job")
	cmd.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	cmd.Flags().BoolVar(&allowSaturatedHost, "allow-saturated-host", false, "command mode: admit CPU-heavy work even when the host's load average exceeds the admission.load_floor in wb.yaml (default: 2x runtime.NumCPU()); the check is disabled automatically in CI (CI=true/GITHUB_ACTIONS=true) and can be disabled or overridden with WB_ADMISSION_LOAD_FLOOR (0 disables, a positive number sets the floor)")
	cmd.Flags().IntVar(&days, "days", 14, "history window in calendar days")
	cmd.Flags().BoolVar(&history, "history", false, "summarize governed commands in the current worktree")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().BoolVar(&list, "list", false, "list configured recipes and exit")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "command mode: silence the queued/admitted/done receipt lines on stderr")
	cmd.Flags().BoolVar(&queueFlag, "queue", false, "list this machine's CPU lease queue (running and waiting governed commands) and exit")
	cmd.Flags().BoolVar(&changed, "changed", false, "command mode: append the Go packages a local diff touches (against --target) to the command instead of running it as given; exits 0 without running when nothing changed")
	cmd.Flags().StringVar(&changedTarget, "target", "", "merge-base branch or ref for --changed (default: the repository's detected default branch)")
	return cmd
}
