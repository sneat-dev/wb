package cmdagent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// runFlags is the whole flag surface of `wb agent run`. It is a struct so the
// task-type router and the compatibility flags cannot drift into two parsers.
type runFlags struct {
	taskType       string
	taskComplexity string
	effort         string
	profile        string
	harness        string
	model          string
	provider       string
	variant        string

	worktree       string
	operation      string
	newWorktree    string
	useWorktree    string
	allowCanonical bool
	allowDirty     bool

	task     string
	taskFile string
	repo     string
	branch   string
	base     string

	validate   []string
	noValidate bool
	timeout    time.Duration

	wait        bool
	waitTimeout time.Duration

	dryRun  bool
	jsonOut bool
}

// NewRun builds `wb agent run`: the task-type and profile aware entry point.
func NewRun(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	flags := &runFlags{}
	command := &cobra.Command{
		Use:   "run",
		Short: "Resolve a task type and profile, then run one harness in a worktree",
		Long: `Run one bounded task with the configuration a task type resolves to.

A task type is a named policy in wb.yaml: a default profile, a profile per
complexity, a default worktree mode, a default timeout, and the validation
suites an execution must pass. A profile is the execution configuration itself:
harness, model, optional variant, optional effort, optional timeout.

If no profile is named, WB resolves one in this order: the task type's
complexity map, the task type's profile, then agents.defaults.profile. WB ships
no model identifier of its own.

Complexity and effort are different. --task-complexity describes the work and
chooses what runs. --effort asks the selected model to think harder. A harness
that cannot apply an effort refuses an explicit --effort and records an
inherited one as not applied.

The command returns as soon as the detached worker owner starts. Use
` + "`wb agent await <id>`" + ` or pass --wait to block for the result.`,
		Example: `# The configured coding policy, worktree chosen by the task type
wb agent run --task-type coding --task-file task.md

# Harder work: deterministic routing to the task type's "high" profile
wb agent run --task-type coding --task-complexity high --task "add pagination and tests"

# See exactly what would run, without creating anything
wb agent run --task-type coding --dry-run --task-file task.md

# Run in the checkout you are already in, explicitly allowing existing work
wb agent run --task-type debugging --worktree current --allow-dirty --task-file bug.md`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			text, err := readAgentTask(runtime, deps, command, flags.task, flags.taskFile)
			if err != nil {
				return err
			}
			mode, worktree, err := resolveRunWorktree(runtime, flags)
			if err != nil {
				return err
			}
			plan := agents.RunRequest{
				Task: text, TaskType: flags.taskType, Complexity: flags.taskComplexity,
				Effort: flags.effort, Profile: flags.profile, Harness: flags.harness,
				Model: flags.model, Provider: flags.provider, Variant: flags.variant,
				WorktreeMode: mode, Worktree: worktree,
				AllowCanonical: flags.allowCanonical, AllowDirty: flags.allowDirty,
				Timeout: flags.timeout, TimeoutSet: command.Flags().Changed("timeout"),
				Validate: flags.validate, ValidateSet: command.Flags().Changed("validate"),
				NoValidate: flags.noValidate,
				Repository: flags.repo, Base: flags.base, Branch: flags.branch,
			}
			if flags.dryRun {
				return renderDryRun(runtime, deps, command, flags.jsonOut, plan)
			}
			request := agentrun.RunRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, Stderr: command.ErrOrStderr(), Request: plan,
			}
			if flags.wait {
				// A waiting run is the only one that can observe an infrastructure
				// failure and continue down the task type's fallback list.
				request.Wait = flags.waitTimeout
			}
			result, err := deps.Run(command.Context(), request)
			if err != nil {
				return agentRequestError(runtime, err)
			}
			if flags.wait {
				if flags.jsonOut {
					return writeAgentJSON(command.OutOrStdout(), result)
				}
				return finishAgentAwait(runtime, deps, command, false, result)
			}
			return renderAgentResult(command, flags.jsonOut, result)
		},
	}
	deps.Discovery(command, "delegate offload run task type profile complexity effort route harness opencode dsh codex worktree auto current dry-run background worker subagent")
	command.Flags().StringVar(&flags.taskType, "task-type", "", "task type whose policy resolves the profile and defaults (default: agents.defaults.task_type, else coding)")
	command.Flags().StringVar(&flags.taskComplexity, "task-complexity", "", "work difficulty: trivial, low, medium, high or extreme; selects the profile deterministically")
	command.Flags().StringVar(&flags.effort, "effort", "", "reasoning effort for the selected model: low, medium, high or xhigh")
	command.Flags().StringVar(&flags.profile, "profile", "", "explicit execution profile; overrides task-type routing")
	command.Flags().StringVar(&flags.harness, "harness", "", "explicit harness with --model: codex, opencode or dsh")
	command.Flags().StringVar(&flags.model, "model", "", "explicit model identifier with --harness; passed to the harness verbatim")
	command.Flags().StringVar(&flags.provider, "provider", "", "optional provider name for an explicit harness/model")
	command.Flags().StringVar(&flags.variant, "variant", "", "optional opencode model variant")
	command.Flags().StringVar(&flags.worktree, "worktree", "", "auto (new isolated checkout), current (this checkout), or an absolute worktree path")
	command.Flags().StringVar(&flags.operation, "operation", "", "worktree name for --worktree auto (default: <task-type>-<task hash>-<run id>)")
	command.Flags().StringVar(&flags.newWorktree, "new-worktree", "", "create a new WB-managed worktree with this name and run there")
	command.Flags().StringVar(&flags.useWorktree, "use-worktree", "", "run inside this existing WB-managed worktree")
	command.Flags().BoolVar(&flags.allowCanonical, "allow-canonical", false, "permit a writable run inside a canonical clone (refused by default)")
	command.Flags().BoolVar(&flags.allowDirty, "allow-dirty", false, "permit a run beside existing uncommitted work (WB never cleans it)")
	command.Flags().StringVar(&flags.task, "task", "", "the bounded task, inline")
	command.Flags().StringVar(&flags.taskFile, "task-file", "", "read the bounded task from this file, or - for stdin")
	command.Flags().StringVar(&flags.repo, "repo", "", "owner/repository to operate on (default: derived from the current checkout)")
	command.Flags().StringVar(&flags.branch, "branch", "", "optional exact feature branch for a created worktree")
	command.Flags().StringVar(&flags.base, "base", "", "optional base branch for a created worktree")
	command.Flags().StringArrayVar(&flags.validate, "validate", nil, "validation suite to run after the harness (repeatable)")
	command.Flags().BoolVar(&flags.noValidate, "no-validate", false, "skip the task type's validation suites")
	command.Flags().DurationVar(&flags.timeout, "timeout", agents.DefaultTimeout, "bound on the worker's run; on expiry WB terminates its process group")
	command.Flags().BoolVar(&flags.wait, "wait", false, "block until the execution reaches a terminal status")
	command.Flags().DurationVar(&flags.waitTimeout, "wait-timeout", time.Hour, "with --wait, give up after this long; zero waits without a bound")
	command.Flags().BoolVar(&flags.dryRun, "dry-run", false, "print the fully resolved plan and start nothing")
	shared.AddJSONFormatFlags(command, &flags.jsonOut)
	return command
}

