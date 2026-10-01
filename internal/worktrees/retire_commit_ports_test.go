package worktrees

import (
	"errors"
	"strings"
	"testing"
)

func TestRetirementCommitPortsRefuseUnsafePathsAndFailuresBeforeCommit(t *testing.T) {
	t.Parallel()
	boom := errors.New("injected commit failure")
	for _, tc := range []struct {
		name, failCommand, unsafeCommand, unsafePath string
		untrackedError, preparedError                bool
		want                                         string
	}{
		{"unstaged secret", "", "diff --name-only -z --diff-filter=ACMR", ".env\x00", false, false, "secret-looking"},
		{"staged secret", "", "diff --cached --name-only -z --diff-filter=ACMR", "private.pem\x00", false, false, "secret-looking"},
		{"untracked secret", "", "ls-files --others --exclude-standard -z", "id_ed25519\x00", false, false, "secret-looking"},
		{"untracked inspection", "", "ls-files --others --exclude-standard -z", "ordinary.txt\x00", true, false, "injected commit failure"},
		{"first scan fails", "diff --name-only -z --diff-filter=ACMR", "", "", false, false, "injected commit failure"},
		{"stage fails", "add -A", "", "", false, false, "injected commit failure"},
		{"staged list fails", "diff --cached --name-only -z", "", "", false, false, "injected commit failure"},
		{"staged secret scan fails", "diff --cached --name-only -z --diff-filter=ACMR", "", "", false, false, "injected commit failure"},
		{"post-stage secret", "", "diff --cached --name-only -z --diff-filter=ACMR", ".env.local\x00", false, false, "secret-looking"},
		{"tree fails", "write-tree", "", "", false, false, "injected commit failure"},
		{"durable intent fails", "", "", "", false, true, "injected commit failure"},
		{"hooks fail", "commit -m Retire worktree source changes", "", "", false, false, "commit source with hooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			committed := false
			ports := retireCommitPorts{
				gitHeld: func(args ...string) ([]byte, error) {
					command := strings.Join(args, " ")
					if command == tc.failCommand {
						return nil, boom
					}
					if command == "commit -m Retire worktree source changes" {
						committed = true
					}
					if command == tc.unsafeCommand {
						return []byte(tc.unsafePath), nil
					}
					if command == "diff --cached --name-only -z" {
						return []byte("ordinary.txt\x00"), nil
					}
					if command == "write-tree" {
						return []byte(strings.Repeat("a", 40) + "\n"), nil
					}
					return nil, nil
				},
				checkUntracked: func(string) error {
					if tc.untrackedError {
						return boom
					}
					return nil
				},
			}
			prepared := func(string, string) error {
				if tc.preparedError {
					return boom
				}
				return nil
			}
			if tc.name == "post-stage secret" { // The first cached scan is empty; the second is post-add evidence.
				original := ports.gitHeld
				calls := 0
				ports.gitHeld = func(args ...string) ([]byte, error) {
					if strings.Join(args, " ") == tc.unsafeCommand {
						calls++
						if calls == 1 {
							return nil, nil
						}
					}
					return original(args...)
				}
			}
			if tc.name == "staged secret scan fails" { // The pre-stage scan passes; the post-stage scan fails.
				original := ports.gitHeld
				calls := 0
				ports.gitHeld = func(args ...string) ([]byte, error) {
					if strings.Join(args, " ") == tc.failCommand {
						calls++
						if calls == 1 {
							return nil, nil
						}
					}
					return original(args...)
				}
			}
			_, err := retireCommitSourceWithPorts("", prepared, ports)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("commit error=%v, want %q", err, tc.want)
			}
			if committed && tc.name != "hooks fail" {
				t.Fatal("committed before refusal")
			}
		})
	}
}

func TestRetirementCommitPortsPersistTreeBeforeGitCommit(t *testing.T) {
	t.Parallel()
	tree := strings.Repeat("a", 40)
	var stages []string
	ports := retireCommitPorts{gitHeld: func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		if command == "diff --cached --name-only -z" {
			return []byte("ordinary.txt\x00"), nil
		}
		if command == "write-tree" {
			return []byte(tree + "\n"), nil
		}
		if strings.HasPrefix(command, "commit ") {
			stages = append(stages, "commit")
		}
		return nil, nil
	}, checkUntracked: func(string) error { return nil }}
	committed, err := retireCommitSourceWithPorts("  custom message  ", func(actual, message string) error {
		if actual != tree || message != "custom message" {
			t.Fatalf("intent = (%s,%s)", actual, message)
		}
		stages = append(stages, "intent")
		return nil
	}, ports)
	if err != nil || !committed || strings.Join(stages, ",") != "intent,commit" {
		t.Fatalf("commit=%v err=%v stages=%v", committed, err, stages)
	}
	ports.gitHeld = func(args ...string) ([]byte, error) { return nil, nil }
	committed, err = retireCommitSourceWithPorts("", nil, ports)
	if err != nil || committed {
		t.Fatalf("empty source committed: %v %v", committed, err)
	}
}
