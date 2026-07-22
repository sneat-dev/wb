package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runCommand(ctx context.Context, timeout time.Duration, retry int, dir, name string, args ...string) (string, int, error) {
	return runCommandWithEnv(ctx, timeout, retry, dir, nil, name, args...)
}

func runGoCommand(ctx context.Context, options Options, dir string, args ...string) (string, int, error) {
	return runCommandWithEnv(ctx, options.Timeout, options.Retry, dir, goCommandEnvironment(os.Environ(), options.GoPrivate), "go", args...)
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
		if environment != nil {
			command.Env = environment
		}
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
