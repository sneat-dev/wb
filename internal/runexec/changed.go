package runexec

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/quality"
	"os"
	"strings"
)

type ChangedOperations struct {
	Getwd         func() (string, error)
	DefaultBranch func(string) string
	Packages      func(context.Context, string, string) (quality.ChangedPackagesResult, error)
}

func Changed(ctx context.Context, request ChangedRequest) (ChangedResult, error) {
	return (ChangedOperations{os.Getwd, hooks.DetectDefaultBranch, quality.ChangedPackages}).Run(ctx, request)
}
func (ops ChangedOperations) Run(ctx context.Context, request ChangedRequest) (ChangedResult, error) {
	cwd, err := ops.Getwd()
	if err != nil {
		return ChangedResult{}, err
	}
	target := request.Target
	if strings.TrimSpace(target) == "" {
		target = ops.DefaultBranch(cwd)
		if strings.TrimSpace(target) == "" {
			return ChangedResult{MissingTarget: true}, nil
		}
	}
	result, err := ops.Packages(ctx, cwd, target)
	if err != nil {
		return ChangedResult{}, fmt.Errorf("wb run --changed: %w", err)
	}
	return ChangedResult{Argv: append(append([]string(nil), request.Argv...), result.Packages...), Packages: append([]string(nil), result.Packages...), Target: result.Target, MergeBase: result.MergeBase, NoWork: len(result.Packages) == 0}, nil
}
