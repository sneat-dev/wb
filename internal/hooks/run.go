package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/console"
)

type RunOptions struct {
	RepoPath     string
	ConfigPath   string
	Hook         string
	Args         []string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Now          func() time.Time
	WBExecutable string
	ProjectsRoot string
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
	layout, err := ResolveExecutionLayout(policy.RepoRoot, options.ProjectsRoot)
	if err != nil {
		return RunResult{ExitCode: 2}, fmt.Errorf("resolve hook runtime layout: %w", err)
	}
	if err := ensureExecutionLayout(layout); err != nil {
		return RunResult{ExitCode: 2}, fmt.Errorf("prepare hook runtime layout: %w", err)
	}
	if options.Hook == "pre-push" {
		branch, commit, present, attestationErr := canonicalrescue.PushAttestationFromEnvironment()
		if attestationErr != nil {
			return RunResult{ExitCode: 1}, attestationErr
		}
		if present {
			started := options.Now()
			verifyErr := canonicalrescue.VerifyAttestedPush(
				context.Background(), policy.RepoRoot, options.ProjectsRoot, branch, commit, options.Stdin,
			)
			result := RunResult{
				ExitCode: 0, Duration: options.Now().Sub(started),
				Blocks: []BlockRunResult{{ID: "worktree/rescue-pre-push", Profile: "worktree", ExitCode: 0}},
			}
			if verifyErr != nil {
				result.ExitCode = 1
				result.Blocks[0].ExitCode = 1
				return result, fmt.Errorf("verify canonical rescue push: %w", verifyErr)
			}
			return result, nil
		}
	}

	blocks := hookBlocks(policy, options.Hook)
	var replicatedInput []byte
	if shouldReplicateStdin(options.Hook, len(blocks), console.IsTerminal(options.Stdin)) {
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
		exitCode, runErr = runTemplate(policy, block, blockOptions, executionContext, layout)
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
		result.MetricsError = recordMetrics(policy, layout, metricEvents, options.Now())
	}
	return result, runErr
}

// shouldReplicateStdin reports whether stdin must be drained so that every
// composed block receives the same input.
//
// Only a composed pre-push hook needs it: git streams the pushed refs on stdin
// and each block expects the complete list. When wb is invoked by hand or by an
// agent rather than by git, stdin is the terminal, and draining it would block
// forever waiting for a human who was never asked to type anything — the whole
// command hangs with no output. A terminal therefore counts as no input at all;
// a pipe or a redirect is real input and is drained as before.
func shouldReplicateStdin(hook string, blockCount int, stdinIsTerminal bool) bool {
	if hook != "pre-push" || blockCount <= 1 {
		return false
	}
	return !stdinIsTerminal
}

func runTemplate(policy Policy, block HookBlock, options RunOptions, context eventContext, layout ExecutionLayout) (int, error) {
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
	wbExecutable := options.WBExecutable
	if wbExecutable == "" {
		wbExecutable, _ = os.Executable()
	}
	cmd.Env = append(gitDirectoryEnvironmentCleared(os.Environ()),
		"WB_HOOK="+block.Hook.Name,
		"WB_PROFILE="+block.Profile,
		"WB_BLOCK="+block.ID,
		"WB_REPO_ROOT="+policy.RepoRoot,
		"WB_REPO_SLUG="+context.repository,
		"WB_HEAD_SHA="+context.commit,
		"WB_BRANCH="+context.branch,
		"WB_HOOKS_CONFIG="+block.Hook.ConfigPath,
		"WB_HOOK_RUNTIME_ROOT="+layout.Root,
		"WB_HOOK_CACHE_ROOT="+layout.CacheRoot,
		"WB_HOOK_METRICS_PATH="+policy.Metrics.Path,
		"WB_HOOK_PENDING_ROOT="+layout.PendingMetricsRoot,
		"WB_HOOK_REPORT_ROOT="+layout.ReportRoot,
		"WB_EXECUTABLE="+wbExecutable,
		"WB_PROJECTS_ROOT="+options.ProjectsRoot,
		"GOPATH="+layout.GoPath,
		"GOCACHE="+layout.GoCache,
		"GOMODCACHE="+layout.GoModCache,
		"GOTMPDIR="+layout.GoTmpDir,
		"TMPDIR="+layout.GoTmpDir,
		"XDG_CACHE_HOME="+layout.XDGCacheHome,
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

// gitGeneratedEnvironmentVars pins the repository git itself resolved before
// invoking the hook currently running wb. cmd.Dir already establishes which
// repository a block's script operates in; these must not also come along,
// or a block that shells out to git itself -- a test suite creating its own
// fixture repositories, for instance -- gets silently redirected at
// whichever repository triggered the outer hook instead of the one it
// intended. Confirmed empirically: git sets GIT_DIR to a path relative to
// its own invocation, which then resolves against the wrong repository the
// moment a block's script or anything it spawns starts from a different cwd.
var gitGeneratedEnvironmentVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
}

func gitDirectoryEnvironmentCleared(environment []string) []string {
	cleared := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && contains(gitGeneratedEnvironmentVars, name) {
			continue
		}
		cleared = append(cleared, entry)
	}
	return cleared
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
