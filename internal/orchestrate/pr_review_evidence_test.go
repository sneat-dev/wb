package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func reviewEvidenceExecuteContext(t *testing.T, execute func(context.Context, string, ...string) githubobserver.CommandResponse) context.Context {
	t.Helper()
	return githubobserver.WithReader(t.Context(), githubobserver.Reader{
		Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
			t.Fatal("unexpected Get or GetPages route")
			return githubobserver.Response{}, nil
		},
		Read: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unexpected Read route")
			return nil, nil
		},
		Execute: execute,
	})
}

func TestPostReviewCommentRequiresAUsableReturnedURL(t *testing.T) {
	t.Parallel()
	identity := ReviewerIdentity{Model: "opus", Harness: "codex", Session: "review-session"}
	const head = "0123456789012345678901234567890123456789"
	for _, row := range []struct {
		name            string
		response        githubobserver.CommandResponse
		want, wantError string
	}{
		{name: "stderr failure", response: githubobserver.CommandResponse{Err: errors.New("execution refused"), Stderr: []byte(" stderr detail "), Stdout: []byte("ignored stdout")}, wantError: "post review comment on acme/app#7: stderr detail"},
		{name: "stdout fallback", response: githubobserver.CommandResponse{Err: errors.New("execution refused"), Stderr: []byte("  "), Stdout: []byte(" stdout detail ")}, wantError: "post review comment on acme/app#7: stdout detail"},
		{name: "malformed reply", response: githubobserver.CommandResponse{Stdout: []byte("{")}, wantError: "decode posted review comment on acme/app#7:"},
		{name: "absent URL", response: githubobserver.CommandResponse{Stdout: []byte(`{}`)}, wantError: "GitHub returned no comment URL"},
		{name: "blank URL", response: githubobserver.CommandResponse{Stdout: []byte(`{"html_url":"  "}`)}, wantError: "GitHub returned no comment URL"},
		{name: "usable URL", response: githubobserver.CommandResponse{Stdout: []byte(`{"html_url":"https://github.com/acme/app/pull/7#issuecomment-42"}`)}, want: "https://github.com/acme/app/pull/7#issuecomment-42"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := reviewEvidenceExecuteContext(t, func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
				calls++
				want := []string{"api", "--method", "POST", "repos/acme/app/issues/7/comments", "-f", "body=" + reviewCommentBody(identity, head, "exact review text")}
				if dir != "" || !reflect.DeepEqual(args, want) {
					t.Fatalf("post binding dir=%q argv=%v want %v", dir, args, want)
				}
				return row.response
			})
			url, err := postReviewComment(ctx, "acme/app", "7", identity, head, "exact review text")
			if calls != 1 || url != row.want {
				t.Fatalf("post=%q/%v calls=%d", url, err, calls)
			}
			if row.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), row.wantError) {
				t.Fatalf("error=%v want %q", err, row.wantError)
			}
		})
	}
}

func TestReviewCommentFetchFailureRemainsUnbound(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name     string
		response githubobserver.CommandResponse
		want     string
		ok       bool
	}{
		{name: "execution error", response: githubobserver.CommandResponse{Err: errors.New("execution refused"), Stdout: []byte(`{"body":"untrusted"}`)}},
		{name: "malformed reply", response: githubobserver.CommandResponse{Stdout: []byte("{")}},
		{name: "valid body", response: githubobserver.CommandResponse{Stdout: []byte(`{"body":"exact fetched text"}`)}, want: "exact fetched text", ok: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := reviewEvidenceExecuteContext(t, func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
				calls++
				if dir != "" || !reflect.DeepEqual(args, []string{"api", "repos/acme/app/issues/comments/42"}) {
					t.Fatalf("fetch binding dir=%q argv=%v", dir, args)
				}
				return row.response
			})
			body, ok, cross := fetchIssueCommentBody(ctx, "https://github.com/acme/app/issues/7#issuecomment-42", "acme/app", "7")
			if body != row.want || ok != row.ok || cross || calls != 1 {
				t.Fatalf("fetch=%q/%v/%v calls=%d", body, ok, cross, calls)
			}
			if head, err := namedReviewedHead(ctx, "https://github.com/acme/app/issues/7#issuecomment-42", approvalKindURL, "acme/app", "7"); head != "" || err != nil || calls != 2 {
				t.Fatalf("head binding=%q/%v calls=%d", head, err, calls)
			}
		})
	}
	// Neither a foreign repository nor an unsupported evidence kind may fetch.
	ctx := reviewEvidenceExecuteContext(t, func(context.Context, string, ...string) githubobserver.CommandResponse {
		t.Fatal("unbound or foreign evidence must not fetch")
		return githubobserver.CommandResponse{}
	})
	if body, ok, cross := fetchIssueCommentBody(ctx, "https://github.com/other/app/issues/7#issuecomment-42", "acme/app", "7"); body != "" || ok || !cross {
		t.Fatalf("cross repository=%q/%v/%v", body, ok, cross)
	}
	if head, err := namedReviewedHead(ctx, "https://example.test/review", approvalKindURL, "acme/app", "7"); head != "" || err != nil {
		t.Fatalf("unsupported comment URL=%q/%v", head, err)
	}
	if head, err := namedReviewedHead(ctx, filepath.Join(t.TempDir(), "absent-review.md"), approvalKindFile, "acme/app", "7"); head != "" || err != nil {
		t.Fatalf("absent approval file=%q/%v", head, err)
	}
	for _, kind := range []approvalKind{approvalKindEmpty, approvalKindCI, approvalKindIdentity} {
		if head, err := namedReviewedHead(ctx, "https://github.com/acme/app/issues/7#issuecomment-42", kind, "acme/app", "7"); head != "" || err != nil {
			t.Fatalf("unsupported kind %d=%q/%v", kind, head, err)
		}
	}
}

func TestResolveReviewProofCheckoutRefusesAbsentOrNonDirectoryCanonical(t *testing.T) {
	t.Parallel()
	for _, makeFile := range []bool{false, true} {
		root := t.TempDir()
		canonical := filepath.Join(root, "acme", "app")
		if makeFile {
			if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(canonical, []byte("not a checkout"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		view := githubchecks.PullRequestView{}
		view.Head.Ref, view.Base.Ref = "feature", "main"
		if path, branch, found := resolveReviewProofCheckout(t.Context(), PullRequestLandOptions{ProjectsRoot: root, Repository: "acme/app"}, view); path != "" || branch != "" || found {
			t.Fatalf("unavailable canonical file=%v: %q/%q/%v", makeFile, path, branch, found)
		}
	}
}
