package main

import (
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

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/recipe"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func newRunCmd() *cobra.Command {
	var (
		apply      bool
		configPath string
		days       int
		history    bool
		jsonOut    bool
		list       bool
	)
	cmd := &cobra.Command{
		Use:   "run [recipe] | run -- <command> [args...]",
		Short: "Run a fleet recipe or execute one command through WB",
		Long: `Run a configured fleet recipe, or use -- to execute one command through
WB while preserving its standard streams and exit code.

Recipe mode is a dry-run by default; --apply lands the recipe. Command mode is
synchronous, records privacy-safe receipts, and admits CPU-heavy work against a
machine-wide CPUCount-1 budget.`,
		Example: `# Discover configured recipes
wb run --list

# Preview, then apply one reusable fleet recipe
wb run refresh-ci
wb run refresh-ci --apply

# Run a command through WB
wb run -- go test ./internal/worktrees -run TestCreate

# Inspect command cost in this worktree
wb run --history --days 7`,
		Args: func(cmd *cobra.Command, args []string) error {
			if history {
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
			if history {
				if apply || configPath != "" || list {
					return usageError("--apply, --config, and --list cannot be used with --history")
				}
				return printRunHistory(cmd, days, jsonOut)
			}
			if cmd.ArgsLenAtDash() == 0 {
				if apply || configPath != "" || list || days != 14 || jsonOut {
					return usageError("--apply, --config, --days, --history, --json, and --list belong to WB modes and cannot be used with run --")
				}
				return runExternalCommand(cmd, args)
			}
			if days != 14 || jsonOut {
				return usageError("--days and --json require --history")
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
	cmd.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	cmd.Flags().IntVar(&days, "days", 14, "history window in calendar days")
	cmd.Flags().BoolVar(&history, "history", false, "summarize governed commands in the current worktree")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit history as JSON")
	cmd.Flags().BoolVar(&list, "list", false, "list configured recipes and exit")
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

func runExternalCommand(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	recorder, telemetryErr := runlog.Begin(cwd, args, time.Now())
	if telemetryErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: command telemetry start failed: %v\n", telemetryErr)
	}
	budget := runqueue.Budget()
	units := runqueue.Units(args, budget)
	lease, waited, leaseErr := runqueue.Acquire(cmd.Context(), projectsRoot, units, budget)
	recorder.RecordAdmission(units, waited)
	if leaseErr != nil {
		_ = recorder.Finish(exitFindings, 0, 0, time.Now())
		return fmt.Errorf("wait for WB CPU capacity: %w", leaseErr)
	}
	defer lease.Release()
	if waited >= 250*time.Millisecond {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wb: admitted %d/%d CPU units after %s\n", units, budget, waited.Round(10*time.Millisecond))
	}

	child := process.CommandContext(cmd.Context(), args[0], args[1:]...)
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	child.Env = governedEnvironment(os.Environ(), recorder.OperationID, args, units)

	err = child.Run()
	exitCode := 0
	if err != nil {
		exitCode = exitFindings
		var childExit *exec.ExitError
		if errors.As(err, &childExit) {
			exitCode = childExit.ExitCode()
		}
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

func governedEnvironment(environment []string, operationID string, args []string, units int) []string {
	environment = withEnvironmentValue(environment, runlog.OperationIDEnv, operationID)
	if units <= 0 {
		return environment
	}
	environment = withEnvironmentValue(environment, "WB_CPU_UNITS", fmt.Sprint(units))
	environment = withEnvironmentValue(environment, "GOMAXPROCS", fmt.Sprint(units))
	environment = withEnvironmentValue(environment, "NX_PARALLEL", fmt.Sprint(units))
	if len(args) > 0 && filepath.Base(args[0]) == "go" {
		goFlags := strings.TrimSpace(os.Getenv("GOFLAGS"))
		if goFlags != "" {
			goFlags += " "
		}
		environment = withEnvironmentValue(environment, "GOFLAGS", goFlags+"-p=1")
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
