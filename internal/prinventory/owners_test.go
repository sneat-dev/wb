package prinventory

import (
	"errors"
	"testing"
)

func TestCwCovResolvePRInventoryOwnersDedupesAndDiagnoses(t *testing.T) {
	t.Parallel()
	auth := func() (string, error) { return "Cw-User", nil }
	orgs := func() ([]string, error) { return []string{"cw-org", "CW-ORG"}, nil }
	owners, diagnostics := ResolveOwners([]string{"extra-org", "  ", "CW-ORG"}, auth, orgs)
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", diagnostics)
	}
	got := map[string]string{}
	for _, owner := range owners {
		got[owner.Login] = owner.Qualifier
	}
	if len(owners) != 3 {
		t.Fatalf("owners = %+v, want user + one org (case-insensitive dedupe) + extra", owners)
	}
	if got["Cw-User"] != "user" || got["cw-org"] != "org" || got["extra-org"] != "org" {
		t.Fatalf("owners = %+v", owners)
	}
	// Sorted by qualifier:login so the snapshot is deterministic.
	for i := 1; i < len(owners); i++ {
		if owners[i-1].String() > owners[i].String() {
			t.Fatalf("owners are not sorted: %+v", owners)
		}
	}

	// With discovery broken the failure must be reported, not silently ignored.
	auth = func() (string, error) { return "", errors.New("discovery unavailable") }
	orgs = func() ([]string, error) { return nil, errors.New("discovery unavailable") }
	owners, diagnostics = ResolveOwners(nil, auth, orgs)
	if len(owners) != 0 {
		t.Fatalf("owners = %+v, want none when discovery fails", owners)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want one per failed discovery", diagnostics)
	}
}
