package cmdpr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

func TestPrintPullRequestLandDisplaysUmbrellaAndCloseOffer(t *testing.T) {
	t.Parallel()
	result := orchestrate.PullRequestLandResult{
		Outcome:              orchestrate.LandSuccess,
		Repository:           "sneat-co/backstage",
		PullRequest:          846,
		BaseRef:              "main",
		MergeSHA:             "0123456789abcdef0123456789abcdef01234567",
		Umbrella:             "github:sneat-co/backstage#845",
		UmbrellaCloseCommand: "wb umbrella close sneat-co/backstage#845 --apply",
		Evidence:             map[string]string{},
	}
	var out bytes.Buffer
	if err := printPullRequestLand(outputCommand(&out), result); err != nil {
		t.Fatalf("printPullRequestLand: %v", err)
	}
	for _, want := range []string{
		"umbrella: github:sneat-co/backstage#845",
		"all umbrella members finished; close with: wb umbrella close sneat-co/backstage#845 --apply",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("printPullRequestLand output missing %q:\n%s", want, out.String())
		}
	}

	// Writer errors on umbrella and close-offer lines propagate.
	for failAt := 1; failAt <= 4; failAt++ {
		if err := printPullRequestLand(newFailingCommand(failAt), result); err == nil {
			t.Fatalf("failAt=%d unexpectedly succeeded", failAt)
		}
	}
}
