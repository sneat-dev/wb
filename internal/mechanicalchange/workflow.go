package mechanicalchange

import (
	"path"
	"regexp"
	"strings"
)

// PullRequest is the part of a pull request's identity the classifier may
// rely on besides its diff. A bot-authored pull request is the only place a
// workflow file edit can be mechanical: the same line typed by a person is a
// change to what CI executes, and only the author says which one this is.
type PullRequest struct {
	// Author is the GitHub login of the pull request author.
	Author string
	// Title is the pull request title; a bot names the dependency and the
	// target version in it.
	Title string
	// CommitsByBot is true only when every commit on the pull request was
	// authored by a dependency bot (see AllCommitsByDependencyBot). The
	// author of the pull request is not enough: in this fleet other bots and
	// people push repair commits onto a Renovate branch, and the pull request
	// still reads as Renovate's. Unset (including when the commit list could
	// not be read) the bot-only shapes are refused.
	CommitsByBot bool
}

// CommitActors are the GitHub logins of one commit's author and committer, as
// the pull request commits API reports them (empty when GitHub has no account
// for the git identity).
type CommitActors struct {
	Author    string
	Committer string
}

// AllCommitsByDependencyBot reports whether every commit was authored by a
// dependency bot. The committer may also be `web-flow`: a bot that commits
// through the GitHub API (Renovate does) shows `web-flow` as committer, and a
// person's web-UI edit still carries that person as author.
func AllCommitsByDependencyBot(commits []CommitActors) bool {
	if len(commits) == 0 {
		return false
	}
	for _, commit := range commits {
		if !IsDependencyBotLogin(commit.Author) {
			return false
		}
		if committer := strings.ToLower(strings.TrimSpace(commit.Committer)); committer != "web-flow" && !IsDependencyBotLogin(committer) {
			return false
		}
	}
	return true
}

// IsDependencyBotLogin reports whether login is a dependency bot account.
func IsDependencyBotLogin(login string) bool {
	return dependencyBotLogins[strings.ToLower(strings.TrimSpace(login))]
}

// dependencyBotLogins are the authors whose pull requests are dependency
// bumps by construction. GitHub reserves the `[bot]` suffix for apps, so a
// person cannot take one of these names; `app/...` is how `gh` spells the
// same accounts.
var dependencyBotLogins = map[string]bool{
	"renovate[bot]":   true,
	"dependabot[bot]": true,
	"app/renovate":    true,
	"app/dependabot":  true,
}

func (pr PullRequest) byDependencyBot() bool {
	return pr.CommitsByBot && IsDependencyBotLogin(pr.Author)
}

