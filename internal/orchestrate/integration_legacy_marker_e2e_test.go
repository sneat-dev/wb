//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"
)

//nolint:paralleltest // Existing complete Land fixture installs process-global GH/PATH and WB_PROJECTS_ROOT; each subcase is isolated sequentially.
func TestE2ELegacyMarkerLossRefusesBeforeOrdinaryReviewOrArming(t *testing.T) {
	for _, kind := range []string{"inventory", "durable", "source read failure"} {
		t.Run(kind, func(t *testing.T) {
			fixture, options := landPromotionFixture(t)
			body := legacyInventoryPrefix + "malformed -->"
			if kind == "durable" {
				body = "ordinary text"
				options.promotePorts.legacyComments = func(context.Context, string, string) ([]LegacyBinding, error) {
					return []LegacyBinding{{URL: "https://github.com/acme/app/commit/" + fixture.headSHA + "#commitcomment-90", Digest: legacyHash([]byte("record inventory"))}}, nil
				}
			}
			fixture.writeState(t, "body", jsonEscapedBody(body))
			fixture.writeState(t, "commits", `[{"sha":"`+fixture.headSHA+`","commit":{"message":"old merge"},"parents":[{"sha":"`+fixture.baseSHA+`"},{"sha":"`+strings.Repeat("b", 40)+`"}]}]`)
			if kind == "source read failure" {
				fixture.writeState(t, "commits", "{")
			}
			options.ApprovedBy = "opus@codex@ordinary-review"
			options.ReviewComment = "ordinary approval must not be posted"
			effects := []string{"merged", "auto-merge", "update-branch", "posted-comments"}
			before := map[string]string{}
			for _, name := range effects {
				before[name] = fixture.readState(t, name)
			}
			result, err := LandPullRequest(context.Background(), options)
			if kind == "source read failure" {
				if err == nil {
					t.Fatal("commit read error ignored")
				}
			} else if err != nil || result.Outcome != LandRefused || result.RefusalCode != LandRefusalBatchBlockInvalid {
				t.Fatal(result, err)
			}
			for _, name := range effects {
				if after := fixture.readState(t, name); after != before[name] {
					t.Fatal("side effect after refused classification", name, before[name], after)
				}
			}
		})
	}
}
