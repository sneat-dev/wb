package cmdworktree

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRetirementCleanupQuietRetainsMutationsAndEveryJSONArtifact(t *testing.T) {
	t.Parallel()
	for _, quiet := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			t.Run(format+map[bool]string{true: " quiet", false: " normal"}[quiet], func(t *testing.T) {
				t.Parallel()
				outcome := worktrees.CleanupOutcome{Artifacts: []worktrees.LifecycleArtifact{{Path: "unchanged-evidence", Reason: "not changed"}, {Path: "mutation-evidence", Reason: "changed", Applied: true}}}
				deps := retirementCleanupDeps(outcome)
				runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "private-root", Quiet: quiet} }}
				var out, errOut bytes.Buffer
				if err := retirementExecute(NewCleanup(runtime, deps), []string{"task", "--format=" + format}, &out, &errOut); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(errOut.String(), "unchanged-evidence") == quiet || !strings.Contains(errOut.String(), "mutation-evidence") {
					t.Fatalf("quiet=%t stderr=%q", quiet, errOut.String())
				}
				if format == "json" {
					var got worktrees.CleanupOutcome
					if err := json.Unmarshal(out.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if len(got.Artifacts) != 2 || got.Artifacts[0].Path != "unchanged-evidence" || got.Artifacts[1].Path != "mutation-evidence" {
						t.Fatalf("artifacts lost: %+v", got.Artifacts)
					}
				}
			})
		}
	}
}
