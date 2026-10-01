package worktreeclaims

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktreejournal"
	"strconv"
	"strings"
)

type GitEvidencePorts struct {
	Git func(context.Context, string, ...string) (string, error)
}

func ObserveUsage(discriminator string, input, output *int64, cost *float64, currency, providerRef string) (*worktreejournal.LocalUsageEvidence, error) {
	discriminator = strings.TrimSpace(discriminator)
	if discriminator == "" {
		if input == nil && output == nil && cost == nil && currency == "" && providerRef == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("--usage-discriminator is required when usage fields are supplied")
	}
	switch discriminator {
	case "provider_reported", "estimated", "unavailable":
	default:
		return nil, fmt.Errorf("usage discriminator must be provider_reported, estimated, or unavailable")
	}
	usage := &worktreejournal.LocalUsageEvidence{
		Discriminator: discriminator,
		InputTokens:   input,
		OutputTokens:  output,
		EstimatedCost: cost,
		Currency:      strings.TrimSpace(currency),
		ProviderRef:   strings.TrimSpace(providerRef),
	}
	if input != nil || output != nil {
		total := int64(0)
		if input != nil {
			total += *input
		}
		if output != nil {
			total += *output
		}
		usage.TotalTokens = &total
	}
	return usage, nil
}

func (p GitEvidencePorts) AheadBehind(ctx context.Context, worktree, targetSHA string) (int, int, error) {
	out, err := p.Git(ctx, worktree, "rev-list", "--left-right", "--count", "HEAD..."+targetSHA)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected ahead/behind output %q", out)
	}
	ahead, err1 := strconv.Atoi(fields[0])
	behind, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("parse ahead/behind %q", out)
	}
	return ahead, behind, nil
}

func (p GitEvidencePorts) BranchPublished(ctx context.Context, worktree string) (bool, error) {
	branch, err := p.Git(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return false, err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, nil
	}
	_, err = p.Git(ctx, worktree, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	return err == nil, nil
}

func MustCountOutbox(worktree string, countLocalOutbox func(string) (int, error)) int {
	count, _ := countLocalOutbox(worktree)
	return count
}

func PtrLocalGit(evidence worktreejournal.LocalGitEvidence) *worktreejournal.LocalGitEvidence {
	copyEvidence := evidence
	return &copyEvidence
}
