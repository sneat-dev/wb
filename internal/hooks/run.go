package hooks

import (
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
	MetricsError error
}

// Run executes a configured template in the repository root and records a
// compact local event. Hook script stdin/stdout/stderr and arguments are passed
// through unchanged so pre-push and commit-message hooks keep Git semantics.
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

	started := options.Now()
	exitCode := 0
	var runErr error
	if hook, ok := policy.Hooks[options.Hook]; ok && !hook.Disabled && hook.Template != "" {
		exitCode, runErr = runTemplate(policy, hook, options)
	}
	duration := options.Now().Sub(started)
	result := RunResult{ExitCode: exitCode, Duration: duration}
	if policy.Metrics.Enabled {
		event := newEvent(policy.RepoRoot, options.Hook, exitCode == 0, duration, options.Now(), policy.Metrics.Labels)
		if metricsErr := AppendEvent(policy.Metrics.Path, event); metricsErr != nil {
			result.MetricsError = metricsErr
		}
	}
	return result, runErr
}

func runTemplate(policy Policy, hook ResolvedHook, options RunOptions) (int, error) {
	templatePath := hook.Template
	cleanup := func() {}
	if hook.Builtin {
		content, ok := builtinTemplate(hook.Template)
		if !ok {
			return 2, fmt.Errorf("unknown built-in template %q", hook.Template)
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
	commit, _ := gitOutput(policy.RepoRoot, "rev-parse", "HEAD")
	branch, _ := gitOutput(policy.RepoRoot, "symbolic-ref", "--quiet", "--short", "HEAD")
	cmd.Env = append(os.Environ(),
		"WB_HOOK="+hook.Name,
		"WB_REPO_ROOT="+policy.RepoRoot,
		"WB_REPO_SLUG="+originSlug(policy.RepoRoot),
		"WB_HEAD_SHA="+commit,
		"WB_BRANCH="+branch,
		"WB_HOOKS_CONFIG="+hook.ConfigPath,
		"WB_HOOK_METRICS_PATH="+policy.Metrics.Path,
	)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), fmt.Errorf("%s hook failed with exit %d", hook.Name, exitErr.ExitCode())
		}
		return 2, fmt.Errorf("run %s template %s: %w", hook.Name, filepath.Clean(templatePath), err)
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
