package orchestrate

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// wipSubjectPattern recognizes a work-in-progress commit subject in any of
// its ordinary spellings: "wip", "wip:", "wip(scope):". GitHub would
// otherwise happily title a pull request with exactly this, and a caller
// cannot fix that afterward without rewriting a protected branch's history.
var wipSubjectPattern = regexp.MustCompile(`(?i)^wip\b`)

func isWipSubject(subject string) bool {
	return wipSubjectPattern.MatchString(strings.TrimSpace(subject))
}

// pullRequestCreateTitle derives a pull request title from the branch's own
// commit subjects: a single commit contributes its subject verbatim, and
// several commits contribute the most recent one that reads like a real
// change. A "wip"-shaped subject never wins either way, because it is a
// placeholder, not a description.
func pullRequestCreateTitle(subjects []string) string {
	nonEmpty := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if trimmed := strings.TrimSpace(subject); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return "Open pull request"
	}
	if len(nonEmpty) == 1 {
		if !isWipSubject(nonEmpty[0]) {
			return nonEmpty[0]
		}
		return "apply 1 commit"
	}
	usable := make([]string, 0, len(nonEmpty))
	for _, subject := range nonEmpty {
		if !isWipSubject(subject) {
			usable = append(usable, subject)
		}
	}
	if len(usable) == 0 {
		return fmt.Sprintf("apply %d commits", len(nonEmpty))
	}
	if len(usable) == 1 {
		return usable[0]
	}
	// git log lists newest first; the oldest usable subject normally states
	// the branch's own purpose, and later commits are review/CI repairs.
	base := usable[len(usable)-1]
	related := len(usable) - 1
	word := "changes"
	if related == 1 {
		word = "change"
	}
	return fmt.Sprintf("%s and %d related %s", base, related, word)
}

// pullRequestCreateBody derives a pull request body from options and the
// branch's own commits, in the order the contract fixes: literal --body wins,
// then --body-file, then the commit-derived default. --title alone never
// substitutes for a body: overriding what the pull request is called says
// nothing about what it contains.
func pullRequestCreateBody(options PullRequestCreateOptions, worktree string, subjects []string) (string, error) {
	body, err := pullRequestCreateBodyWithoutCloses(options, worktree, subjects)
	if err != nil {
		return "", err
	}
	return withClosesPrefix(body, options.Closes), nil
}

func pullRequestCreateBodyWithoutCloses(options PullRequestCreateOptions, worktree string, subjects []string) (string, error) {
	if body := strings.TrimSpace(options.Body); body != "" {
		return options.Body, nil
	}
	if path := strings.TrimSpace(options.BodyFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --body-file %s: %w", path, err)
		}
		return string(contents), nil
	}
	if len(subjects) == 1 {
		commitBody, _, err := runCommand(context.Background(), options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "log", "-1", "--format=%b")
		if err == nil {
			if trimmed := strings.TrimSpace(commitBody); trimmed != "" {
				return trimmed, nil
			}
		}
		return "", nil
	}
	var body strings.Builder
	body.WriteString("Mechanically prepared by `wb pr create` from the branch's own commits.\n\nCommits:\n\n")
	for index := len(subjects) - 1; index >= 0; index-- {
		fmt.Fprintf(&body, "- %s\n", subjects[index])
	}
	return body.String(), nil
}
