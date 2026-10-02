//go:build e2e

package canonicalrescue

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestE2EInspectPropagatesHeadReadFailure(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	failure := errors.New("HEAD disappeared")
	query := func(ctx context.Context, root string, arguments ...string) (string, error) {
		if len(arguments) == 2 && arguments[0] == "rev-parse" && arguments[1] == "HEAD" {
			return "", failure
		}
		return git(ctx, root, arguments...)
	}
	if report, err := inspectGit(context.Background(), repositories.Canonical, options(repositories), query); !errors.Is(err, failure) || report.Path != "" {
		t.Fatalf("inspection = %+v, %v", report, err)
	}
}

func TestE2EAttestedPushRejectsCommitReadFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"inspect", "branch", "parents", "tree"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			repositories := newFixture(t)
			dirtyTheClone(t, repositories)
			ctx := context.Background()
			report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/read-boundary"))
			if err != nil {
				t.Fatal(err)
			}
			captured, err := Capture(ctx, report)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("commit object became unavailable")
			query := func(ctx context.Context, root string, arguments ...string) (string, error) {
				if operation == "branch" && arguments[0] == "branch" || operation == "inspect" && arguments[0] == "rev-parse" && arguments[len(arguments)-1] == "HEAD" || operation == "parents" && arguments[0] == "rev-list" || operation == "tree" && arguments[0] == "rev-parse" && strings.HasSuffix(arguments[len(arguments)-1], "^{tree}") {
					return "", failure
				}
				return git(ctx, root, arguments...)
			}
			err = verifyAttestedPushGit(ctx, repositories.Canonical, repositories.ProjectsRoot, captured.RescueBranch, captured.RescueCommit, strings.NewReader(lgCovPushInput(captured.RescueBranch, captured.RescueCommit)), query)
			if !errors.Is(err, failure) {
				t.Fatalf("attestation = %v", err)
			}
		})
	}
}
