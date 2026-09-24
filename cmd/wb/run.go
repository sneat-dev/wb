package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/recipe"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func newRunCmd() *cobra.Command {
	return newRunCmdWithDaemonDependencies(defaultDaemonDependencies())
}

func newRunCmdWithDaemonDependencies(daemonDeps daemonDependencies) *cobra.Command {
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
					return usageError("--apply, --async, --changed, --config, --history, --idempotency-key, --list, --quiet, and --target cannot be used with --queue")
				}
				return printRunQueue(cmd, jsonOut)
			}
			if history {
				if apply || async || configPath != "" || idempotencyKey != "" || list || quiet || changed || changedTarget != "" {
					return usageError("--apply, --async, --changed, --config, --idempotency-key, --list, --quiet, and --target cannot be used with --history")
				}
				return printRunHistory(cmd, days, jsonOut)
			}
			if cmd.ArgsLenAtDash() == 0 {
				if apply || configPath != "" || list || days != 14 || outputFormatChanged(cmd) {
					return usageError("--apply, --config, --days, --format, --history, --json, and --list belong to WB modes and cannot be used with run --")
				}
				if changed {
					if async {
						return usageError("--changed cannot be combined with --async")
					}
					expanded, err := expandChangedRunArgs(cmd, args, changedTarget)
					if err != nil {
						return err
					}
					if expanded == nil {
						return nil
					}
					args = expanded
				} else if changedTarget != "" {
					return usageError("--target requires --changed")
				}
				if async {
					if strings.TrimSpace(workerID) == "" {
						return usageError("--async requires --worker <stable-id>; WB never guesses which sandbox may execute the job")
					}
					return submitWorkerOperation(cmd, daemonDeps, strings.TrimSpace(workerID), strings.TrimSpace(idempotencyKey), args)
				}
				if workerID != "" {
					return usageError("--worker requires --async command mode")
				}
				if idempotencyKey != "" {
					return usageError("--idempotency-key requires --async command mode")
				}
				return runExternalCommand(cmd, args, configPath, allowSaturatedHost, quiet)
			}
			if async {
				return usageError("--async requires command mode with run --")
			}
			if workerID != "" {
				return usageError("--worker requires --async command mode")
			}
			if idempotencyKey != "" {
				return usageError("--idempotency-key requires --async command mode")
			}
			if allowSaturatedHost {
				return usageError("--allow-saturated-host requires command mode with run --")
			}
			if quiet {
				return usageError("--quiet requires command mode with run --")
			}
			if changed || changedTarget != "" {
				return usageError("--changed and --target require command mode with run --")
			}
			if days != 14 || outputFormatChanged(cmd) {
				return usageError("--days, --format=json, and --json require --history")
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			if code := runRun(projectsRoot, filterFlag, extraOrgs, configPath, name, list, apply); code != 0 {
				return &exitError{
					code:    code,
					message: "the recipe reported errors, or drift that --apply would land; see the per-repository lines above",
				}
			}
			return nil
		},
	}
	setDiscoveryTerms(cmd, "run recipe reusable fleet change apply dry run automation repeat command")
	cmd.Flags().BoolVar(&apply, "apply", false, "commit & push changes (default: dry-run report)")
	cmd.Flags().BoolVar(&async, "async", false, "submit for a sandboxed worker and return the durable queue receipt")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "stable caller key for an intentional submission or exact retry")
	cmd.Flags().StringVar(&workerID, "worker", "", "stable ID of the sandbox worker that must execute the async job")
	cmd.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	cmd.Flags().BoolVar(&allowSaturatedHost, "allow-saturated-host", false, "command mode: admit CPU-heavy work even when the host's load average exceeds the admission.load_floor in wb.yaml (default: 2x runtime.NumCPU()); the check is disabled automatically in CI (CI=true/GITHUB_ACTIONS=true) and can be disabled or overridden with WB_ADMISSION_LOAD_FLOOR (0 disables, a positive number sets the floor)")
	cmd.Flags().IntVar(&days, "days", 14, "history window in calendar days")
	cmd.Flags().BoolVar(&history, "history", false, "summarize governed commands in the current worktree")
	addJSONFormatFlags(cmd, &jsonOut)
	cmd.Flags().BoolVar(&list, "list", false, "list configured recipes and exit")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "command mode: silence the queued/admitted/done receipt lines on stderr")
	cmd.Flags().BoolVar(&queueFlag, "queue", false, "list this machine's CPU lease queue (running and waiting governed commands) and exit")
	cmd.Flags().BoolVar(&changed, "changed", false, "command mode: append the Go packages a local diff touches (against --target) to the command instead of running it as given; exits 0 without running when nothing changed")
	cmd.Flags().StringVar(&changedTarget, "target", "", "merge-base branch or ref for --changed (default: the repository's detected default branch)")
	return cmd
}

