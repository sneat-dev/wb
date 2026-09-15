package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func newAgentCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "Dispatch a bounded coding task to a configured agent harness in an isolated worktree",
		Long: `Dispatch one bounded coding task to one configured agent harness running
inside an isolated WB worktree, then inspect it later by its agent run ID.

WB is the deterministic execution layer here. It resolves a profile, creates or
resolves the worktree through the same code ` + "`wb worktree create`" + ` uses, launches the
harness as a detached child, and reports execution facts: state, exit status,
resolved configuration, worktree, branch, changed files, token usage, and the
log location. WB never judges whether the resulting diff is correct — that is
the caller's decision, not WB's.

An agent profile is a named execution configuration in wb.yaml:

  agents:
    profiles:
      cheap-coder:
        harness: codex
        provider: deepseek
        model: deepseek-flash
        reasoning: high

Provider routing (base URL, credential environment variable, wire protocol)
comes from a small built-in registry a project may override under
` + "`agents.providers`" + `. Credentials are read from the environment and are never
stored in configuration, passed in an argument list, or written to a run
record.`,
	}
	command.AddCommand(newAgentDispatchCmd())
	command.AddCommand(newAgentStatusCmd())
	command.AddCommand(newAgentAwaitCmd())
	command.AddCommand(newAgentListCmd())
	command.AddCommand(newAgentLogsCmd())
	command.AddCommand(newAgentStopCmd())
	return command
}

// agentConfigPath is WB's existing user configuration file. Profiles live
// there rather than in a second configuration hierarchy.
func agentConfigPath() string { return wbconfig.DefaultPath() }

func loadAgentConfig() (agents.Config, error) {
	config, err := agents.LoadConfigFile(agentConfigPath())
	if err != nil {
		return agents.Config{}, err
	}
	return config, nil
}

func agentStoreForRead() (agents.Store, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return agents.Store{}, err
	}
	return agents.NewStore(home), nil
}

func agentHomeForWrite() (string, error) {
	return wbhome.EnsureRoot(projectsRoot)
}

// loadAgentResult reads one run and renders it, mapping a lookup miss onto the
// findings exit code: the invocation was accepted and then found nothing,
// which is not the same as a mistyped command.
func loadAgentResult(agentID string) (agents.Store, agents.Record, agents.Result, error) {
	store, err := agentStoreForRead()
	if err != nil {
		return agents.Store{}, agents.Record{}, agents.Result{}, err
	}
	record, err := store.Load(agentID)
	if err != nil {
		return agents.Store{}, agents.Record{}, agents.Result{}, err
	}
	return store, record, store.Render(record), nil
}

func writeAgentJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// ------------------------------------------------------------------ dispatch

