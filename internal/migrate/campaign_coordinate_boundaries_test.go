package migrate

import (
	"errors"
	"testing"
)

func TestCampaignPlanningPreservesAbsoluteResolutionErrors(t *testing.T) {
	t.Parallel()
	cause := errors.New("absolute root unavailable")
	absolute := func(string) (string, error) { return "", cause }
	if _, err := normalizeCampaignOptionsWithAbsolute(CampaignOptions{GitHubDir: "relative"}, absolute); !errors.Is(err, cause) {
		t.Fatalf("normalize error=%v", err)
	}
	if plan, err := planCampaignWithAbsolute(migCovTextReplaceSpec("relative"), "relative", CampaignOptions{}, absolute); plan != nil || !errors.Is(err, cause) {
		t.Fatalf("plan=%v error=%v", plan, err)
	}
}