var (
	// `uses: owner/action[/path]@ref`, optionally a list item, optionally
	// followed by a `# vX` comment. Quoted values and expressions do not match.
	workflowUsesLine = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*([A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+)@([A-Za-z0-9_.+-]+)\s*(?:#\s*(.*))?$`)
	// `some-version: 1.2.3`, with optional quotes and trailing comment.
	workflowVersionLine = regexp.MustCompile(`^\s*(?:-\s+)?([A-Za-z0-9_-]+):\s*["']?([^"'#\s]+)["']?\s*(?:#.*)?$`)
	workflowTagRef      = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|v?\d+(?:\.\d+){0,3}(?:[-+][0-9A-Za-z.]+)?)$`)
	workflowVersionText = regexp.MustCompile(`^v?\d+(?:\.\d+){0,3}(?:[-+][0-9A-Za-z.]+)?$`)
)

// workflowFile reports whether name is a GitHub Actions workflow definition.
func workflowFile(name string) bool {
	normalized := strings.TrimPrefix(name, "./")
	if !strings.HasPrefix(normalized, ".github/workflows/") && !strings.Contains(normalized, "/.github/workflows/") {
		return false
	}
	extension := path.Ext(normalized)
	return extension == ".yml" || extension == ".yaml"
}

// nonMechanicalWorkflow returns the reason a bot-authored workflow edit is
// not a dependency bump, or "".
//
// Accepted: every changed line is, paired with the line it replaces, either
// the ref of the same `uses: owner/action[/path]@ref` (a version tag or a full
// commit SHA) or the value of the same version key (`go-version`,
// `node-version`, `version`, `GOLANGCI_LINT_VERSION`, ...) set to a version
// the pull request title names. Anything else — a new or removed step,
// permissions, triggers, `run:` text, a changed action name — is refused.
func nonMechanicalWorkflow(name, status, patch string, pr PullRequest) string {
	if status != "" && status != "modified" {
		return name + " is " + status + ", not modified, so it is more than a version edit"
	}
	if strings.TrimSpace(patch) == "" {
		return name + " has no diff GitHub could show, so its content cannot be classified"
	}
	var removed, added []string
	for _, line := range strings.Split(patch, "\n") {
		if len(line) == 0 || line[0] != '+' && line[0] != '-' || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if line[0] == '-' {
			removed = append(removed, line[1:])
		} else {
			added = append(added, line[1:])
		}
	}
	if len(removed) == 0 || len(removed) != len(added) {
		return name + " adds or removes lines, which a version bump does not do"
	}
	for index := range removed {
		if reason := workflowPairRefusal(name, removed[index], added[index], pr); reason != "" {
			return reason
		}
	}
	return ""
}

func workflowPairRefusal(name, before, after string, pr PullRequest) string {
	if oldUses, newUses := workflowUsesLine.FindStringSubmatch(before), workflowUsesLine.FindStringSubmatch(after); oldUses != nil || newUses != nil {
		if oldUses == nil || newUses == nil || oldUses[1] != newUses[1] {
			return name + " changes a uses: line to a different action: " + trimForMessage(strings.TrimSpace(after))
		}
		if !workflowTagRef.MatchString(oldUses[2]) || !workflowTagRef.MatchString(newUses[2]) {
			return name + " moves " + newUses[1] + " to a ref that is not a version tag or commit SHA: " + trimForMessage(newUses[2])
		}
		if !usesNamedByTitle(newUses, pr.Title) {
			return name + " moves " + newUses[1] + " to " + newUses[2] + ", which the pull request title does not name"
		}
		return ""
	}
	oldVersion, newVersion := workflowVersionLine.FindStringSubmatch(before), workflowVersionLine.FindStringSubmatch(after)
	if oldVersion == nil || newVersion == nil || oldVersion[1] != newVersion[1] || !versionKey(newVersion[1]) {
		return name + " changes a line that is not an action ref or a version value: " + trimForMessage(strings.TrimSpace(after))
	}
	if !workflowVersionText.MatchString(oldVersion[2]) || !workflowVersionText.MatchString(newVersion[2]) {
		return name + " sets " + newVersion[1] + " to something that is not a version: " + trimForMessage(newVersion[2])
	}
	if !strings.Contains(strings.ToLower(pr.Title), strings.ToLower(strings.TrimPrefix(newVersion[2], "v"))) {
		return name + " sets " + newVersion[1] + " to " + newVersion[2] + ", which the pull request title does not name"
	}
	return ""
}

// versionKey reports whether key names a version input or variable.
func versionKey(key string) bool {
	lower := strings.ToLower(key)
	return lower == "version" || strings.HasSuffix(lower, "-version") || strings.HasSuffix(lower, "_version")
}

// usesNamedByTitle ties a `uses:` ref change to the pull request: the action's
// `owner/repo` is in the title, or the new tag is, or (for a commit SHA) the
// version in the trailing `# vX` comment is.
func usesNamedByTitle(uses []string, title string) bool {
	title = strings.ToLower(title)
	segments := strings.SplitN(uses[1], "/", 3)
	if strings.Contains(title, strings.ToLower(segments[0]+"/"+segments[1])) {
		return true
	}
	ref := uses[2]
	if len(ref) != 40 || strings.Trim(ref, "0123456789abcdefABCDEF") != "" {
		return strings.Contains(title, strings.ToLower(strings.TrimPrefix(ref, "v")))
	}
	comment := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(strings.SplitN(strings.TrimSpace(uses[3]), " ", 2)[0])), "v")
	return comment != "" && strings.Contains(title, comment)
}