func newAgentDispatchCmd() *cobra.Command {
	var newWorktree, useWorktree, profile, task, taskFile, repository, branch, base string
	var jsonOut bool
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "dispatch",
		Short: "Start one configured agent harness on one bounded task now",
		Long: `Start execution now in a new or existing WB worktree.

Exactly one worktree mode is required. Creating a new isolated workspace and
modifying an existing one have materially different intent, so they stay two
explicit options:

  --new-worktree <name>   create the checkout through ` + "`wb worktree create`" + `'s own code
  --use-worktree <name>   resolve an existing WB-managed worktree; never create one

Exactly one task source is required: --task, or --task-file (with ` + "`-`" + ` for
stdin). Task bytes are delivered to the harness on standard input, so they never
appear in the process table.`,
		Example: `# Start a bounded task in a fresh isolated worktree
wb agent dispatch \
  --new-worktree cg-symbol-api \
  --profile cheap-coder \
  --task "Implement the requested symbol API and run the relevant tests"

# Continue in the worktree the first worker produced
wb agent dispatch \
  --use-worktree cg-symbol-api \
  --profile reviewer \
  --task-file /tmp/review-task.md`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			mode, name, err := selectWorktreeMode(command, newWorktree, useWorktree)
			if err != nil {
				return err
			}
			text, err := readAgentTask(command, task, taskFile)
			if err != nil {
				return err
			}
			home, err := agentHomeForWrite()
			if err != nil {
				return err
			}
			store := agents.NewStore(home)
			deps := agents.DispatchDeps{
				ConfigPath:   agentConfigPath(),
				LoadConfig:   loadAgentConfig,
				ProjectsRoot: projectsRoot,
				Home:         home,
				BeforeCreate: refreshManagedHooksBeforeWorktreeCreate,
				AfterCreate: func(repositories []string, results []worktrees.CreateResult) {
					markCreatedCheckouts(command, base, results)
				},
				SpawnOwner: func(agentID string) (int, error) {
					return agents.SpawnOwner(store.Dir(agentID), os.Executable)
				},
				Now: time.Now,
			}
			record, err := agents.Dispatch(command.Context(), agents.DispatchRequest{
				Mode: mode, Worktree: name, Profile: profile, Task: text,
				Repository: repository, Branch: branch, Base: base, Timeout: timeout,
			}, deps)
			if err != nil {
				if agents.IsRequestError(err) {
					return usageError(err.Error())
				}
				return err
			}
			return renderAgentResult(command, jsonOut, store, record)
		},
	}
	setDiscoveryTerms(command, "offload delegate dispatch start worker agent profile worktree isolated subagent codex deepseek parallel background task")
	command.Flags().StringVar(&newWorktree, "new-worktree", "", "create a new WB-managed worktree with this name and run the worker inside it")
	command.Flags().StringVar(&useWorktree, "use-worktree", "", "run the worker inside this existing WB-managed worktree")
	command.Flags().StringVar(&profile, "profile", "", "required agent profile name from the agents.profiles section of wb.yaml")
	command.Flags().StringVar(&task, "task", "", "the bounded task, inline")
	command.Flags().StringVar(&taskFile, "task-file", "", "read the bounded task from this file, or - for stdin")
	command.Flags().StringVar(&repository, "repo", "", "owner/repository to operate on (default: derived from the current checkout's origin)")
	command.Flags().StringVar(&branch, "branch", "", "optional exact feature branch, passed through to worktree creation policy")
	command.Flags().StringVar(&base, "base", "", "optional base branch, passed through to worktree creation policy")
	command.Flags().DurationVar(&timeout, "timeout", agents.DefaultTimeout, "bound on the worker's run; on expiry WB terminates its process group")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func selectWorktreeMode(command *cobra.Command, newWorktree, useWorktree string) (string, string, error) {
	created := strings.TrimSpace(newWorktree)
	existing := strings.TrimSpace(useWorktree)
	switch {
	case command.Flags().Changed("new-worktree") && command.Flags().Changed("use-worktree"):
		return "", "", usageError("--new-worktree and --use-worktree are mutually exclusive; pick the one that matches your intent")
	case created != "" && existing != "":
		return "", "", usageError("--new-worktree and --use-worktree are mutually exclusive; pick the one that matches your intent")
	case created != "":
		return agents.ModeNew, created, nil
	case existing != "":
		return agents.ModeExisting, existing, nil
	default:
		return "", "", usageError("exactly one of --new-worktree or --use-worktree is required")
	}
}

func readAgentTask(command *cobra.Command, task, taskFile string) (string, error) {
	path := strings.TrimSpace(taskFile)
	if strings.TrimSpace(task) != "" && path != "" {
		return "", usageError("--task and --task-file are mutually exclusive")
	}
	switch {
	case strings.TrimSpace(task) != "":
		return task, nil
	case path == "-":
		raw, err := io.ReadAll(command.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read --task-file - from stdin: %w", err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			return "", usageError("--task-file - requires a non-empty task on stdin")
		}
		return string(raw), nil
	case path != "":
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --task-file %s: %w", path, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			return "", fmt.Errorf("--task-file %s is empty", path)
		}
		return string(raw), nil
	default:
		return "", usageError("one of --task or --task-file is required")
	}
}

func renderAgentResult(command *cobra.Command, jsonOut bool, store agents.Store, record agents.Record) error {
	result := store.Render(record)
	if jsonOut {
		return writeAgentJSON(command.OutOrStdout(), result)
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s (%s/%s)\n",
		result.AgentID, result.State, result.Worktree, result.Resolved.Provider, result.Resolved.Model)
	return err
}

// -------------------------------------------------------------------- status

func newAgentStatusCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "status <agent-id>",
		Short: "Report the execution facts of one dispatched agent run",
		Long: `Report what WB observed: state, resolved configuration, worktree, process
status, timestamps, exit status, and the log location.

A run whose owner vanished without recording a terminal state is reported as
` + "`abandoned`" + ` rather than as ` + "`running`" + ` or ` + "`completed`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			_, _, result, err := loadAgentResult(args[0])
			if err != nil {
				if agents.IsUnknownAgent(err) {
					return &exitError{code: exitFindings, message: err.Error()}
				}
				return err
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), result)
			}
			return printAgentStatusText(command.OutOrStdout(), result)
		},
	}
	setDiscoveryTerms(command, "check inspect state agent run progress exit code logs status")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func printAgentStatusText(writer io.Writer, result agents.Result) error {
	lines := []string{
		"agent:    " + result.AgentID,
		"state:    " + string(result.State),
		"profile:  " + result.RequestedProfile,
		fmt.Sprintf("resolved: harness=%s provider=%s model=%s", result.Resolved.Harness, result.Resolved.Provider, result.Resolved.Model),
		"worktree: " + result.Worktree + " (" + result.WorktreeMode + ") " + result.WorktreeDir,
		"branch:   " + result.Branch,
		"base:     " + strings.TrimSpace(result.Base+" "+result.BaseSHA),
		"started:  " + result.StartedAt.Local().Format(time.RFC3339),
	}
	if result.Resolved.Reasoning != "" {
		lines[3] += " reasoning=" + result.Resolved.Reasoning
	}
	if result.FinishedAt != nil {
		lines = append(lines, "finished: "+result.FinishedAt.Local().Format(time.RFC3339),
			fmt.Sprintf("duration: %s", (time.Duration(result.DurationMS)*time.Millisecond).Round(time.Millisecond)))
	}
	if result.ExitCode != nil {
		lines = append(lines, fmt.Sprintf("exit:     %d", *result.ExitCode))
	}
	if result.Failure != "" {
		lines = append(lines, "failure:  "+result.Failure)
	}
	if result.Changes != nil {
		lines = append(lines, fmt.Sprintf("changes:  %d file(s), +%d/-%d, %d commit(s)",
			result.Changes.FilesChanged, result.Changes.Insertions, result.Changes.Deletions, result.Changes.Commits))
	}
	if result.Usage != nil {
		lines = append(lines, fmt.Sprintf("usage:    in=%d cached=%d out=%d reasoning=%d, %d tool call(s)",
			result.Usage.InputTokens, result.Usage.CachedInputTokens, result.Usage.OutputTokens,
			result.Usage.ReasoningOutputTokens, result.ToolCalls))
	}
	if result.LogPath != "" {
		lines = append(lines, "log:      "+result.LogPath)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(writer, strings.TrimRight(line, " ")); err != nil {
			return err
		}
	}
	return nil
}

// --------------------------------------------------------------------- await

func newAgentAwaitCmd() *cobra.Command {
	var jsonOut bool
	var waitTimeout time.Duration
	command := &cobra.Command{
		Use:   "await <agent-id>",
		Short: "Block efficiently until a dispatched agent run reaches a terminal state",
		Long: `Block until the run finishes, then report its result.

This exists so a supervisor can wait for a worker without polling
` + "`wb agent status`" + ` in a loop and without holding the worker's transcript in its
own context. Zero means wait without a bound.

When the wait bound elapses first, the command reports the still-non-terminal
state truthfully and never claims success.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := agentStoreForRead()
			if err != nil {
				return err
			}
			deadline := time.Time{}
			if waitTimeout > 0 {
				deadline = time.Now().Add(waitTimeout)
			}
			result, err := awaitAgentRun(command.Context(), store, args[0], deadline)
			if err != nil {
				if agents.IsUnknownAgent(err) {
					return &exitError{code: exitFindings, message: err.Error()}
				}
				return err
			}
			if jsonOut {
				if err := writeAgentJSON(command.OutOrStdout(), result); err != nil {
					return err
				}
			} else if err := printAgentAwaitText(command.OutOrStdout(), result); err != nil {
				return err
			}
			if !result.Terminal {
				// The run was still going when the wait bound elapsed. Exiting
				// zero here would let a script read "awaited successfully" as
				// "the work succeeded", which is exactly the confusion await
				// exists to remove.
				return &exitError{code: exitFindings, message: fmt.Sprintf("agent run %s is still %s after the wait bound; it has not finished", result.AgentID, result.State)}
			}
			return nil
		},
	}
	setDiscoveryTerms(command, "wait block wait-for finish poll sleep agent run offload supervisor")
	command.Flags().DurationVar(&waitTimeout, "wait-timeout", time.Hour, "give up waiting after this long; zero waits without a bound")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

// awaitAgentRun waits for a terminal state without busy-spinning. It polls the
// durable record on a fixed interval rather than watching a process, because
// the record is the contract: it survives the dispatcher, the owner, and this
// process, and it is what a later reader will see anyway.
func awaitAgentRun(ctx context.Context, store agents.Store, agentID string, deadline time.Time) (agents.Result, error) {
	const interval = 250 * time.Millisecond
	for {
		record, err := store.Load(agentID)
		if err != nil {
			return agents.Result{}, err
		}
		result := store.Render(record)
		if result.Terminal {
			return result, nil
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func printAgentAwaitText(writer io.Writer, result agents.Result) error {
	if err := printAgentStatusText(writer, result); err != nil {
		return err
	}
	if result.Changes != nil && len(result.Changes.Files) > 0 {
		if _, err := fmt.Fprintln(writer, "changed:  "+strings.Join(result.Changes.Files, ", ")); err != nil {
			return err
		}
	}
	if result.Result != "" {
		if _, err := fmt.Fprintf(writer, "result:   %s\n", indentContinuation(result.Result)); err != nil {
			return err
		}
	}
	if !result.Terminal {
		if _, err := fmt.Fprintln(writer, "note:     the wait bound elapsed before the run finished; this is not a success"); err != nil {
			return err
		}
	}
	return nil
}

func indentContinuation(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "\n", "\n          ")
}

// ---------------------------------------------------------------------- list

func newAgentListCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List dispatched agent runs, newest first",
		Long: `List every dispatched run WB still has a record for. This is inventory,
not garbage collection: a run's worktree is the artefact and WB never removes
it on the run's behalf.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			store, err := agentStoreForRead()
			if err != nil {
				return err
			}
			records, err := store.List()
			if err != nil {
				return err
			}
			results := make([]agents.Result, 0, len(records))
			for _, record := range records {
				results = append(results, store.Render(record))
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), results)
			}
			if len(results) == 0 {
				_, err := fmt.Fprintln(command.OutOrStdout(), "no dispatched agent runs")
				return err
			}
			for _, result := range results {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\n",
					result.AgentID, result.State, result.RequestedProfile,
					result.Resolved.Model, result.Worktree,
					result.StartedAt.Local().Format("2006-01-02 15:04")); err != nil {
					return err
				}
			}
			return nil
		},
	}
	setDiscoveryTerms(command, "inventory runs history dispatched agents parallel background")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

