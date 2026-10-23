package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestClosingIssueTextContracts(t *testing.T) {
	t.Parallel()
	if got := dedupeIssues(nil); got != nil {
		t.Fatalf("nil input became %v", got)
	}
	empty := []int{}
	if got := dedupeIssues(empty); got == nil || len(got) != 0 {
		t.Fatalf("empty input became %#v", got)
	}
	input := []int{7, 2, 7, 0, -1, 2}
	before := append([]int(nil), input...)
	if got := dedupeIssues(input); !reflect.DeepEqual(got, []int{7, 2, 0, -1}) {
		t.Fatalf("dedupe: %v", got)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("dedupe mutated input")
	}
	if got := closesLines(input); got != "Closes #7\nCloses #2\nCloses #0\nCloses #-1\n\n" {
		t.Fatalf("lines: %q", got)
	}
	if got := withClosesPrefix("body", nil); got != "body" {
		t.Fatalf("empty prefix: %q", got)
	}
	if got := withClosesPrefix("body", []int{7, 7}); got != "Closes #7\n\nbody" {
		t.Fatalf("prefix: %q", got)
	}
	body := "cLoSeS #7\nCloses #2   \nEmbedded Closes #9\nCloses #999999999999999999999999999999\n"
	if got := missingClosesIssues(body, []int{7, 9, 9, 2, 4}); !reflect.DeepEqual(got, []int{9, 4}) {
		t.Fatalf("missing: %v", got)
	}
	if got := missingClosesIssues("Closes #7\n", []int{7}); len(got) != 0 {
		t.Fatalf("already present: %v", got)
	}
	if got := formatClosesFinding(nil); got != "no linked issue" {
		t.Fatalf("empty finding: %q", got)
	}
	if got := formatClosesFinding([]int{7, 2, 7}); got != "closes: #7, #2, #7" {
		t.Fatalf("finding: %q", got)
	}
}

func TestClosingIssuePromptReferencePolicies(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name, prompt string
		want         []int
	}{
		{"empty", "nothing referenced", []int{}},
		{"start and repeated", "#7 then #2 and #7", []int{7, 2}},
		{"repository token", "acme/app#3 /#4 other #5 acme/app #6", []int{5, 6}},
		{"whitespace", "a/b\t#7 a/b\n#8", []int{7, 8}},
		{"color lengths", "#123 #1234 #12345 #123456 #1234567 #12345678 #3b82f6 #00ff00", []int{123, 1234, 12345, 1234567}},
		{"zero overflow boundary", "#0 #0007 #999999999999999999999999999999 #12abc #9!", []int{7, 9}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := SuggestClosesFromPrompt(row.prompt); !reflect.DeepEqual(got, row.want) {
				t.Fatalf("got %v want %v", got, row.want)
			}
		})
	}
	for _, row := range []struct {
		text  string
		start int
		want  bool
	}{
		{"#7", 0, false}, {"acme/app#7", 8, true}, {"acme/app #7", 9, false}, {"abc#7", 3, false},
	} {
		if got := precedingTokenNamesAnotherRepository(row.text, row.start); got != row.want {
			t.Fatalf("token %q at %d: %v", row.text, row.start, got)
		}
	}
	for _, n := range []int{0, 3, 4, 5, 6, 7, 8, 9} {
		if got := looksLikeHexColorLength(n); got != (n == 6 || n == 8) {
			t.Fatalf("color length %d: %v", n, got)
		}
	}
}

func TestClosingIssueGraphQLResponseContracts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("controlled transport failure")
	for _, row := range []struct {
		name, repository, number string
		response                 githubobserver.CommandResponse
		want                     []int
		errorPrefix              string
		calls                    int
	}{
		{name: "repository refusal", repository: "app", number: "7", errorPrefix: `repository "app" must be owner/name`},
		{name: "number refusal", repository: "acme/app", number: "not-number", errorPrefix: `pull request number "not-number":`},
		{name: "ordered wire nodes", repository: "acme/app", number: " 7 ", response: githubobserver.CommandResponse{Stdout: []byte(`{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"nodes":[{"number":9},{"number":2},{"number":9},{"number":0}]}}}}}`)}, want: []int{9, 2, 9, 0}, calls: 1},
		{name: "empty wire", repository: "acme/app", number: " 7 ", response: githubobserver.CommandResponse{Stdout: []byte(`{}`)}, want: []int{}, calls: 1},
		{name: "stderr precedence", repository: "acme/app", number: " 7 ", response: githubobserver.CommandResponse{Err: sentinel, Stderr: []byte(" stderr detail \n"), Stdout: []byte("stdout detail")}, errorPrefix: "read closing issues for acme/app# 7 : stderr detail", calls: 1},
		{name: "stdout fallback", repository: "acme/app", number: " 7 ", response: githubobserver.CommandResponse{Err: sentinel, Stderr: []byte(" \n"), Stdout: []byte(" stdout detail \n")}, errorPrefix: "read closing issues for acme/app# 7 : stdout detail", calls: 1},
		{name: "malformed wire", repository: "acme/app", number: " 7 ", response: githubobserver.CommandResponse{Stdout: []byte(`{"data":`)}, errorPrefix: "decode closing issues for acme/app# 7 :", calls: 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := reviewEvidenceExecuteContext(t, func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
				calls++
				query := "query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){closingIssuesReferences(first:50){nodes{number}}}}}"
				want := []string{"api", "graphql", "-f", "query=" + query, "-f", "owner=acme", "-f", "name=app", "-F", "number=7"}
				if dir != "" || !reflect.DeepEqual(args, want) {
					t.Fatalf("unexpected GraphQL request dir=%q args=%q", dir, args)
				}
				return row.response
			})
			got, err := closingIssuesReferences(ctx, row.repository, row.number)
			if calls != row.calls {
				t.Fatalf("calls %d want %d", calls, row.calls)
			}
			if row.errorPrefix != "" {
				if err == nil || !strings.HasPrefix(err.Error(), row.errorPrefix) || got != nil {
					t.Fatalf("got %v err %v", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, row.want) {
				t.Fatalf("got %#v err %v want %#v", got, err, row.want)
			}
		})
	}
}

func TestClosingIssueAdoptedEditContracts(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"already present", "edit", "refused"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			sentinel := errors.New("controlled edit refusal")
			body := "Closes #7\n\noriginal body"
			issues := []int{7}
			if name != "already present" {
				issues = []int{7, 9, 9}
				var err error
				if name == "refused" {
					err = sentinel
				}
				fake.Expect(func(c runnertest.Call) bool {
					return c.Op == "RunOpts" && c.Dir == "private-worktree" && c.Opts.CaptureCombined && len(c.Opts.Env) > 0 && reflect.DeepEqual(c.Argv(), []string{"gh", "pr", "edit", "https://github.com/acme/app/pull/7", "--repo", "acme/app", "--body", "Closes #9\n\n" + body})
				}, runner.Result{}, err)
			}
			err := applyClosesToAdoptedPullRequest(t.Context(), "private-worktree", "acme/app", "https://github.com/acme/app/pull/7", body, issues, Options{run: fake})
			wantCalls := 1
			if name == "already present" {
				wantCalls = 0
			}
			if fake.CallCount() != wantCalls {
				t.Fatalf("calls %d want %d", fake.CallCount(), wantCalls)
			}
			if name == "refused" {
				if !errors.Is(err, sentinel) {
					t.Fatalf("lost refusal identity: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
