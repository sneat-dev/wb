package streamabsorb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/streamsync"
)

// ExecGit implements absorb's Git surface on top of the sync engine's, so the
// two verbs share one rebase implementation rather than two that can drift.
type ExecGit struct {
	streamsync.ExecGit
}

// CommitsNotIn implements Git by patch identity.
//
// `git cherry` answers which commits the upstream does not already carry AS
// PATCHES, which is the right question: the agent branch is rebased constantly,
// so an ancestry test would report work as new that the stream already has.
func (git ExecGit) CommitsNotIn(ctx context.Context, dir, branch, upstream string) ([]Commit, error) {
	out, err := git.Run(ctx, dir, "cherry", upstream, branch)
	if err != nil {
		return nil, fmt.Errorf("compare %s against %s: %w", branch, upstream, err)
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+ ") {
			continue
		}
		if sha := strings.TrimSpace(strings.TrimPrefix(line, "+ ")); sha != "" {
			shas = append(shas, sha)
		}
	}
	if len(shas) == 0 {
		return nil, nil
	}
	patchIDs, err := git.patchIDs(ctx, dir, upstream, branch)
	if err != nil {
		return nil, err
	}
	commits := make([]Commit, 0, len(shas))
	for _, sha := range shas {
		subject, err := git.Run(ctx, dir, "log", "-1", "--format=%s", sha)
		if err != nil {
			return nil, err
		}
		files, err := git.Run(ctx, dir, "show", "--name-only", "--format=", sha)
		if err != nil {
			return nil, err
		}
		commits = append(commits, Commit{
			SHA: sha, Subject: strings.TrimSpace(subject),
			PatchID: patchIDs[sha], Files: nonEmptyLines(files),
		})
	}
	return commits, nil
}

func (git ExecGit) patchIDs(ctx context.Context, dir, upstream, branch string) (map[string]string, error) {
	patch, err := git.Run(ctx, dir, "log", "--patch", "--no-color", "--no-merges", "--format=commit %H", upstream+".."+branch)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(patch) == "" {
		return map[string]string{}, nil
	}
	out, err := git.RunWithInput(ctx, dir, patch, "patch-id", "--stable")
	if err != nil {
		return nil, err
	}
	identities := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 {
			identities[fields[1]] = fields[0]
		}
	}
	return identities, nil
}

// SquashOnto implements Git.
//
// `reset --soft` to the upstream keeps every change in the index and then
// writes exactly one commit, which is what "squash to one commit" means. It
// cannot produce a merge commit by construction.
func (git ExecGit) SquashOnto(ctx context.Context, dir, branch, upstream, message string) (string, error) {
	if err := git.Checkout(ctx, dir, branch); err != nil {
		return "", err
	}
	if _, err := git.Run(ctx, dir, "reset", "--soft", upstream); err != nil {
		return "", fmt.Errorf("collapse %s onto %s: %w", branch, upstream, err)
	}
	staged, err := git.Run(ctx, dir, "diff", "--cached", "--name-only")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(staged) == "" {
		return "", fmt.Errorf("%s has no change relative to %s after the rebase", branch, upstream)
	}
	if _, err := git.Run(ctx, dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return git.Head(ctx, dir, "HEAD")
}

// BuildCheck proves one kept commit compiles.
//
// Keeping several commits is only better than squashing if each one is
// individually usable; a kept commit that does not build makes the history
// less bisectable than one squashed commit would have been.
func (git ExecGit) BuildCheck(ctx context.Context, dir, sha string) error {
	head, err := git.Head(ctx, dir, "HEAD")
	if err != nil {
		return err
	}
	defer func() { _ = git.ResetHard(ctx, dir, head) }()
	if _, err := git.Run(ctx, dir, "checkout", "--detach", sha); err != nil {
		return err
	}
	if _, err := git.RunTool(ctx, dir, "go", "build", "./..."); err != nil {
		return err
	}
	return nil
}

func nonEmptyLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

var _ = time.Second
