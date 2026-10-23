package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func TestIntegrationHeadReasonDecidesOnlyForThisRepositorysHead(t *testing.T) {
	t.Parallel()
	view := githubchecks.PullRequestView{}
	view.Head.Ref, view.Base.Ref = "feature/x", "main"
	options := PullRequestLandOptions{Repository: "acme/app", sourceCommits: []SourceCommit{{SHA: "abc", Subject: "c"}},
		pullRequestsOnto: func(context.Context, string, string) (int, error) { return 0, errors.New("must not be asked") }}
	// A fork's head, or no head repository at all, is never retired here.
	if reason := integrationHeadReason(context.Background(), options, view, "7", nil); reason != "" {
		t.Fatalf("fork head reason = %q", reason)
	}
	view.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{FullName: "acme/app"}
	if reason := integrationHeadReason(context.Background(), options, view, "7", nil); !strings.Contains(reason, "could not be listed") {
		t.Fatalf("failed read reason = %q", reason)
	}
	options.pullRequestsOnto = func(context.Context, string, string) (int, error) { return 0, nil }
	if reason := integrationHeadReason(context.Background(), options, view, "7", nil); reason != "" {
		t.Fatalf("ordinary head reason = %q", reason)
	}
}
