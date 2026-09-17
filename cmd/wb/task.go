package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/taskoffload"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type taskOffloadDependencies struct {
	create func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error)
	launch func(*cobra.Command, taskLaunchRequest) error
	store  func() (taskoffload.Store, error)
}

type taskLaunchRequest struct {
	WorktreeDir string
	ContextFile string
	Target      string
	Harness     string
	Model       string
	Format      string
}

type taskOffloadOutput struct {
	TaskID      string `json:"task_id"`
	Task        string `json:"task"`
	WorktreeDir string `json:"worktree_dir"`
	Status      string `json:"status"`
	Harness     string `json:"harness,omitempty"`
	Model       string `json:"model,omitempty"`
	Target      string `json:"target,omitempty"`
}

func defaultTaskOffloadDependencies() taskOffloadDependencies {
	return taskOffloadDependencies{
		create: worktrees.Create,
		launch: launchTaskWithSessionMove,
		store: func() (taskoffload.Store, error) {
			home, err := wbhome.Root(projectsRoot)
			if err != nil {
				return taskoffload.Store{}, err
			}
			return taskoffload.NewStore(filepath.Join(home, taskoffload.DirName)), nil
		},
	}
}

func newTaskCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "task",
		Short: "Park, pick up, or offload a portion of work as its own successor session",
	}
	deps := defaultTaskOffloadDependencies()
	command.AddCommand(newTaskOffloadCmdWithDeps(deps, false))
	command.AddCommand(newTaskOffloadCmdWithDeps(deps, true))
	command.AddCommand(newTaskPickupCmdWithDeps(deps))
	return command
}

func newTaskOffloadCmdWithDeps(deps taskOffloadDependencies, parkOnly bool) *cobra.Command {
	var contextFile, harness, model, target, format, originalPrompt string
	use := "offload <task> [owner/repository...]"
	short := "Create an isolated worktree and start a successor session for a portion of work"
	if parkOnly {
		use = "park <task> [owner/repository...]"
		short = "Create an isolated worktree and store continuation without starting a successor"
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			body, err := readParkContext(command, contextFile)
			if err != nil {
				return err
			}
			continuation := strings.TrimSpace(string(body))
			if continuation == "" {
				return fmt.Errorf("task %s requires non-empty continuation via --context-file", command.Name())
			}
			normalizedHarness := ""
			if strings.TrimSpace(harness) != "" {
				normalizedHarness, err = sessionlaunch.NormalizeRuntime("", harness)
				if err != nil {
					return err
				}
			}
			model = sessionlaunch.NormalizeModel(model)
			if originalPrompt == "" {
				originalPrompt = contextFile
			}
			repositories := args[1:]
			if len(repositories) == 0 {
				repository, originErr := worktrees.OriginSlug(command.Context(), ".")
				if originErr != nil {
					return fmt.Errorf("derive current repository: %w", originErr)
				}
				repositories = []string{repository}
			}
			repositories, err = worktrees.ValidateRepositories(repositories)
			if err != nil {
				return err
			}
			workLog, err := worktrees.PrepareWorkLogOptions(projectsRoot, args[0], worktrees.WorkLogOptions{
				OriginalPrompt:        originalPrompt,
				RequireOriginalPrompt: true,
				TaskSummary:           "offload " + args[0],
				Model:                 "unknown",
			})
			if err != nil {
				return err
			}
			if originalPrompt == "-" {
				workLog, err = workLog.WithOriginalPromptFromStdin(body)
				if err != nil {
					return err
				}
			}
			results, err := deps.create(command.Context(), repositories, worktrees.CreateOptions{
				ProjectsRoot: projectsRoot, Operation: args[0], WorkLog: workLog, SessionRequired: true,
			})
			if err != nil {
				return err
			}
			if len(results) == 0 {
				return fmt.Errorf("task %s created no worktree", args[0])
			}
			store, err := deps.store()
			if err != nil {
				return err
			}
			id, err := taskoffload.NewID()
			if err != nil {
				return err
			}
			status := taskoffload.StatusParked
			if !parkOnly {
				status = taskoffload.StatusOffload
			}
			record := taskoffload.Record{
				SchemaVersion: 1, TaskID: id, Task: args[0], WorktreeDir: results[0].WorktreeDir,
				Repository: results[0].Repository, Harness: normalizedHarness, Model: model,
				Target: strings.TrimSpace(target), Status: status, CreatedAt: time.Now().UTC(),
			}
			if err := store.Save(record, continuation); err != nil {
				return err
			}
			if !parkOnly {
				contextPath := filepath.Join(store.Root, id, "context.md")
				if err := deps.launch(command, taskLaunchRequest{
					WorktreeDir: results[0].WorktreeDir, ContextFile: contextPath,
					Target: record.Target, Harness: normalizedHarness, Model: model, Format: format,
				}); err != nil {
					return err
				}
			}
			output := taskOffloadOutput{TaskID: id, Task: args[0], WorktreeDir: results[0].WorktreeDir,
				Status: string(status), Harness: normalizedHarness, Model: model, Target: record.Target}
			return writeTaskOffloadOutput(command, format, parkOnly, output)
		},
	}
	command.Flags().StringVar(&contextFile, "context-file", "", "self-contained successor brief (required; - for stdin)")
	command.Flags().StringVar(&harness, "harness", "", "successor harness: claude or codex")
	command.Flags().StringVar(&model, "model", "", "successor model, e.g. opus or sol")
	command.Flags().StringVar(&target, "to", "", "target WB machine (default: this machine)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	_ = command.MarkFlagRequired("context-file")
	return command
}

