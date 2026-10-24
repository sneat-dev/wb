package agents

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/process"
)

// validationLogDirName is the private directory holding one log per step.
const validationLogDirName = "validation"

// maxValidationLogBytes bounds one step's captured output.
const maxValidationLogBytes = 8 << 20

// ValidationOptions is one validation run's inputs.
type ValidationOptions struct {
	WorktreeDir string
	RunDir      string
	Steps       []ValidationStep
	// Lease is inherited by every step, so a validation process still holds the
	// worktree claim until it exits.
	Lease *os.File
	Now   func() time.Time
	// Command builds the process; it is a seam for tests, never a production
	// override.
	Command func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// RunValidation executes the snapshotted steps and reports one record. It never
// takes a credential: a validation is repository-owned code, not a provider
// call, and the worker's credential must not reach it.
func RunValidation(ctx context.Context, options ValidationOptions) ValidationRecord {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	command := options.Command
	if command == nil {
		command = process.CommandContext
	}
	record := ValidationRecord{Status: ValidationRunning, StartedAt: now().UTC()}
	if len(options.Steps) == 0 {
		record.Status = ValidationSkipped
		record.Reason = "no validation steps configured"
		record.FinishedAt = now().UTC()
		return record
	}
	logDir := filepath.Join(options.RunDir, validationLogDirName)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		record.Status = ValidationFailed
		record.Reason = fmt.Sprintf("create validation log directory: %v", err)
		record.FinishedAt = now().UTC()
		return record
	}
	record.Status = ValidationPassed
	for index, step := range options.Steps {
		result := runValidationStep(ctx, options, command, now, logDir, index, step)
		record.Steps = append(record.Steps, result)
		if result.Status != ValidationPassed {
			record.Status = ValidationFailed
		}
	}
	record.FinishedAt = now().UTC()
	return record
}

func runValidationStep(ctx context.Context, options ValidationOptions, command func(context.Context, string, ...string) *exec.Cmd, now func() time.Time, logDir string, index int, step ValidationStep) ValidationStepResult {
	name := strings.TrimSpace(step.Name)
	result := ValidationStepResult{Name: name, Argv: append([]string(nil), step.Argv...), Status: ValidationFailed}
	if len(step.Argv) == 0 {
		result.Status = ValidationSkipped
		return result
	}

	timeout, err := parseStepTimeout(step.Timeout)
	if err != nil {
		return result
	}
	if timeout == 0 {
		timeout = defaultValidationStepTimeout
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	logPath := filepath.Join(logDir, fmt.Sprintf("%02d-%s.log", index+1, safeStepName(name)))
	logFile, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if openErr != nil {
		result.LogPath = ""
		return result
	}
	defer func() { _ = logFile.Close() }()
	bounded := &boundedLog{writer: logFile, remaining: maxValidationLogBytes}
	process := command(stepCtx, step.Argv[0], step.Argv[1:]...)
	process.Dir = options.WorktreeDir
	process.Env = WorkerEnvironment(Credential{})
	process.Stdout = bounded
	process.Stderr = bounded
	if options.Lease != nil {
		process.ExtraFiles = []*os.File{options.Lease}
	}
	started := now()
	if err := process.Start(); err != nil {
		result.LogPath = logPath
		return result
	}
	waitErr := process.Wait()
	result.DurationMS = now().Sub(started).Milliseconds()
	result.LogPath = logPath
	if process.ProcessState != nil {
		code := process.ProcessState.ExitCode()
		result.ExitCode = &code
	}
	if waitErr == nil {
		result.Status = ValidationPassed
	}
	return result
}

func safeStepName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", "..", "-")
	cleaned := replacer.Replace(name)
	if cleaned == "" {
		return "step"
	}
	return cleaned
}
