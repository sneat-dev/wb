package hooks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type RunOptions struct {
	RepoPath   string
	ConfigPath string
	Hook       string
	Args       []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Now        func() time.Time
}

type RunResult struct {
	ExitCode     int
	Duration     time.Duration
	Blocks       []BlockRunResult
	MetricsError error
}

type BlockRunResult struct {
	ID       string
	Profile  string
	ExitCode int
	Duration time.Duration
}

// Run executes the configured base and active-profile blocks in the repository
// root and records compact local events. Hook arguments and streams are passed
// through unchanged; composed pre-push blocks each receive the complete stdin.
func Run(options RunOptions) (RunResult, error) {
	if !validHookName.MatchString(options.Hook) {
		return RunResult{ExitCode: 2}, fmt.Errorf("invalid hook name %q", options.Hook)
	}
	policy, err := LoadPolicy(options.RepoPath, options.ConfigPath)
	if err != nil {
		return RunResult{ExitCode: 2}, err
	}
	if !contains(expectedHookNames(policy), options.Hook) {
		return RunResult{ExitCode: 2}, fmt.Errorf("hook %q is disabled or not configured", options.Hook)
	}
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	blocks := hookBlocks(policy, options.Hook)
	var replicatedInput []byte
	if options.Hook == "pre-push" && len(blocks) > 1 {
		replicatedInput, err = io.ReadAll(options.Stdin)
		if err != nil {
			return RunResult{ExitCode: 2}, fmt.Errorf("read pre-push input for composed hook blocks: %w", err)
		}
	}

	var metricEvents []Event
	executionContext := loadEventContext(policy.RepoRoot, policy.Metrics.Labels)
	started := options.Now()
	exitCode := 0
	var runErr error
	result := RunResult{Blocks: make([]BlockRunResult, 0, len(blocks))}
	for _, block := range blocks {
		blockOptions := options
		if replicatedInput != nil {
			blockOptions.Stdin = bytes.NewReader(replicatedInput)
		}
		blockStarted := options.Now()
		exitCode, runErr = runTemplate(policy, block, blockOptions, executionContext)
		blockDuration := options.Now().Sub(blockStarted)
		result.Blocks = append(result.Blocks, BlockRunResult{
			ID: block.ID, Profile: block.Profile, ExitCode: exitCode, Duration: blockDuration,
		})
		if policy.Metrics.Enabled {
			metricEvents = append(metricEvents, executionContext.newBlockEvent(options.Hook, block, exitCode == 0, blockDuration, options.Now()))
		}
		if runErr != nil {
			break
		}
	}
	duration := options.Now().Sub(started)
	result.ExitCode = exitCode
	result.Duration = duration
	if policy.Metrics.Enabled {
		metricEvents = append(metricEvents, executionContext.newEvent(options.Hook, exitCode == 0, duration, options.Now()))
		result.MetricsError = AppendEvents(policy.Metrics.Path, metricEvents)
	}
	return result, runErr
}

func runTemplate(policy Policy, block HookBlock, options RunOptions, context eventContext) (int, error) {
	templatePath := block.Hook.Template
	cleanup := func() {}
	if block.Hook.Builtin {
		content, ok := builtinTemplate(block.Hook.Template)
		if !ok {
			return 2, fmt.Errorf("unknown built-in template %q", block.Hook.Template)
		}
		temporary, err := os.CreateTemp("", "wb-hook-*.sh")
		if err != nil {
			return 2, err
		}
		templatePath = temporary.Name()
		cleanup = func() { _ = os.Remove(templatePath) }
		if _, err := temporary.WriteString(content); err != nil {
			_ = temporary.Close()
			cleanup()
			return 2, err
		}
		if err := temporary.Close(); err != nil {
			cleanup()
			return 2, err
		}
	}
	defer cleanup()

	cmd := exec.Command("/bin/sh", append([]string{templatePath}, options.Args...)...)
	cmd.Dir = policy.RepoRoot
	cmd.Stdin = options.Stdin
	cmd.Stdout = options.Stdout
	cmd.Stderr = options.Stderr
	cmd.Env = append(os.Environ(),
		"WB_HOOK="+block.Hook.Name,
		"WB_PROFILE="+block.Profile,
		"WB_BLOCK="+block.ID,
		"WB_REPO_ROOT="+policy.RepoRoot,
		"WB_REPO_SLUG="+context.repository,
		"WB_HEAD_SHA="+context.commit,
		"WB_BRANCH="+context.branch,
		"WB_HOOKS_CONFIG="+block.Hook.ConfigPath,
		"WB_HOOK_METRICS_PATH="+policy.Metrics.Path,
	)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), fmt.Errorf("%s block failed with exit %d", block.ID, exitErr.ExitCode())
		}
		return 2, fmt.Errorf("run %s template %s: %w", block.ID, filepath.Clean(templatePath), err)
	}
	return 0, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