func newTaskPickupCmdWithDeps(deps taskOffloadDependencies) *cobra.Command {
	var harness, model, target, format string
	command := &cobra.Command{
		Use:   "pickup <parked-task-id>",
		Short: "Start a successor for a previously parked task",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			store, err := deps.store()
			if err != nil {
				return err
			}
			record, _, err := store.Load(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(harness) != "" {
				record.Harness, err = sessionlaunch.NormalizeRuntime("", harness)
				if err != nil {
					return err
				}
			}
			if model = sessionlaunch.NormalizeModel(model); model != "" {
				record.Model = model
			}
			if strings.TrimSpace(target) != "" {
				record.Target = strings.TrimSpace(target)
			}
			contextPath := filepath.Join(store.Root, record.TaskID, "context.md")
			if err := deps.launch(command, taskLaunchRequest{
				WorktreeDir: record.WorktreeDir, ContextFile: contextPath,
				Target: record.Target, Harness: record.Harness, Model: record.Model, Format: format,
			}); err != nil {
				return err
			}
			record.Status = taskoffload.StatusOffload
			body, err := os.ReadFile(contextPath)
			if err != nil {
				return err
			}
			if err := store.Save(record, string(body)); err != nil {
				return err
			}
			return writeTaskOffloadOutput(command, format, false, taskOffloadOutput{
				TaskID: record.TaskID, Task: record.Task, WorktreeDir: record.WorktreeDir,
				Status: string(record.Status), Harness: record.Harness, Model: record.Model, Target: record.Target,
			})
		},
	}
	command.Flags().StringVar(&harness, "harness", "", "successor harness override")
	command.Flags().StringVar(&model, "model", "", "successor model override")
	command.Flags().StringVar(&target, "to", "", "target WB machine override")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func launchTaskWithSessionMove(command *cobra.Command, request taskLaunchRequest) error {
	move := newSessionMoveCmd()
	args := []string{request.WorktreeDir, "--handover-file", request.ContextFile}
	if request.Target != "" {
		args = append(args, "--to", request.Target)
	}
	if request.Harness != "" {
		args = append(args, "--harness", request.Harness)
	}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	if request.Format != "" {
		args = append(args, "--format", request.Format)
	}
	move.SetArgs(args)
	move.SetIn(command.InOrStdin())
	move.SetOut(command.OutOrStdout())
	move.SetErr(command.ErrOrStderr())
	return move.Execute()
}

func writeTaskOffloadOutput(command *cobra.Command, format string, parked bool, output taskOffloadOutput) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}
	verb := "offloaded"
	if parked {
		verb = "parked"
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%s task %s as %s in %s\n", verb, output.Task, output.TaskID, output.WorktreeDir)
	return err
}
