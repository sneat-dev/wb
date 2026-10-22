package orchestrate

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// GitHub's head_sha run listing is eventually consistent (#877). The direct-CI
// deferral must judge the newest exact-head run, so it recovers a run the
// listing omitted: an omitted newer failure refuses, and an omitted success
// still qualifies.
func TestDirectCIContractJudgesRunsTheListingOmitted(t *testing.T) {
	t.Parallel()
	const head = "0123456789012345678901234567890123456789"
	run := func(id, suite int, created, conclusion string) string {
		return `{"id":` + strconv.Itoa(id) + `,"workflow_id":300,"head_sha":"` + head + `","head_branch":"integration","event":"pull_request","status":"completed","conclusion":"` + conclusion + `","created_at":"` + created + `","check_suite_id":` + strconv.Itoa(suite) + `,"pull_requests":[{"number":17,"base":{"ref":"main"}}]}`
	}
	for _, tc := range []struct{ name, listed, recovered, want string }{
		{"omitted newer failure refuses", `{"total_count":1,"workflow_runs":[` + run(15, 305, "2026-09-27T09:00:00Z", "success") + `]}`, run(16, 306, "2026-09-27T09:05:00Z", "failure"), "no pull_request run"},
		{"omitted success qualifies", `{"total_count":0,"workflow_runs":[]}`, run(16, 306, "2026-09-27T09:05:00Z", "success"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			answers := map[string]string{
				"repos/acme/app/pulls/17":                                        `{"number":17,"state":"open","head":{"ref":"integration","sha":"` + head + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`,
				"repos/acme/app/git/ref/heads/integration":                       `{"object":{"sha":"` + head + `"}}`,
				"repos/acme/app/branches/main":                                   `{"protected":true,"protection":{"required_status_checks":{"contexts":["Required checks passed"]}}}`,
				"repos/acme/app/branches/main/protection/required_status_checks": `{"strict":true,"contexts":["Required checks passed"],"checks":[]}`,
				"repos/acme/app/rules/branches/main?per_page=100":                `[]`,
				"repos/acme/app/actions/workflows/go-ci.yml":                     `{"id":300,"name":"Go CI","path":".github/workflows/go-ci.yml","state":"active"}`,
				"repos/acme/app/commits/" + head + "/check-runs?per_page=100":    `{"total_count":1,"check_runs":[{"id":91,"name":"Required checks passed","app":{"slug":"github-actions"},"check_suite":{"id":306}}]}`,
				"repos/acme/app/actions/runs?head_sha=" + head + "&per_page=100": tc.listed,
				"repos/acme/app/actions/jobs/91":                                 `{"run_id":16}`,
				"repos/acme/app/actions/runs/16":                                 tc.recovered,
			}
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: func(_ context.Context, request githubobserver.GetRequest) (githubobserver.Response, error) {
				body, ok := answers[request.Endpoint]
				if !ok {
					t.Errorf("unexpected read: %+v", request)
				}
				return githubobserver.Response{Body: []byte(body)}, nil
			}})
			contract, err := resolveWorktreeMergeDirectCIContract(ctx, "acme/app", "integration", "17")
			if tc.want == "" {
				if err != nil || contract == nil || contract.WorkflowID != 300 || contract.PullRequestNumber != 17 {
					t.Fatalf("contract=%+v err=%v", contract, err)
				}
				return
			}
			if contract != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("contract=%+v err=%v, want %q", contract, err, tc.want)
			}
		})
	}
}