func printRunHistory(cmd *cobra.Command, days int, jsonOut bool) error {
	if days < 1 {
		return usageError("--days must be at least 1")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	events, path, err := runlog.ReadCurrent(cwd)
	if err != nil {
		return err
	}
	summary := runlog.Summarize(events, time.Now().AddDate(0, 0, -days))
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Governed commands · %d days · %s\n", days, path); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "operations %d · failed %d · running %d · wall %s · CPU %s\n",
		summary.Operations, summary.Failed, summary.Running,
		time.Duration(summary.WallMS)*time.Millisecond,
		time.Duration(summary.UserCPUMS+summary.SystemCPUMS)*time.Millisecond); err != nil {
		return err
	}
	for _, kind := range summary.Kinds {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-20s %4d runs  %2d failed  p50 %s  p95 %s  total %s\n",
			kind.Kind, kind.Operations, kind.Failed,
			time.Duration(kind.P50MS)*time.Millisecond,
			time.Duration(kind.P95MS)*time.Millisecond,
			time.Duration(kind.WallMS)*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func runExternalCommand(cmd *cobra.Command, args []string, configPath string, allowSaturatedHost, quiet bool) error {
	commandStarted := time.Now()
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	recorder, telemetryErr := runlog.Begin(cwd, args, commandStarted)
	if telemetryErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: command telemetry start failed: %v\n", telemetryErr)
	}
	kind := runqueue.Classify(args)
	if kind != runqueue.KindNone {
		floor, skippedReason := hostload.Resolve(configPath)
		if loadErr := hostload.Check(nil, floor, allowSaturatedHost); loadErr != nil {
			_ = recorder.Finish(exitFindings, 0, 0, time.Now())
			return fmt.Errorf("wb: %w", loadErr)
		}
		if skippedReason != "" {
			recorder.RecordLoadFloorSkipped(skippedReason)
		}
		if allowSaturatedHost {
			recorder.RecordLoadOverride(true)
		}
	}
	self := runqueue.Participant{PID: os.Getpid(), Summary: runQueueSummary(args), Worktree: cwd}
	queueProgress := newRunQueueProgressWithHeartbeat(cmd.ErrOrStderr(), !quiet, configPath, runQueueHeartbeat())
	lease, units, waited, leaseErr := admitWithQueueVisibility(cmd.Context(), projectsRoot, args, self, queueProgress)
	admittedAt := time.Now()
	recorder.RecordAdmission(units, waited)
	if leaseErr == nil {
		recorder.RecordQueueAdmittedAt(admittedAt)
	}
	if leaseErr != nil {
		_ = recorder.Finish(exitFindings, 0, 0, time.Now())
		return fmt.Errorf("wait for WB CPU capacity: %w", leaseErr)
	}
	defer lease.Release()

	interactive := console.Interactive(cmd.ErrOrStderr(), false)
	child := process.CommandContextInteractive(cmd.Context(), interactive, args[0], args[1:]...)
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	child.Env = governedEnvironment(os.Environ(), recorder.OperationID, args, units)

	if err = child.Start(); err == nil {
		done := make(chan struct{})
		// progressDone closes only once the goroutine below (when started)
		// has fully returned — not merely once `done` is closed, which a
		// select can race against an already-dispatched tick. Review
		// finding (PR #628 re-review round 4, Serious 1): this goroutine
		// used to call lease.Heartbeat() itself, racing Lease.Release's
		// own cleanup (a heartbeat write already in flight when the child
		// exited could still land after Release ran extraRelease — a
		// genuine data race on the legacy pool, and a resurrected "ghost"
		// heavy holder file, reproduced by the reviewer in 241 of 300
		// runs). The holder record is refreshed by the Lease's own
		// background self-heartbeat (runqueue.Lease.armHeartbeat, started
		// automatically at admission) regardless of whether this goroutine
		// runs at all, so it must never touch lease itself — it is now a
		// courtesy print only, and joined before Release runs regardless,
		// so no write to cmd.ErrOrStderr() can outlive this function's own
		// use of it either.
		progressDone := make(chan struct{})
		if interactive {
			go func() {
				defer close(progressDone)
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-ticker.C:
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wb: command still running: %s\n", strings.Join(args, " "))
					}
				}
			}()
		} else {
			close(progressDone)
		}
		err = child.Wait()
		close(done)
		<-progressDone
	}
	exitCode := 0
	if err != nil {
		exitCode = exitFindings
		var childExit *exec.ExitError
		if errors.As(err, &childExit) {
			exitCode = childExit.ExitCode()
		}
	}
	if units > 0 {
		queueProgress.done(time.Since(commandStarted), exitCode)
	}
	var userCPU, systemCPU time.Duration
	if child.ProcessState != nil {
		userCPU = child.ProcessState.UserTime()
		systemCPU = child.ProcessState.SystemTime()
	}
	if finishErr := recorder.Finish(exitCode, userCPU, systemCPU, time.Now()); finishErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: command telemetry finish failed: %v\n", finishErr)
	}
	if err == nil {
		return nil
	}
	var childExit *exec.ExitError
	if errors.As(err, &childExit) {
		return &exitError{
			code:    childExit.ExitCode(),
			message: fmt.Sprintf("%s exited with status %d", args[0], childExit.ExitCode()),
		}
	}
	return fmt.Errorf("execute %s: %w", args[0], err)
}

