package defaultbranch

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// Each fixture owns fresh mutable provider state; neither the source nor writable target is shared between children.
func workflowServiceFixture(t *testing.T, initiallyArchived bool) (*Service, func() []string) {
	t.Helper()
	service := New()
	archived, Branch := initiallyArchived, "master"
	old, child := strings.Repeat("a", 40), strings.Repeat("b", 40)
	head, workflowDone, renameFailed := old, false, false
	mutations := []string{}
	service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
		switch endpoint {
		case "repos/acme/app":
			return []byte(fmt.Sprintf(`{"id":77,"default_branch":%q,"archived":%t}`, Branch, archived)), nil
		case "repos/acme/app/branches/master":
			if Branch == "main" {
				return nil, errors.New("HTTP 404")
			}
			return []byte(fmt.Sprintf(`{"commit":{"sha":%q}}`, head)), nil
		case "repos/acme/app/branches/main":
			if Branch != "main" {
				return nil, errors.New("HTTP 404")
			}
			return []byte(fmt.Sprintf(`{"commit":{"sha":%q}}`, head)), nil
		case "repos/acme/app/pulls?state=open&head=acme%3Amaster":
			return []byte(`[]`), nil
		case "repos/acme/app/contents/.github/workflows?ref=master":
			return []byte(`[{"path":".github/workflows/ci.yml","sha":"blob","type":"file"}]`), nil
		case "repos/acme/app/git/blobs/blob":
			contents := "name: remaster check\non:\n  push:\n    branches: [ master ]\n  pull_request:\n    branches: [master]\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n"
			if workflowDone {
				contents = "name: remaster check\non:\n  push:\n    branches: [ main ]\n  pull_request:\n    branches: [main]\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n"
			}
			return []byte(fmt.Sprintf(`{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(contents)))), nil
		case "repos/acme/app/commits/" + child:
			return []byte(fmt.Sprintf(`{"parents":[{"sha":%q}]}`, old)), nil
		default:
			if strings.HasSuffix(endpoint, "/pages") || strings.HasSuffix(endpoint, "/protection") {
				return nil, errors.New("HTTP 404")
			}
			if strings.Contains(endpoint, "/rules/branches/") {
				return []byte(`[]`), nil
			}
			return nil, errors.New("unexpected endpoint " + endpoint)
		}
	}
	service.deps.Execute = func(_ context.Context, args ...string) githubobserver.CommandResponse {
		mutation := strings.Join(args, " ")
		mutations = append(mutations, mutation)
		switch mutation {
		case "api --method PATCH repos/acme/app -f archived=false":
			archived = false
		case "api graphql -f query=" + defaultBranchWorkflowMutation + " -F branch[repositoryNameWithOwner]=acme/app -F branch[branchName]=master -F expected=" + old + " -F message[headline]=chore: update default-branch workflow triggers -F additions[][path]=.github/workflows/ci.yml -F additions[][contents]=" + base64.StdEncoding.EncodeToString([]byte("name: remaster check\non:\n  push:\n    branches: [ main ]\n  pull_request:\n    branches: [main]\n  workflow_dispatch:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: curl https://raw.githubusercontent.com/golang/dep/master/install.sh\n")):
			workflowDone, head = true, child
			return githubobserver.CommandResponse{Stdout: []byte(fmt.Sprintf(`{"data":{"createCommitOnBranch":{"commit":{"oid":%q}}}}`, child))}
		case "api --method POST repos/acme/app/branches/master/rename -f new_name=main":
			if renameFailed {
				return githubobserver.CommandResponse{Err: errors.New("rename rejected")}
			}
			Branch = "main"
		case "api --method PATCH repos/acme/app -f archived=true":
			archived = true
		default:
			return githubobserver.CommandResponse{Err: errors.New("unexpected mutation")}
		}
		return githubobserver.CommandResponse{}
	}
	return service, func() []string { return append([]string(nil), mutations...) }
}
