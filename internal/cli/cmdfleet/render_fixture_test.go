package cmdfleet

import "github.com/sneat-dev/wb/internal/mergepolicy"

func cwDepsMergePolicyReportFixture() mergepolicy.Report {
	return mergepolicy.Report{
		SchemaVersion: 1, Mode: "check",
		Summary: mergepolicy.Summary{Inspected: 4, Compliant: 1, Drift: 1, Blocked: 1, Errors: 1},
		Repositories: []mergepolicy.Repository{
			{Repository: "acme/clean", Disposition: "compliant"},
			{Repository: "acme/drift", Disposition: "drift", Drift: []string{"allow_squash_merge=true"}},
			{Repository: "acme/conflict", Disposition: "blocked", Conflicts: []string{"repository ruleset 7 (acme): requires linear history"}},
			{Repository: "acme/broken", Disposition: "error", Error: "decode repository settings: unexpected EOF"},
		},
		Rulesets: []mergepolicy.RulesetChange{{SourceType: "Organization", Source: "acme", ID: 9, Repositories: []string{"acme/a", "acme/b"}, Disposition: "planned"}},
	}
}
