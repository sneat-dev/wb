package githubapp

import (
	"strings"
	"testing"
	"time"
)

func TestDQCovValidateProjectionDocumentRejectsInvalidEligibilityEvidence(t *testing.T) {
	t.Parallel()
	document := ProjectionDocument{
		Scope: ScopeRepository, ID: "github.com/acme/widgets", DisplayName: "widgets",
		UpdatedAt: time.Unix(1, 0), PublicOptIn: true,
		PublicEligibility: &PublicEligibility{
			Repository: "github.com/acme/widgets",
			READMEURL:  "https://github.com/acme/widgets/blob/main/README.md",
			VerifiedAt: time.Unix(1, 0),
		},
	}
	err := ValidateProjectionDocument(document)
	if err == nil || !strings.Contains(err.Error(), "invalid public projection eligibility") {
		t.Fatalf("err = %v, want invalid eligibility refusal", err)
	}
}
