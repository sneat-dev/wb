package githubapp

import (
	"strings"
	"testing"
	"time"
)

const dqCovEligibilityREADMEURL = "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md"

func TestDQCovVerifyPublicEligibilityRequiresValidEvidence(t *testing.T) {
	t.Parallel()
	markdown := "## WB\n\n[Workbench dashboard](https://sneat.work/bench)\n"
	_, err := VerifyPublicEligibility("acme/widgets", dqCovEligibilityREADMEURL, markdown, time.Now())
	if err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("non-canonical repository err = %v", err)
	}
	evidence, err := VerifyPublicEligibility("github.com/acme/widgets", dqCovEligibilityREADMEURL, markdown, time.Now())
	if err != nil || evidence.Repository != "github.com/acme/widgets" {
		t.Fatalf("valid evidence = %#v, %v", evidence, err)
	}
}

func TestDQCovValidatePublicEligibilityRejectsUnparseableREADMEURL(t *testing.T) {
	t.Parallel()
	evidence := PublicEligibility{
		Repository: "github.com/acme/widgets",
		READMEURL:  dqCovEligibilityREADMEURL + "\x7f",
		VerifiedAt: time.Now(),
	}
	if err := ValidatePublicEligibility(evidence); err == nil || !strings.Contains(err.Error(), "parse public eligibility README URL") {
		t.Fatalf("unparseable README URL err = %v", err)
	}
}

func TestDQCovReadmeOptInIgnoresShortFenceRuns(t *testing.T) {
	t.Parallel()
	markdown := "## WB\n\n``\n\n[Workbench](https://sneat.work/bench)\n"
	evidence, err := VerifyPublicEligibility("github.com/acme/widgets", dqCovEligibilityREADMEURL, markdown, time.Now())
	if err != nil {
		t.Fatalf("a two-backtick line is not a code fence: %v", err)
	}
	if evidence.Repository != "github.com/acme/widgets" {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestDQCovWorkbenchURLRejectsMalformedButApprovedLinks(t *testing.T) {
	t.Parallel()
	if workbenchURL("https://sneat.work/bench\x7f") {
		t.Fatal("a URL with a control character must not be accepted")
	}
	if workbenchURL("https://sneat.work/bench") != true {
		t.Fatal("the approved dashboard root was rejected")
	}
	if !workbenchURL("https://sneat.work/bench/repo/github.com/acme/widgets") {
		t.Fatal("the approved repository subpath was rejected")
	}
	if workbenchURL("https://user@sneat.work/bench") {
		t.Fatal("a URL with embedded credentials was accepted")
	}

	markdown := "## WB\n\n[Workbench](https://sneat.work/bench\x7f)\n"
	if _, err := VerifyPublicEligibility("github.com/acme/widgets", dqCovEligibilityREADMEURL, markdown, time.Now()); err == nil || !strings.Contains(err.Error(), "opt-in") {
		t.Fatalf("malformed opt-in link err = %v", err)
	}
}
