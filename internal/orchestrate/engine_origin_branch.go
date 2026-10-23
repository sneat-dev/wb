package orchestrate

import (
	"context"
	"fmt"
	"strings"
)

// resolveOriginDefaultBranch determines the repository's actual default
// branch on origin. It prefers the locally cached origin/HEAD symref, which
// `git clone` sets automatically; a long-lived canonical clone assembled by
// `git remote add` + `git fetch` (common across an older fleet) never gets
// that symref, and a clone's cached symref can also go stale after the
// remote's default branch is renamed on GitHub — so a missing or unusable
// symref is refreshed from origin (`git remote set-head origin --auto`,
// falling back to `git ls-remote --symref`) before giving up.
func resolveOriginDefaultBranch(ctx context.Context, canonical string, options Options) (string, error) {
	if ref, err := readOriginHeadSymref(ctx, canonical, options); err == nil && ref != "" {
		return ref, nil
	}
	if _, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "remote", "set-head", "origin", "--auto"); err == nil {
		if ref, err := readOriginHeadSymref(ctx, canonical, options); err == nil && ref != "" {
			return ref, nil
		}
	}
	output, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	ref, err := parseLsRemoteSymref(output)
	if err != nil {
		return "", err
	}
	return ref, nil
}

func readOriginHeadSymref(ctx context.Context, canonical string, options Options) (string, error) {
	output, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, canonical, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(output)
	const prefix = "refs/remotes/origin/"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("unexpected origin/HEAD symref %q", ref)
	}
	return strings.TrimPrefix(ref, prefix), nil
}

// parseLsRemoteSymref extracts the branch name from `git ls-remote --symref
// origin HEAD` output, which looks like:
//
//	ref: refs/heads/master	HEAD
//	<sha>	HEAD
func parseLsRemoteSymref(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		const prefix = "refs/heads/"
		if ref, ok := strings.CutPrefix(fields[0], prefix); ok {
			return ref, nil
		}
	}
	return "", fmt.Errorf("origin HEAD symref not found in ls-remote output")
}
