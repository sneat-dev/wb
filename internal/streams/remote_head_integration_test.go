package streams

import (
	"path/filepath"
	"testing"
)

func TestRecordStreamSyncRemoteHeadPersistsTheNextStatusAndLeaseHead(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "remote-advance", Members: []Member{{Repository: "acme/app", Role: RoleConsumer, Branch: "stream/remote-advance", Lease: Lease{RecordedHead: "stale-head"}}}}); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordRemoteHead("remote-advance", "acme/app", "remote-after"); err != nil {
		t.Fatalf("record remote head: %v", err)
	}

	persisted, err := store.Load("remote-advance")
	if err != nil {
		t.Fatal(err)
	}
	member, ok := persisted.Member("acme/app")
	if !ok || member.Lease.RecordedHead != "remote-after" {
		t.Fatalf("persisted member = %#v; next status/push must use remote-after", member)
	}
}
