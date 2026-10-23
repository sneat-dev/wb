package defaultbranch

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestDefaultBranchWorkflowCommitFailureMatrix(t *testing.T) {
	t.Parallel()
	service := New()

	for _, test := range []struct {
		name, mode, want string
	}{
		{"invalid repository", "invalid", "invalid repository observation"},
		{"source changed", "source", "workflow source changed after planning"},
		{"planned bytes changed", "bytes", "workflow bytes changed after planning"},
		{"pending receipt unavailable", "pending receipt", "persist pending workflow commit"},
		{"mutation left source head unchanged", "head unchanged", "did not produce a verified new source head"},
		{"successful response lacks exact OID", "response", "lacks the exact created commit OID"},
		{"post-read bytes differ", "post bytes", "did not prove every replacement"},
		{"commit parent read fails", "parent read", "did not prove the expected parent"},
		{"commit parent differs", "parent mismatch", "did not prove the expected parent"},
		{"verified receipt unavailable", "final receipt", "persist verified workflow commit"},
	} {
		//nolint:paralleltest // Rows replace callbacks or provider state on the same parent-owned policy Service.
		t.Run(test.name, func(t *testing.T) {
			oldRead, oldExecute := service.deps.Read, service.deps.Execute
			t.Cleanup(func() { service.deps.Read, service.deps.Execute = oldRead, oldExecute })
			oldHead, newHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
			original, rewritten := "on:\n  push:\n    branches:\n      - master\n", "on:\n  push:\n    branches:\n      - main\n"
			committed, mutations, checkpoints := false, 0, 0
			service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
				switch endpoint {
				case "repos/acme/app":
					Branch := "master"
					if test.mode == "source" {
						Branch = "main"
					}
					return []byte(`{"id":1,"default_branch":"` + Branch + `"}`), nil
				case "repos/acme/app/branches/master":
					head := oldHead
					if committed {
						head = newHead
					}
					return []byte(`{"commit":{"sha":"` + head + `"}}`), nil
				case "repos/acme/app/branches/main", "repos/acme/app/pages", "repos/acme/app/branches/master/protection":
					return nil, errors.New("HTTP 404")
				case "repos/acme/app/pulls?state=open&head=acme%3Amaster", "repos/acme/app/rules/branches/master?per_page=100":
					return []byte(`[]`), nil
				case "repos/acme/app/contents/.github/workflows?ref=master":
					return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
				case "repos/acme/app/git/blobs/blob":
					contents := original
					if committed {
						contents = rewritten
						if test.mode == "post bytes" {
							contents = "on:\n  push:\n    branches: [main, topic]\n"
						}
					}
					return []byte(`{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte(contents)) + `"}`), nil
				case "repos/acme/app/commits/" + newHead:
					if test.mode == "parent read" {
						return nil, errors.New("commit endpoint unavailable")
					}
					parent := oldHead
					if test.mode == "parent mismatch" {
						parent = strings.Repeat("c", 40)
					}
					return []byte(`{"parents":[{"sha":"` + parent + `"}]}`), nil
				default:
					t.Fatalf("unmodelled workflow endpoint %q", endpoint)
					return nil, nil
				}
			}
			service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
				mutations++
				if len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
					t.Fatalf("unexpected workflow mutation %q", args)
				}
				if test.mode != "head unchanged" {
					committed = true
				}
				if test.mode == "response" {
					return githubobserver.CommandResponse{Stdout: []byte(`{"data":{}}`)}
				}
				return githubobserver.CommandResponse{Stdout: []byte(`{"data":{"createCommitOnBranch":{"commit":{"oid":"` + newHead + `"}}}}`)}
			}
			planned := Repository{Repository: "acme/app", Desired: "main", ObservedDefault: "master", OldHead: oldHead, Disposition: "drift", WorkflowFiles: []Workflow{{Path: ".github/workflows/ci.yml", BlobBefore: "blob", SHA256Before: defaultBranchDigest([]byte(original)), SHA256After: defaultBranchDigest([]byte(rewritten)), contents: original, rewritten: rewritten}}}
			if test.mode == "invalid" {
				planned.Repository = "invalid"
			}
			if test.mode == "bytes" {
				planned.WorkflowFiles[0].SHA256Before = strings.Repeat("0", 64)
			}
			result := service.applyDefaultBranchWorkflowTriggers(context.Background(), planned, func(Repository) error {
				checkpoints++
				if test.mode == "pending receipt" && checkpoints == 1 || test.mode == "final receipt" && checkpoints == 2 {
					return errors.New("injected receipt failure")
				}
				return nil
			})
			if result.Disposition != "error" && result.Disposition != "blocked" || !strings.Contains(result.Error, test.want) {
				t.Fatalf("mode %q: result=%+v, want %q (mutations=%d checkpoints=%d)", test.mode, result, test.want, mutations, checkpoints)
			}
			if (test.mode == "invalid" || test.mode == "source" || test.mode == "bytes" || test.mode == "pending receipt") && mutations != 0 {
				t.Fatalf("mode %q mutated before required proof: %d calls", test.mode, mutations)
			}
		})
	}
}
