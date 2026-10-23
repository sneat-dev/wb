package landingcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sneat-dev/wb/internal/locallink"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type Refusal struct{ Message string }

func (r *Refusal) Error() string { return r.Message }
func CheckWorktrees(projectsRoot string, paths []string) error {
	return checkWorktrees(projectsRoot, paths, streams.Open, locallink.HasLiveLink)
}
func checkWorktrees(projectsRoot string, worktrees []string, open func(string) (*streams.Store, error), live func(locallink.LiveLinkStore, string) ([]locallink.LiveLink, error)) error {
	store, err := open(projectsRoot)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		links, err := live(store, worktree)
		if err != nil {
			return err
		}
		if len(links) == 0 {
			continue
		}
		return &Refusal{Message: locallink.RefusalMessage(worktree, links)}
	}
	return nil
}

func CheckReceipt(projectsRoot string, receiptPath string) error {
	worktrees, err := worktreeMergeReceiptWorktrees(receiptPath)
	if err != nil {
		// A path that is a worktree rather than a receipt is the documented
		// second form of the argument; guard it directly.
		if info, statErr := os.Stat(receiptPath); statErr == nil && info.IsDir() {
			return CheckWorktrees(projectsRoot, []string{receiptPath})
		}
		return err
	}
	return CheckWorktrees(projectsRoot, worktrees)
}

func worktreeMergeReceiptWorktrees(receiptPath string) ([]string, error) {
	contents, err := os.ReadFile(receiptPath)
	if err != nil {
		return nil, err
	}
	var receipt struct {
		Sources []struct {
			Worktree string `json:"worktree"`
		} `json:"sources"`
		Candidate struct {
			Worktree string `json:"worktree"`
		} `json:"candidate"`
	}
	if err := json.Unmarshal(contents, &receipt); err != nil {
		return nil, fmt.Errorf("read merge receipt %s: %w", receiptPath, err)
	}
	seen := map[string]bool{}
	var worktrees []string
	for _, source := range receipt.Sources {
		if source.Worktree != "" && !seen[source.Worktree] {
			seen[source.Worktree] = true
			worktrees = append(worktrees, source.Worktree)
		}
	}
	if receipt.Candidate.Worktree != "" && !seen[receipt.Candidate.Worktree] {
		worktrees = append(worktrees, receipt.Candidate.Worktree)
	}
	return worktrees, nil
}

func CheckRepository(projectsRoot, repository string) error {
	return checkRepository(projectsRoot, repository, streams.Open)
}
func checkRepository(projectsRoot, repository string, open func(string) (*streams.Store, error)) error {
	store, err := open(projectsRoot)
	if err != nil {
		return err
	}
	stream, found, unreadable, err := store.RepositoryStream(repository)
	if err != nil {
		return err
	}
	if len(unreadable) > 0 {
		// A stream WB cannot read might be the one holding a live link to this
		// repository. "I could not tell" must not be spelled the same way as
		// "there is no link", so the guard says what it could not read and
		// stops — the same rule CheckWorktrees applies to a store it
		// cannot open.
		names := make([]string, 0, len(unreadable))
		for _, entry := range unreadable {
			names = append(names, entry.Name+" ("+entry.Reason+")")
		}
		return &Refusal{Message: "cannot tell whether " + repository +
			" holds a live local link: these streams are unreadable — " + strings.Join(names, "; ") +
			"; fix or remove them, then rerun"}
	}
	if !found {
		// Outside every stream a hand-written go.work is still a live link, and
		// it is the signal stream state cannot see. Guard every WB worktree of
		// this repository directly.
		return checkRepositoryWorktrees(projectsRoot, repository)
	}
	worktrees := make([]string, 0, len(stream.Members)+len(stream.LinkedConsumers))
	for _, member := range stream.Members {
		if member.Repository == repository && member.Worktree != "" {
			worktrees = append(worktrees, member.Worktree)
		}
	}
	// A repository admitted only as a linked consumer holds no membership row,
	// but its Links are exactly the live local links this guard exists to
	// catch — missing them here would let a repository dodge the guard just
	// by joining as a consumer instead of a member.
	for _, consumer := range stream.LinkedConsumers {
		if consumer.Repository == repository && consumer.Worktree != "" {
			worktrees = append(worktrees, consumer.Worktree)
		}
	}
	return CheckWorktrees(projectsRoot, worktrees)
}

func checkRepositoryWorktrees(projectsRoot, repository string) error {
	return checkRepositoryWorktreesWith(projectsRoot, repository, worktrees.ListWithDiagnostics)
}
func checkRepositoryWorktreesWith(projectsRoot, repository string, list func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error)) error {
	listed, err := list(context.Background(), worktrees.ListOptions{
		ProjectsRoot: projectsRoot,
		Filter:       repository,
	})
	if err != nil {
		return err
	}
	paths := make([]string, 0, 4)
	for _, entry := range listed.Results {
		if entry.Repository == repository {
			paths = append(paths, entry.WorktreeDir)
		}
	}
	return CheckWorktrees(projectsRoot, paths)
}