// resolveRunWorktree turns the three worktree spellings into one mode and
// target. A bare name is refused in --worktree so a worktree called "auto" can
// never be ambiguous.
func resolveRunWorktree(runtime shared.Runtime, flags *runFlags) (string, string, error) {
	chosen := 0
	for _, value := range []string{flags.worktree, flags.newWorktree, flags.useWorktree} {
		if strings.TrimSpace(value) != "" {
			chosen++
		}
	}
	if chosen > 1 {
		return "", "", runtime.ExitError(shared.ExitUsage, "--worktree, --new-worktree and --use-worktree are mutually exclusive")
	}
	switch {
	case strings.TrimSpace(flags.newWorktree) != "":
		return agents.ModeNew, strings.TrimSpace(flags.newWorktree), nil
	case strings.TrimSpace(flags.useWorktree) != "":
		return agents.ModeExisting, strings.TrimSpace(flags.useWorktree), nil
	case strings.TrimSpace(flags.worktree) == "":
		return "", strings.TrimSpace(flags.operation), nil
	}
	value := strings.TrimSpace(flags.worktree)
	switch value {
	case agents.ModeAuto:
		return agents.ModeAuto, strings.TrimSpace(flags.operation), nil
	case agents.ModeCurrent:
		return agents.ModeCurrent, "", nil
	}
	if !filepath.IsAbs(value) {
		return "", "", runtime.ExitError(shared.ExitUsage, fmt.Sprintf("--worktree %q must be %q, %q or an absolute path; a bare name belongs to --use-worktree", value, agents.ModeAuto, agents.ModeCurrent))
	}
	return agents.ModePath, value, nil
}