// runQueueHeartbeatOverride lets tests shrink `wb run`'s CPU-queue heartbeat
// cadence below the universal 10s default (universalProgressHeartbeat), the
// same way other progress types here expose a *WithHeartbeat constructor.
// Production code never sets it; tests restore it to zero when done.
var runQueueHeartbeatOverride time.Duration

func runQueueHeartbeat() time.Duration {
	if runQueueHeartbeatOverride > 0 {
		return runQueueHeartbeatOverride
	}
	return universalProgressHeartbeat
}

// queueAdmissionGrace is how long admitWithQueueVisibility waits before
// deciding a command must announce itself as queued rather than admitted
// immediately. Most `wb run --` invocations find the CPU budget free; this
// grace period keeps the common case silent (a single "admitted (queue
// empty)" line) instead of always printing a queued line the caller barely
// has time to read.
const queueAdmissionGrace = 200 * time.Millisecond

// admitWithQueueVisibility wraps runqueue.Admit with the human-readable
// receipts described in cmd/wb/run_queue_progress.go: an immediate
// admitted/queued line, a heartbeat at most every progress.heartbeat while
// still queued, and the caller prints the admitted-after-wait line itself
// once this returns. A command runqueue.Classify finds ungoverned
// (KindNone) skips every line — nothing was queued.
func admitWithQueueVisibility(ctx context.Context, projectsRoot string, argv []string, self runqueue.Participant, progress *runQueueProgress) (*runqueue.Lease, int, time.Duration, error) {
	ticket := runqueue.RegisterForAdmission(projectsRoot, argv, self)
	defer ticket.Forget()

	if runqueue.Classify(argv) == runqueue.KindNone {
		admission, err := runqueue.Admit(ctx, projectsRoot, argv, self, ticket)
		return admission.Lease, admission.Units, admission.Waited, err
	}

	type admitResult struct {
		admission runqueue.Admission
		err       error
	}
	resultCh := make(chan admitResult, 1)
	go func() {
		admission, err := runqueue.Admit(ctx, projectsRoot, argv, self, ticket)
		resultCh <- admitResult{admission: admission, err: err}
	}()

	select {
	case result := <-resultCh:
		progress.admittedImmediately()
		return result.admission.Lease, result.admission.Units, result.admission.Waited, result.err
	case <-time.After(queueAdmissionGrace):
	}

	state := ticket.Snapshot(runqueue.Budget())
	select {
	case result := <-resultCh:
		progress.admittedImmediately()
		return result.admission.Lease, result.admission.Units, result.admission.Waited, result.err
	default:
	}
	if queueStateHasAdmittedSelf(state, self) {
		// Adaptive admission records its holder before it removes its waiting
		// ticket. A self holder with no ticket therefore proves admission won;
		// only delivery to resultCh remains. Do not emit the contradictory
		// "queued ... position 0 of 0, waiting on: self" receipt seen when the
		// scheduler ran the grace timer before it scheduled this goroutine.
		result := <-resultCh
		progress.admittedImmediately()
		return result.admission.Lease, result.admission.Units, result.admission.Waited, result.err
	}

	queuedAt := time.Now()
	progress.queued(self.Summary, state)

	ticker := time.NewTicker(progress.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			if result.err == nil {
				progress.admittedAfterWait(time.Since(queuedAt))
			}
			return result.admission.Lease, result.admission.Units, result.admission.Waited, result.err
		case <-ticker.C:
			// Refresh the ticket alongside reporting on it, so a still-waiting
			// command's registration never ages past staleAfter and gets
			// reaped by another WB process as if this one had died.
			ticket.Heartbeat()
			progress.heartbeat(time.Since(queuedAt), ticket.Snapshot(runqueue.Budget()))
		}
	}
}

