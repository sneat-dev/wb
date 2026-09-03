package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// commandPipeDrainWaitDelay bounds the period exec.Cmd waits after its direct
// child exits but a descendant still holds one of CombinedOutput's pipes open.
// Package-manager launchers may hand work to another process and exit first;
// without this bound the dependency planner can remain in planning_wave forever
// despite having no child process left to observe or cancel.
var commandPipeDrainWaitDelay = 5 * time.Second

func runCommand(ctx context.Context, timeout time.Duration, retry int, dir, name string, args ...string) (string, int, error) {
	return runCommandWithEnv(ctx, timeout, retry, dir, nil, name, args...)
}

func runGoCommand(ctx context.Context, options Options, dir string, args ...string) (string, int, error) {
	environment := goCommandEnvironment(os.Environ(), options.GoPrivate)
	if mutatesModuleGraph(args) {
		// GOWORK=off is set BY THE VERB, never left to the caller.
		//
		// `go mod tidy` and `go get` resolve against the workspace, so running
		// either while a `wb deps propagate local` link is live writes a
		// go.sum describing an UNPUBLISHED library tree. Catching that at the
		// merge guard would be too late: the poisoned commit already exists
		// and CI has already run on it. Turning the workspace off here makes
		// the resolution always the published one.
		//
		// Implements: dependency-streams#req:no-module-graph-mutation-under-a-live-link.
		environment = append(environment, "GOWORK=off")
	}
	return runCommandWithEnv(ctx, options.Timeout, options.Retry, dir, environment, "go", args...)
}

// mutatesModuleGraph reports whether a `go` invocation can rewrite go.mod or
// go.sum. Read-only commands keep whatever workspace the caller has, because
// turning it off would change what they report.
func mutatesModuleGraph(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "get":
		return true
	case "mod":
		return len(args) > 1 && (args[1] == "tidy" || args[1] == "edit" || args[1] == "download")
	}
	return false
}

func runCommandWithEnv(ctx context.Context, timeout time.Duration, retry int, dir string, environment []string, name string, args ...string) (string, int, error) {
	attempts := 0
	for {
		attempts++
		attemptCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		command := exec.CommandContext(attemptCtx, name, args...)
		command.Dir = dir
		command.WaitDelay = commandPipeDrainWaitDelay
		if environment == nil {
			environment = os.Environ()
		}
		command.Env = console.CommandEnv(environment)
		output, err := command.CombinedOutput()
		timedOut := attemptCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			err = fmt.Errorf("timed out after %s", timeout)
		}
		if err == nil || attempts > retry {
			if err != nil {
				return string(output), attempts, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
			}
			return string(output), attempts, nil
		}
	}
}

// normalizeGoPrivatePatterns splits repeatable CLI values, removes blanks and
// duplicates, and preserves the first occurrence for predictable diagnostics.
func normalizeGoPrivatePatterns(patterns []string) []string {
	seen := make(map[string]bool, len(patterns))
	result := make([]string, 0, len(patterns))
	for _, value := range patterns {
		for _, pattern := range strings.Split(value, ",") {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" || seen[pattern] {
				continue
			}
			seen[pattern] = true
			result = append(result, pattern)
		}
	}
	return result
}

// goCommandEnvironment augments the inherited Go privacy settings for one
// subprocess. GOPRIVATE is Go's high-level private-module setting; explicit
// GONOPROXY or GONOSUMDB values take precedence over its defaults, so they are
// extended too. This does not provide credentials: Git remains responsible for
// authenticating direct fetches through its configured credential helper.
func goCommandEnvironment(base, privatePatterns []string) []string {
	privatePatterns = normalizeGoPrivatePatterns(privatePatterns)
	if len(privatePatterns) == 0 {
		return append([]string(nil), base...)
	}
	values := make(map[string]string, len(base)+3)
	order := make([]string, 0, len(base)+3)
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, exists := values[name]; !exists {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, name := range []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB"} {
		values[name] = mergeGoPrivatePatterns(values[name], privatePatterns)
		if !containsString(order, name) {
			order = append(order, name)
		}
	}
	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, name+"="+values[name])
	}
	return result
}

func mergeGoPrivatePatterns(existing string, additions []string) string {
	return strings.Join(normalizeGoPrivatePatterns(append(strings.Split(existing, ","), additions...)), ",")
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
