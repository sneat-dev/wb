package main

import (
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// The next stream status and any justified push read the lease from durable
// stream state. A sync that saw a newer remote head must therefore persist it,
// rather than merely reporting it from the current process.
func TestRecordStreamSyncRemoteHeadPersistsTheNextStatusAndLeaseHead(t *testing.T) {
	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "remote-advance",
		Members: []streams.Member{{
			Repository: "acme/app", Role: streams.RoleConsumer,
			Branch: "stream/remote-advance",
			Lease:  streams.Lease{RecordedHead: "stale-head"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := recordStreamSyncRemoteHead(store, "remote-advance", "acme/app", "remote-after"); err != nil {
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
