package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestStrandedRecoveryRefusesUnavailableLocalIdentityBeforeHostedProof(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nil", "missing pull request", "already landed", "missing candidate path", "existing candidate", "invalid receipt", "native inspection error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := acknowledgementReadReceipt(t)
			r.Status, r.Phase, r.PullRequest, r.PublishedCandidateSHA = WorktreeMergePublished, WorktreeMergePhaseLand, "91", r.Candidate.SHA
			input := &r
			switch mode {
			case "nil":
				input = nil
			case "missing pull request":
				r.PullRequest = ""
			case "already landed":
				r.LandingSHA = r.Candidate.SHA
			case "missing candidate path":
				r.Candidate.Worktree = ""
			case "existing candidate":
				if err := os.MkdirAll(r.Candidate.Worktree, 0700); err != nil {
					t.Fatal(err)
				}
			case "invalid receipt":
				r.Status = WorktreeMergePrepared
			case "native inspection error":
				parent := filepath.Join(t.TempDir(), "owned-file")
				if err := os.WriteFile(parent, []byte("owned file"), 0600); err != nil {
					t.Fatal(err)
				}
				r.Candidate.Worktree = filepath.Join(parent, "missing-child")
				_, physical := os.Lstat(r.Candidate.Worktree)
				if physical == nil || !errors.Is(physical, syscall.ENOTDIR) {
					t.Fatalf("owned filesystem did not produce ENOTDIR: %v", physical)
				}
			}
			before := r
			reads := 0
			forbidden := errors.New("hosted observation forbidden before eligibility")
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{
				Read: func(context.Context, string, ...string) ([]byte, error) { reads++; return nil, forbidden },
				Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
					reads++
					return githubobserver.Response{}, forbidden
				},
				Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
					reads++
					return githubobserver.CommandResponse{Err: forbidden}
				},
			})
			recovered, err := recoverAlreadyMergedPublishedWorktreeMerge(ctx, input)
			wantError := mode == "invalid receipt" || mode == "native inspection error"
			if recovered || (err != nil) != wantError || reads != 0 || !reflect.DeepEqual(before, r) {
				t.Fatalf("%s recovered=%t err=%v reads=%d mutated=%t", mode, recovered, err, reads, !reflect.DeepEqual(before, r))
			}
			if mode == "native inspection error" {
				var physical *os.PathError
				if !errors.As(err, &physical) || !errors.Is(err, syscall.ENOTDIR) || physical.Path != r.Candidate.Worktree || !strings.HasPrefix(err.Error(), "inspect published candidate worktree: ") {
					t.Fatalf("inspection cause/path=%v", err)
				}
			}
		})
	}
}

// Hosted tree DTOs prove decoding, request shape and error order only; native
// equality and ancestry witnesses remain in the separately selected e2e tests.
func TestStrandedCommitTreeProtocolRetainsExactReadAndDecodeFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"read", "decode", "missing", "blank", "trimmed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			cause := errors.New("private commit read refused")
			calls := 0
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
				calls++
				if req.Dir != "" || req.Repository != "acme/app" || req.Target != "exact-sha" || req.Head != "" || req.Endpoint != "repos/acme/app/git/commits/exact-sha" || req.FreshWindow != 0 {
					t.Fatalf("commit request=%+v", req)
				}
				var body string
				switch mode {
				case "read":
					return githubobserver.Response{}, cause
				case "decode":
					body = "{"
				case "missing":
					body = `{"tree":{}}`
				case "blank":
					body = `{"tree":{"sha":"  \n\t "}}`
				case "trimmed":
					body = `{"tree":{"sha":"  native-protocol-tree \n "}}`
				}
				return githubobserver.Response{Body: []byte(body)}, nil
			}})
			tree, err := commitTreeSHA(ctx, "acme/app", "exact-sha")
			if calls != 1 || (err == nil) != (mode == "trimmed") || (mode != "trimmed" && tree != "") {
				t.Fatalf("commit %s tree=%q err=%v calls=%d", mode, tree, err, calls)
			}
			switch mode {
			case "read":
				if !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "read commit exact-sha: ") {
					t.Fatalf("read cause=%v", err)
				}
			case "decode":
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "decode commit exact-sha: ") {
					t.Fatalf("decode cause=%v", err)
				}
			case "missing", "blank":
				if err.Error() != "GitHub commit exact-sha returned no tree SHA" {
					t.Fatalf("empty tree diagnostic=%v", err)
				}
			case "trimmed":
				if tree != "native-protocol-tree" {
					t.Fatalf("trimmed tree=%q", tree)
				}
			}
		})
	}
}

func TestStrandedTreeComparisonPreservesReadOrderAndEmptyFailureResult(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"first read", "second read", "equal", "unequal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			cause := errors.New("private tree observation")
			var calls []string
			ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Get: func(_ context.Context, req githubobserver.GetRequest) (githubobserver.Response, error) {
				calls = append(calls, req.Target)
				sha := "merge-sha"
				if len(calls) == 2 {
					sha = "candidate-sha"
				}
				if len(calls) > 2 || req.Dir != "" || req.Repository != "acme/app" || req.Target != sha || req.Head != "" || req.Endpoint != "repos/acme/app/git/commits/"+sha || req.FreshWindow != 0 {
					t.Fatalf("ordered tree request=%+v calls=%v", req, calls)
				}
				if mode == "first read" && len(calls) == 1 || mode == "second read" && len(calls) == 2 {
					return githubobserver.Response{}, cause
				}
				tree := "shared-tree"
				if mode == "unequal" && len(calls) == 2 {
					tree = "different-tree"
				}
				return githubobserver.Response{Body: []byte(`{"tree":{"sha":"` + tree + `"}}`)}, nil
			}})
			equal, tree, err := candidateTreeIdenticalToMergeCommit(ctx, "acme/app", "merge-sha", "candidate-sha")
			if mode == "first read" || mode == "second read" {
				expected := 1
				if mode == "second read" {
					expected = 2
				}
				if equal || tree != "" || !errors.Is(err, cause) || len(calls) != expected {
					t.Fatalf("comparison failure equal=%t tree=%q err=%v calls=%v", equal, tree, err, calls)
				}
				if !strings.HasPrefix(err.Error(), "read commit "+calls[len(calls)-1]+": ") {
					t.Fatalf("failure belongs to wrong SHA: %v", err)
				}
			} else if err != nil || equal != (mode == "equal") || tree != "shared-tree" || !reflect.DeepEqual(calls, []string{"merge-sha", "candidate-sha"}) {
				t.Fatalf("comparison equal=%t tree=%q err=%v calls=%v", equal, tree, err, calls)
			}
		})
	}
}
