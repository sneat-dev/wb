package orchestrate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestAutoMergeNodeIdentityContracts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, wantID, wantReason string
		err                            error
	}{
		{name: "identity", body: `{"node_id":"node-7"}`, wantID: "node-7"},
		{name: "transport", err: errors.New("identity unavailable"), wantReason: "read pull request node id: identity unavailable"},
		{name: "malformed", body: `{`, wantReason: "decode pull request node id:"},
		{name: "missing", body: `{}`, wantReason: "pull request response carried no node id"},
		{name: "blank", body: `{"node_id":" \n"}`, wantReason: "pull request response carried no node id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reads := 0
			ctx := autoMergeContractContext(t, githubobserver.Reader{
				Get: func(_ context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
					reads++
					if r.Dir != "" || r.Repository != "acme/app" || r.Target != "" || r.Head != "" || r.FreshWindow != 0 || r.Endpoint != "repos/acme/app/pulls/7" {
						t.Fatalf("request=%+v", r)
					}
					return githubobserver.Response{Body: []byte(tc.body)}, tc.err
				},
				Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
					t.Fatal("identity must not mutate")
					return githubobserver.CommandResponse{}
				},
			})
			id, reason := pullRequestNodeID(ctx, "acme/app", "7")
			if id != tc.wantID || (tc.wantReason == "" && reason != "") || (tc.wantReason != "" && !strings.HasPrefix(reason, tc.wantReason)) || reads != 1 {
				t.Fatalf("id=%q reason=%q reads=%d", id, reason, reads)
			}
		})
	}
}

func TestAutoMergeMutationPinsIdentityHeadAndDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, wantMethod, want string
		response                       githubobserver.CommandResponse
		readErr                        error
	}{
		{name: "merge", method: " merge ", wantMethod: "MERGE"},
		{name: "squash", method: "squash", wantMethod: "SQUASH"},
		{name: "rebase", method: "ReBaSe", wantMethod: "REBASE"},
		{name: "invalid defaults merge", method: "invalid", wantMethod: "MERGE"},
		{name: "read refuses before mutation", readErr: errors.New("read denied"), want: "read pull request node id: read denied"},
		{name: "stderr wins", wantMethod: "MERGE", response: githubobserver.CommandResponse{ExitCode: 1, Stderr: []byte(" stderr "), Stdout: []byte("stdout"), Err: errors.New("transport")}, want: "enable auto-merge: stderr"},
		{name: "stdout fallback", wantMethod: "MERGE", response: githubobserver.CommandResponse{ExitCode: 1, Stderr: []byte(" \n"), Stdout: []byte(" stdout "), Err: errors.New("transport")}, want: "enable auto-merge: stdout"},
		{name: "transport fallback", wantMethod: "MERGE", response: githubobserver.CommandResponse{ExitCode: 1, Err: errors.New("transport")}, want: "enable auto-merge: transport"},
		{name: "nil transport empty", wantMethod: "MERGE", response: githubobserver.CommandResponse{ExitCode: 1}, want: "enable auto-merge: "},
		{name: "exit code remains decision", wantMethod: "MERGE", response: githubobserver.CommandResponse{Err: errors.New("ignored without failure exit")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stages := []string{}
			ctx := autoMergeContractContext(t, githubobserver.Reader{
				Get: func(_ context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
					stages = append(stages, "identity")
					if r.Dir != "" || r.Repository != "acme/app" || r.Endpoint != "repos/acme/app/pulls/7" || r.Target != "" || r.Head != "" || r.FreshWindow != 0 {
						t.Fatalf("request=%+v", r)
					}
					return githubobserver.Response{Body: []byte(`{"node_id":"node-7"}`)}, tc.readErr
				},
				Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
					stages = append(stages, "mutation")
					want := []string{"api", "graphql", "-f", "query=mutation($id:ID!,$method:PullRequestMergeMethod!,$head:GitObjectID!,$subject:String!,$body:String!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:$method,expectedHeadOid:$head,commitHeadline:$subject,commitBody:$body}){pullRequest{autoMergeRequest{enabledAt}}}}", "-f", "id=node-7", "-f", "method=" + tc.wantMethod, "-f", "head=observed-head", "-f", "subject=subject", "-f", "body=body"}
					if dir != "" || !reflect.DeepEqual(args, want) {
						t.Fatalf("dir=%q args=%q", dir, args)
					}
					return tc.response
				},
			})
			got := enablePullRequestAutoMerge(ctx, "acme/app", "7", tc.method, "observed-head", "subject", "body")
			wantStages := []string{"identity", "mutation"}
			if tc.readErr != nil {
				wantStages = wantStages[:1]
			}
			if got != tc.want || !reflect.DeepEqual(stages, wantStages) {
				t.Fatalf("reason=%q stages=%q", got, stages)
			}
		})
	}
}

