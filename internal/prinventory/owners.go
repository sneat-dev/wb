package prinventory

import (
	"sort"
	"strings"
)

func ResolveOwners(extra []string, authUser func() (string, error), memberOrgs func() ([]string, error)) ([]Owner, []Diagnostic) {
	seen := map[string]bool{}
	owners := make([]Owner, 0)
	diagnostics := make([]Diagnostic, 0)
	add := func(login, qualifier string) {
		login = strings.TrimSpace(login)
		if login == "" {
			return
		}
		key := qualifier + ":" + strings.ToLower(login)
		if seen[key] {
			return
		}
		seen[key] = true
		owners = append(owners, Owner{Login: login, Qualifier: qualifier})
	}
	if user, err := authUser(); err == nil {
		add(user, "user")
	} else {
		diagnostics = append(diagnostics, Diagnostic{Severity: "error", Message: "could not discover authenticated GitHub user: " + err.Error()})
	}
	if orgs, err := memberOrgs(); err == nil {
		for _, org := range orgs {
			add(org, "org")
		}
	} else {
		diagnostics = append(diagnostics, Diagnostic{Severity: "error", Message: "could not discover GitHub organizations: " + err.Error()})
	}
	for _, org := range extra {
		add(org, "org")
	}
	result := make([]Owner, 0, len(owners))
	for _, owner := range owners {
		if owner.Login != "" {
			result = append(result, owner)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, diagnostics
}
