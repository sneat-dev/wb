package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// This invokes the real user-policy/default-inspector composition; family tests
// separately own rendered reports and the absence of an apply path.
func TestBranchRootArchiveTargetUsesTheContextReaderAndRealPreflight(t *testing.T) {
	t.Parallel()
	reads := 0
	ctx := githubobserver.WithReader(t.Context(), githubobserver.Reader{Read: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		reads++
		if dir != "" || !reflect.DeepEqual(args, []string{"api", "repos/acme/backstage-retired"}) {
			t.Fatal(dir, args)
		}
		return []byte(`{"full_name":"acme/backstage-retired","private":false}`), nil
	}})
	cmd := newBranchCmd(testInvocation(t, t.TempDir()))
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"archive-target", "--repo=acme/app", "--format=json"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err, out.String(), stderr.String())
	}
	var plan worktrees.RetiredArchivePlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || reads != 1 || plan.SourceRepository != "acme/app" || plan.ArchiveRepository != "acme/backstage-retired" || plan.Refusal != "archive repository is public" || plan.Outcome != "refused" || plan.RemoteRefRename || plan.WorktreeDeletion || plan.LocalQuarantine != "preserved" || plan.WorkLogExport != "not_started" {
		t.Fatal(plan, reads, err, out.String())
	}
}