// renderDryRun prints the resolved plan. It never probes a harness, so it
// starts no process at all.
func renderDryRun(runtime shared.Runtime, deps Dependencies, command *cobra.Command, jsonOut bool, request agents.RunRequest) error {
	plan, err := deps.Plan(command.Context(), agentrun.PlanRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Request: request})
	if err != nil {
		return agentRequestError(runtime, err)
	}
	document := dryRunDocument{Plan: plan, TaskSHA256: "", TaskBytes: 0}
	document.TaskSHA256, document.TaskBytes = agents.TaskIdentity(request.Task)
	if jsonOut {
		return writeAgentJSON(command.OutOrStdout(), document)
	}
	lines := []string{
		"task type:  " + plan.TaskType + " (" + plan.TaskTypeSource + ")",
		"profile:    " + plan.Profile.Profile + " (" + plan.ProfileSource + ")",
		"harness:    " + plan.Profile.Harness,
		"provider:   " + emptyWord(plan.Profile.Provider, "(harness default)"),
		"model:      " + plan.Profile.Model,
		"complexity: " + emptyWord(plan.Complexity, "(none)") + " (" + plan.ComplexitySource + ")",
		"effort:     " + effortPlanWord(plan),
		"worktree:   " + plan.WorktreeMode + " (" + plan.WorktreeSource + ")",
		"timeout:    " + (time.Duration(plan.TimeoutMS) * time.Millisecond).String() + " (" + plan.TimeoutSource + ")",
		"validation: " + validationPlanWord(plan),
		"acceptance: " + acceptanceWord(plan.AcceptanceRequired),
		"task:       " + fmt.Sprintf("%d bytes, sha256 %s", document.TaskBytes, document.TaskSHA256),
		"harness version: not probed by --dry-run",
	}
	if plan.FallbackFrom != "" {
		lines = append(lines, "fallback:   from "+plan.FallbackFrom)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(command.OutOrStdout(), line); err != nil {
			return err
		}
	}
	return nil
}

// dryRunDocument is the machine-readable dry-run plan.
type dryRunDocument struct {
	Plan       agents.Plan `json:"plan"`
	TaskSHA256 string      `json:"task_sha256"`
	TaskBytes  int         `json:"task_bytes"`
	Executed   bool        `json:"executed"`
}

func effortPlanWord(plan agents.Plan) string {
	if plan.EffortRequested == "" {
		return "(none)"
	}
	word := plan.EffortRequested + " (" + plan.EffortSource + ")"
	if plan.EffortEffective == "" {
		return word + ", not applied: " + plan.EffortNote
	}
	if plan.EffortVariant != "" {
		word += " as variant " + plan.EffortVariant
	}
	return word
}

func validationPlanWord(plan agents.Plan) string {
	if len(plan.ValidationSteps) == 0 {
		return "none (" + plan.ValidationSource + ")"
	}
	names := make([]string, 0, len(plan.ValidationSteps))
	for _, step := range plan.ValidationSteps {
		names = append(names, step.Name)
	}
	return strings.Join(names, ", ") + " (" + plan.ValidationSource + ")"
}

func acceptanceWord(required bool) string {
	if required {
		return "required"
	}
	return "optional"
}

// agentRequestError maps a refused invocation to the usage exit code.
func agentRequestError(runtime shared.Runtime, err error) error {
	if agents.IsRequestError(err) {
		return runtime.ExitError(shared.ExitUsage, err.Error())
	}
	return err
}
