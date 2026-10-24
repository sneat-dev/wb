package gitops

import "strings"

// GitConfig runs `git config <args>` in repoPath and returns its trimmed
// output. A `--get` of an unset key fails (git exits 1), so callers that read
// an optional key treat an error as "unset".
func GitConfig(repoPath string, args ...string) (string, error) {
	out, err := run(repoPath, "git", append([]string{"config"}, args...)...)
	return strings.TrimSpace(out), err
}