// queueStateHasAdmittedSelf identifies the adaptive pool's brief handoff
// window after it has replaced this caller's waiting ticket with its holder,
// but before admitWithQueueVisibility receives the buffered result. A missing
// ticket alone is only best-effort visibility and is deliberately insufficient.
func queueStateHasAdmittedSelf(state runqueue.State, self runqueue.Participant) bool {
	if state.Total != 0 {
		return false
	}
	for _, holder := range state.Holders {
		if holder.PID == self.PID && holder.Summary == self.Summary && holder.Worktree == self.Worktree {
			return true
		}
	}
	return false
}

// runQueueSummary is the short, privacy-safe label runqueue.Participant
// carries into queue-visibility receipts and `wb run --queue` — the program
// name and its verb (e.g. "go test"), never full arguments, flags, or paths.
// It mirrors runlog's own privacy-safe-telemetry contract even though these
// records are transient rather than durable.
func runQueueSummary(args []string) string {
	if len(args) == 0 {
		return "unknown"
	}
	base := filepath.Base(args[0])
	for _, argument := range args[1:] {
		if strings.HasPrefix(argument, "-") {
			continue
		}
		return base + " " + argument
	}
	return base
}

func printRunQueue(cmd *cobra.Command, jsonOut bool) error {
	listing := runqueue.ListQueue(projectsRoot, runqueue.Budget())
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(listing)
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "WB CPU queue · budget %d · heavy k %d\n", listing.Budget, listing.HeavyK); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "running (%d):\n", len(listing.Running)); err != nil {
		return err
	}
	for _, entry := range listing.Running {
		if _, err := fmt.Fprintf(out, "  pid %-8d %-16s %-8s units %-3d %s\n", entry.PID, entry.Summary, entry.Age.Round(time.Second), entry.Units, entry.Worktree); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "waiting (%d):\n", len(listing.Waiting)); err != nil {
		return err
	}
	for _, entry := range listing.Waiting {
		if _, err := fmt.Fprintf(out, "  pid %-8d %-16s %-8s %s\n", entry.PID, entry.Summary, entry.Age.Round(time.Second), entry.Worktree); err != nil {
			return err
		}
	}
	return nil
}