func TestUpdateBranchMutationPreservesCASAndDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		response   githubobserver.CommandResponse
	}{
		{name: "published head"},
		{name: "stderr wins", response: githubobserver.CommandResponse{ExitCode: 1, Stderr: []byte(" stderr "), Stdout: []byte("stdout"), Err: errors.New("transport")}, want: "update pull request branch: stderr"},
		{name: "stdout fallback", response: githubobserver.CommandResponse{ExitCode: 1, Stderr: []byte(" \n"), Stdout: []byte(" stdout "), Err: errors.New("transport")}, want: "update pull request branch: stdout"},
		{name: "transport fallback", response: githubobserver.CommandResponse{ExitCode: 1, Err: errors.New("transport")}, want: "update pull request branch: transport"},
		{name: "nil transport empty", response: githubobserver.CommandResponse{ExitCode: 1}, want: "update pull request branch: "},
		{name: "exit code remains decision", response: githubobserver.CommandResponse{Err: errors.New("ignored without failure exit")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stages := []string{}
			ctx := autoMergeContractContext(t, githubobserver.Reader{
				Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
					stages = append(stages, "mutation")
					want := []string{"api", "--method", "PUT", "repos/acme/app/pulls/7/update-branch", "-f", "expected_head_sha=observed-head"}
					if dir != "" || !reflect.DeepEqual(args, want) {
						t.Fatalf("dir=%q args=%q", dir, args)
					}
					return tc.response
				},
				Get: func(_ context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
					stages = append(stages, "publication")
					if r.Dir != "" || r.Repository != "acme/app" || r.Endpoint != "repos/acme/app/pulls/7" || r.Target != "" || r.Head != "" || r.FreshWindow != 0 {
						t.Fatalf("request=%+v", r)
					}
					return githubobserver.Response{Body: []byte(`{"number":7,"head":{"sha":"published-head"}}`)}, nil
				},
			})
			head, reason := updatePullRequestBranch(ctx, "acme/app", "7", "observed-head", nil)
			wantHead := "published-head"
			wantStages := []string{"mutation", "publication"}
			if tc.response.ExitCode != 0 {
				wantHead = ""
				wantStages = wantStages[:1]
			}
			if head != wantHead || reason != tc.want || !reflect.DeepEqual(stages, wantStages) {
				t.Fatalf("head=%q reason=%q stages=%q", head, reason, stages)
			}
		})
	}
}

func TestAutoMergeGuardUsesTheExistingPolicyReader(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, branch, detail, want string
		keep, allow                bool
		err                        error
	}{
		{name: "keep precedes allow", keep: true, allow: true, want: "--keep-commits merges a rebuilt branch, not this head"},
		{name: "explicit unfenced", allow: true},
		{name: "policy read refusal", err: errors.New("policy unavailable"), want: "target policy unreadable: read target branch protection for main: policy unavailable"},
		{name: "strict", branch: `{"protected":true,"protection":{"required_status_checks":{"contexts":["build"]}}}`, detail: `{"strict":true,"contexts":["build"]}`},
		{name: "unfenced", branch: `{"protected":false}`, want: "main has no strict up-to-date policy; pass --allow-unfenced to arm anyway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stages := []string{}
			ctx := autoMergeContractContext(t, githubobserver.Reader{
				Get: func(_ context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
					if r.Dir != "" || r.Repository != "acme/app" || r.Target != "main" || r.Head != "" || r.FreshWindow != 0 {
						t.Fatalf("request=%+v", r)
					}
					stages = append(stages, r.Endpoint)
					switch r.Endpoint {
					case "repos/acme/app/branches/main":
						return githubobserver.Response{Body: []byte(tc.branch)}, tc.err
					case "repos/acme/app/branches/main/protection/required_status_checks":
						return githubobserver.Response{Body: []byte(tc.detail)}, nil
					case "repos/acme/app/rules/branches/main?per_page=100":
						return githubobserver.Response{Body: []byte(`[]`)}, nil
					default:
						t.Fatalf("unexpected Get %+v", r)
						return githubobserver.Response{}, nil
					}
				},
			})
			options := PullRequestLandOptions{Repository: "acme/app", AllowUnfenced: tc.allow}
			if tc.keep {
				options.KeepCommits = []string{"commit"}
			}
			got := autoMergeBypassesAGuard(ctx, options, "main")
			wantStages := []string{}
			if !tc.keep && !tc.allow {
				wantStages = append(wantStages, "repos/acme/app/branches/main")
				if tc.err == nil {
					if tc.detail != "" {
						wantStages = append(wantStages, "repos/acme/app/branches/main/protection/required_status_checks")
					}
					wantStages = append(wantStages, "repos/acme/app/rules/branches/main?per_page=100")
				}
			}
			if got != tc.want || !reflect.DeepEqual(stages, wantStages) {
				t.Fatalf("reason=%q stages=%q", got, stages)
			}
		})
	}
}

func autoMergeContractContext(t *testing.T, r githubobserver.Reader) context.Context {
	t.Helper()
	if r.Read == nil {
		r.Read = func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unexpected Read")
			return nil, errors.New("unexpected Read")
		}
	}
	if r.Get == nil {
		r.Get = func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
			t.Fatal("unexpected Get")
			return githubobserver.Response{}, errors.New("unexpected Get")
		}
	}
	if r.Execute == nil {
		r.Execute = func(context.Context, string, ...string) githubobserver.CommandResponse {
			t.Fatal("unexpected Execute")
			return githubobserver.CommandResponse{ExitCode: 1, Err: errors.New("unexpected Execute")}
		}
	}
	return githubobserver.WithReader(context.Background(), r)
}
