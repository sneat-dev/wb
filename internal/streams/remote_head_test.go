package streams

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordRemoteHeadReportsUnknownMemberAndMatchesCanonicalCase(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "cw-cov", Members: []Member{{Repository: "acme/app", Role: RoleConsumer, Branch: "stream/cw-cov"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRemoteHead("cw-cov", "acme/absent", "head"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown member error = %v, want a named refusal", err)
	}
	// Recorded heads are matched case-insensitively, the way a remote's
	// canonical casing may differ from a hand-written stream file.
	if err := store.RecordRemoteHead("cw-cov", "ACME/APP", "fresh-head"); err != nil {
		t.Fatalf("case-insensitive update: %v", err)
	}
	persisted, err := store.Load("cw-cov")
	if err != nil {
		t.Fatal(err)
	}
	if member, ok := persisted.Member("acme/app"); !ok || member.Lease.RecordedHead != "fresh-head" {
		t.Fatalf("persisted member = %+v", member)
	}
}