func governedEnvironment(environment []string, operationID string, args []string, units int) []string {
	environment = withEnvironmentValue(environment, runlog.OperationIDEnv, operationID)
	if units <= 0 {
		return environment
	}
	environment = withEnvironmentValue(environment, "WB_CPU_UNITS", fmt.Sprint(units))
	environment = withEnvironmentValue(environment, "GOMAXPROCS", runqueue.GovernGOMAXPROCS(runqueue.LookupEnv(environment, "GOMAXPROCS"), units))
	environment = withEnvironmentValue(environment, "NX_PARALLEL", fmt.Sprint(units))
	if goFlags := runqueue.GovernGoFlags(args, runqueue.EffectiveGOFLAGS(), units); goFlags != "" {
		environment = withEnvironmentValue(environment, "GOFLAGS", goFlags)
	}
	return environment
}

func withEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func runRun(projectsRoot, filter string, extraOrgs []string, configPath, name string, list, apply bool) int {
	if configPath == "" {
		configPath = wbconfig.DefaultPath()
	}
	cfg, err := recipe.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if list || name == "" {
		names := make([]string, 0, len(cfg.Recipes))
		for n := range cfg.Recipes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}
		return 0
	}

	r, ok := cfg.Recipes[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown recipe %q (see `wb run --list`)\n", name)
		return 1
	}

	repos, err := fleet(projectsRoot, filter, func() []string { return fleetOwners(extraOrgs) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery error:", err)
		return 1
	}
	if !apply {
		fmt.Fprintln(os.Stderr, "dry-run: reporting only; pass --apply to commit & push")
	}

	var rep report
	drift := false
	for _, repoItem := range repos {
		if repoItem.Archived {
			rep.record(&rep.archived, "▪", repoItem.Slug())
			continue
		}
		if !repoItem.Remote {
			rep.record(&rep.skipped, "–", repoItem.Slug()+" — local-only (not under your GitHub orgs)")
			continue
		}
		if repoItem.IsFork {
			rep.record(&rep.forked, "⑂", repoItem.Slug())
			continue
		}
		if repoItem.Path == "" {
			rep.record(&rep.skipped, "–", repoItem.Slug()+" — remote-only (clone to evaluate)")
			continue
		}
		applies, err := r.AppliesTo(repoItem.Path)
		if err != nil {
			rep.record(&rep.errors, "✗", repoItem.Slug()+" — "+err.Error())
			continue
		}
		if !applies {
			rep.record(&rep.skipped, "–", repoItem.Slug()+" — recipe does not apply")
			continue
		}
		if !apply {
			preview, err := recipe.Evaluate(r, repoItem.Path)
			switch {
			case err != nil:
				rep.record(&rep.errors, "✗", repoItem.Slug()+" — "+err.Error())
			case !preview.Changed:
				rep.record(&rep.skipped, "–", repoItem.Slug()+" — "+preview.Summary)
			default:
				drift = true
				rep.record(&rep.updated, "✓", repoItem.Slug()+" — would "+preview.Summary)
			}
			continue
		}
		if err := applyRecipe(r, repoItem, &rep); err != nil {
			rep.record(&rep.errors, "✗", repoItem.Slug()+" — "+err.Error())
		}
	}
	rep.print()
	if len(rep.errors) > 0 || (!apply && drift) {
		return 1
	}
	return 0
}

func applyRecipe(r recipe.Recipe, repoItem discover.Repo, rep *report) error {
	def, err := gitops.DefaultBranch(repoItem.Path)
	if err != nil {
		if ferr := gitops.Fetch(repoItem.Path); ferr != nil {
			return ferr
		}
		if def, err = gitops.DefaultBranch(repoItem.Path); err != nil {
			return err
		}
	}
	outcome, err := recipe.Land(r, repoItem.Path, def)
	if err != nil {
		return err
	}
	if !outcome.Changed {
		rep.record(&rep.skipped, "–", repoItem.Slug()+" — "+outcome.Detail)
		return nil
	}
	rep.record(&rep.updated, "✓", repoItem.Slug()+" — "+outcome.Detail)
	return nil
}