// ---------------------------------------------------------------------- logs

func newAgentLogsCmd() *cobra.Command {
	var tail int
	var raw bool
	command := &cobra.Command{
		Use:   "logs <agent-id>",
		Short: "Locate a run's full worker transcript, or inspect a bounded slice of it",
		Long: `The complete worker transcript is preserved for debugging but never travels
back through status or await. By default this prints the log location and the
last few actions the worker took. Pass --raw to print the transcript itself, or
--tail to widen the action summary.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			_, record, _, err := loadAgentResult(args[0])
			if err != nil {
				if agents.IsUnknownAgent(err) {
					return &exitError{code: exitFindings, message: err.Error()}
				}
				return err
			}
			writer := command.OutOrStdout()
			if _, err := fmt.Fprintln(writer, record.LogPath); err != nil {
				return err
			}
			if !recordFileExists(record.LogPath) {
				_, err := fmt.Fprintln(writer, "(no worker transcript was captured)")
				return err
			}
			file, err := os.Open(record.LogPath)
			if err != nil {
				return fmt.Errorf("open run log: %w", err)
			}
			defer func() { _ = file.Close() }()
			if raw {
				_, err := io.Copy(writer, file)
				return err
			}
			if tail <= 0 {
				tail = 8
			}
			for _, action := range agents.RecentActions(file, tail) {
				if _, err := fmt.Fprintln(writer, "  "+action); err != nil {
					return err
				}
			}
			return nil
		},
	}
	setDiscoveryTerms(command, "transcript output stdout jsonl debug diagnose worker actions log")
	command.Flags().IntVar(&tail, "tail", 8, "how many recent worker actions to summarise")
	command.Flags().BoolVar(&raw, "raw", false, "print the complete worker transcript instead of a summary")
	return command
}

func recordFileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// ---------------------------------------------------------------------- stop

func newAgentStopCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "stop <agent-id>",
		Short: "Terminate a running worker's process group",
		Long: `Terminate the worker and everything it started.

The run owner is deliberately left alive so it observes the exit and records a
real terminal state; a stopped run therefore reports what happened instead of
drifting into "abandoned". The worktree and everything already written to it are
left untouched.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := agentStoreForRead()
			if err != nil {
				return err
			}
			record, err := agents.StopRun(store, args[0])
			if err != nil {
				if agents.IsUnknownAgent(err) {
					return &exitError{code: exitFindings, message: err.Error()}
				}
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "stopping %s worker process %d\n", record.AgentID, record.WorkerPID)
			return err
		},
	}
	setDiscoveryTerms(command, "kill cancel terminate abort worker process group runaway")
	return command
}
