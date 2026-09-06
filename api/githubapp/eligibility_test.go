package githubapp

import (
	"strings"
	"testing"
	"time"
)

func TestVerifyPublicEligibilityAcceptsOnlyExplicitRootREADMESections(t *testing.T) {
	verifiedAt := time.Date(2026, time.September, 6, 3, 30, 0, 0, time.FixedZone("IST", 3600))
	for name, markdown := range map[string]string{
		"wb section markdown link":              "# Widget\n\n## WB\n\n[Workbench dashboard](https://sneat.work/bench)\n",
		"workbench section dashboard link":      "## Workbench\n\n[Dashboard](https://sneat.work/bench/dashboard)\n",
		"workbench section repository autolink": "## Workbench\n\n<https://sneat.work/bench/repo/github.com/acme/widgets>\n",
		"nested content stays in section":       "## WB\n\n### Operations\n\n[Open](https://sneat.work/bench/repo/github.com/acme/widgets)\n",
	} {
		t.Run(name, func(t *testing.T) {
			evidence, err := VerifyPublicEligibility("github.com/acme/widgets", "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md", markdown, verifiedAt)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Repository != "github.com/acme/widgets" || evidence.READMEURL != "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md" || !evidence.VerifiedAt.Equal(verifiedAt) {
				t.Fatalf("evidence = %#v", evidence)
			}
			if evidence.VerifiedAt.Location() != time.UTC {
				t.Fatalf("verification time location = %s, want UTC", evidence.VerifiedAt.Location())
			}
		})
	}
}

func TestVerifyPublicEligibilityRejectsAmbiguousOrUnrelatedLinks(t *testing.T) {
	for name, markdown := range map[string]string{
		"outside WB section":          "[Workbench](https://sneat.work/bench)\n\n## About\n",
		"wrong heading level":         "### WB\n\n[Workbench](https://sneat.work/bench)\n",
		"after a following section":   "## WB\n\n## About\n\n[Workbench](https://sneat.work/bench)\n",
		"fenced code":                 "## WB\n\n```md\n[Workbench](https://sneat.work/bench)\n```\n",
		"nested fence remains fenced": "## WB\n\n````md\n```\n[Workbench](https://sneat.work/bench)\n````\n",
		"wrong host":                  "## WB\n\n[Workbench](https://sneat.work.example/bench)\n",
		"wrong prefix":                "## WB\n\n[Workbench](https://sneat.work/benches)\n",
		"unapproved subpath":          "## WB\n\n[Workbench](https://sneat.work/bench/anything)\n",
		"query":                       "## WB\n\n[Workbench](https://sneat.work/bench?utm_source=readme)\n",
		"fragment":                    "## WB\n\n[Workbench](https://sneat.work/bench#dashboard)\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := VerifyPublicEligibility("github.com/acme/widgets", "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md", markdown, time.Now())
			if err == nil || !strings.Contains(err.Error(), "opt-in") {
				t.Fatalf("err = %v, want opt-in refusal", err)
			}
		})
	}
}

func TestValidatePublicEligibilityRejectsNonCanonicalEvidence(t *testing.T) {
	valid := PublicEligibility{Repository: "github.com/acme/widgets", READMEURL: "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md", VerifiedAt: time.Now()}
	if err := ValidatePublicEligibility(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PublicEligibility){
		"repository is not canonical":   func(e *PublicEligibility) { e.Repository = "acme/widgets" },
		"repository has trailing slash": func(e *PublicEligibility) { e.Repository = "github.com/acme/widgets/" },
		"README belongs to a different repository": func(e *PublicEligibility) {
			e.READMEURL = "https://github.com/acme/other/blob/0123456789abcdef0123456789abcdef01234567/README.md"
		},
		"README is not root README": func(e *PublicEligibility) {
			e.READMEURL = "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/docs/README.md"
		},
		"README ref is a branch":    func(e *PublicEligibility) { e.READMEURL = "https://github.com/acme/widgets/blob/main/README.md" },
		"README ref is abbreviated": func(e *PublicEligibility) { e.READMEURL = "https://github.com/acme/widgets/blob/0123456/README.md" },
		"README is not HTTPS": func(e *PublicEligibility) {
			e.READMEURL = "http://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md"
		},
		"README has a query": func(e *PublicEligibility) {
			e.READMEURL = "https://github.com/acme/widgets/blob/0123456789abcdef0123456789abcdef01234567/README.md?plain=1"
		},
		"verification time missing": func(e *PublicEligibility) { e.VerifiedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			evidence := valid
			mutate(&evidence)
			if err := ValidatePublicEligibility(evidence); err == nil {
				t.Fatalf("ValidatePublicEligibility(%#v) returned nil", evidence)
			}
		})
	}
}
