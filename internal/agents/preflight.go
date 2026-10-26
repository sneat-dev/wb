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

// preflightTimeout bounds each read-only probe WB runs before creating any
// worktree. A probe that hangs is not a reason to hang the dispatcher.
const preflightTimeout = 15 * time.Second

// PreflightDeps are the seams preflight needs.
type PreflightDeps struct {
	LookPath          func(string) (string, error)
	ResolveCredential func(Provider) (Credential, error)
	Run               func(ctx context.Context, name string, args ...string) ([]byte, error)
	TempDir           func() (string, error)
}

// DefaultPreflightDeps returns the production seams.
func DefaultPreflightDeps() PreflightDeps {
	return PreflightDeps{
		LookPath:          exec.LookPath,
		ResolveCredential: ResolveCredential,
		Run:               runProbe,
		TempDir:           func() (string, error) { return os.MkdirTemp("", "wb-agent-preflight-") },
	}
}

func runProbe(ctx context.Context, name string, args ...string) ([]byte, error) {
	// process.CommandContext is WB's own process boundary: it owns the process
	// group and cancellation this probe needs.
	command := process.CommandContext(ctx, name, args...)
	// A probe never needs the user's credential in its environment; the
	// harness reads its own store when it runs.
	command.Env = WorkerEnvironment(Credential{})
	// agy writes --help to stderr even when the probe succeeds. Capture both
	// streams for capability discovery without changing stdout-only version and
	// model probes.
	var output []byte
	var err error
	if len(args) == 1 && args[0] == "--help" {
		output, err = command.CombinedOutput()
	} else {
		output, err = command.Output()
	}
	if err != nil {
		return output, fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}
	return output, nil
}

// Preflight proves an execution can start before anything is created: the
// harness binary exists, a Codex run has a resolvable credential, and a `dsh`
// run can prove the model its overlay asks for.
func Preflight(ctx context.Context, resolved Resolved, deps PreflightDeps) error {
	if deps.LookPath == nil || deps.Run == nil {
		return fmt.Errorf("agent preflight requires executable lookup and a runner")
	}
	executable, err := deps.LookPath(harnessExecutable(resolved.Harness))
	if err != nil {
		return fmt.Errorf("harness executable %q is not on PATH: %w", resolved.Harness, err)
	}
	if resolved.Harness == HarnessCodex {
		if deps.ResolveCredential == nil {
			return fmt.Errorf("agent preflight requires a credential resolver for the %q harness", HarnessCodex)
		}
		if _, err := deps.ResolveCredential(resolved.Routing); err != nil {
			return fmt.Errorf("provider %q: %w", resolved.Provider, err)
		}
	}
	if resolved.Harness == HarnessDSH {
		return proveDshModel(ctx, executable, resolved.Model, deps)
	}
	if resolved.Harness == HarnessAntigravity {
		probeCtx, cancel := context.WithTimeout(ctx, preflightTimeout)
		defer cancel()
		help, err := deps.Run(probeCtx, executable, "--help")
		if err != nil {
			return fmt.Errorf("probe antigravity CLI capabilities: %w", err)
		}
		for _, flag := range []string{"--print", "--output-format", "--mode", "--sandbox", "--model", "--effort", "--add-dir", "--print-timeout"} {
			if !strings.Contains(string(help), flag) {
				return fmt.Errorf("antigravity CLI is missing required %s capability; update agy", flag)
			}
		}
	}
	return nil
}

// proveDshModel runs the harness's own config dump with the exact overlay a run
// would apply. Nothing else proves which model a `dsh` run will actually use,
// because the model is not a command-line argument.
func proveDshModel(ctx context.Context, executable, model string, deps PreflightDeps) error {
	tempDir, err := deps.TempDir()
	if err != nil {
		return fmt.Errorf("prepare dsh model proof: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	patchPath := filepath.Join(tempDir, dshModelPatchName)
	if err := os.WriteFile(patchPath, dshModelPatch(model), 0o600); err != nil {
		return fmt.Errorf("write dsh model proof overlay: %w", err)
	}
	output, err := deps.Run(ctx, executable, "--profile", dshProfile, "--patch", patchPath, "--dump-config")
	if err != nil {
		return fmt.Errorf("prove the %q harness model: %w", HarnessDSH, err)
	}
	dump := string(output)
	if !strings.Contains(dump, "id: agent-default-model") || !strings.Contains(dump, "model: "+strings.TrimSpace(model)) {
		return fmt.Errorf("the %q harness did not accept model %q in its composed configuration; the profile would run a different model", HarnessDSH, model)
	}
	return nil
}

// DetectVersion reads the installed harness version, best effort. An unknown
// version is recorded as absent rather than guessed.
func DetectVersion(ctx context.Context, harness string, deps PreflightDeps) string {
	if deps.Run == nil {
		return ""
	}
	lookPath := deps.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	executable, err := lookPath(harnessExecutable(harness))
	if err != nil {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	output, err := deps.Run(probeCtx, executable, "--version")
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(output))
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}
