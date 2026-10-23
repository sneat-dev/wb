package gitops

// Helpers kept for tests only: no production caller remains.

// UnpushedCommits lists commits present on some local branch and on no
// remote-tracking branch, newest first, as `<short-sha> <subject>` lines.
//
// Deliberately every local branch, not just the checked-out one: work is
// abandoned on a side branch at least as often as on the default one, and a
// clone holding either is holding work that exists nowhere else.
func UnpushedCommits(repoPath string) ([]string, error) {
	commits, _, err := UnpushedWork(repoPath)
	return commits, err
}
