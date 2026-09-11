// Copyright 2026 Sneat Co.

package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
)

const testPepper = "0123456789abcdef0123456789abcdef"

func testCredentialBinding(now time.Time, machine string) MachineCredentialBinding {
	return MachineCredentialBinding{
		IdentityID: "local", IdentityDisplayName: "Alice", MachineName: machine,
		IssuedAt: now, Scopes: cloneEnrollmentScopes(),
	}
}

// TestMachineCredentialStoreRotatesDigestAndKeepsStableMachineID is the
// journey the daemon takes on every restart: the same machine name under the
// same identity must keep one machine id while its token rotates, and the
// superseded digest must stop resolving.
func TestMachineCredentialStoreRotatesDigestAndKeepsStableMachineID(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	credentials, resolver, _ := NewMachineStores(backend)
	pepper := []byte(testPepper)
	now := time.Date(2026, 9, 11, 18, 0, 0, 0, time.UTC)

	firstDigest, err := DigestMachineToken("first-token", pepper)
	if err != nil {
		t.Fatal(err)
	}
	binding := testCredentialBinding(now, "laptop")
	first, err := credentials.RotateMachineCredential(ctx, binding, firstDigest)
	if err != nil || first.MachineID == "" {
		t.Fatalf("first rotation = %+v, %v", first, err)
	}
	if first.MachineID != MachineID(binding.IdentityID, binding.MachineName) {
		t.Fatalf("machine id %q is not derived from identity and name", first.MachineID)
	}
	if resolved, resolveErr := resolver.ResolveMachineCredential(ctx, firstDigest); resolveErr != nil || !reflect.DeepEqual(resolved, first) {
		t.Fatalf("resolve first = %+v, %v", resolved, resolveErr)
	}

	secondDigest, err := DigestMachineToken("second-token", pepper)
	if err != nil {
		t.Fatal(err)
	}
	binding.IssuedAt = now.Add(time.Minute)
	second, err := credentials.RotateMachineCredential(ctx, binding, secondDigest)
	if err != nil || second.MachineID != first.MachineID {
		t.Fatalf("second rotation = %+v, %v", second, err)
	}
	if _, resolveErr := resolver.ResolveMachineCredential(ctx, firstDigest); !errors.Is(resolveErr, errMachineCredentialUnavailable) {
		t.Fatalf("revoked credential error = %v", resolveErr)
	}
	if resolved, resolveErr := resolver.ResolveMachineCredential(ctx, secondDigest); resolveErr != nil || !reflect.DeepEqual(resolved, second) {
		t.Fatalf("resolve second = %+v, %v", resolved, resolveErr)
	}

	// Re-presenting a digest that is already stored is refused rather than
	// silently re-bound: two machines must never share one credential.
	if _, err := credentials.RotateMachineCredential(ctx, testCredentialBinding(now, "desktop"), secondDigest); err == nil {
		t.Fatal("rotating onto an existing digest was accepted")
	}
}

// TestMachineBearerResolverBindsOnlyStoredCredential proves the stored
// credential, and nothing else, authenticates a request.
func TestMachineBearerResolverBindsOnlyStoredCredential(t *testing.T) {
	ctx := context.Background()
	credentials, resolverStore, _ := NewMachineStores(newFirestoreMemoryBackend())
	pepper := []byte(testPepper)
	digest, err := DigestMachineToken("valid-token", pepper)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := credentials.RotateMachineCredential(ctx, testCredentialBinding(time.Now().UTC(), "vm-1"), digest)
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewMachineBearerResolver(resolverStore, pepper)
	request := httptest.NewRequest(http.MethodGet, "/v0/workbench/repository-events", nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	machine, err := resolver.ResolveMachineBearer(request)
	if err != nil || machine.ID != binding.MachineID || machine.Name != binding.MachineName ||
		machine.IdentityID != binding.IdentityID || len(machine.Scopes) != len(enrollmentScopes) {
		t.Fatalf("resolved machine = %+v, %v", machine, err)
	}
	for name, authorization := range map[string]string{
		"missing": "", "wrong": "Bearer wrong-token", "ambiguous": "Basic valid-token", "whitespace": "Bearer two tokens",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v0/workbench/repository-events", nil)
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			if _, err := resolver.ResolveMachineBearer(request); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// TestMachineCredentialStoreFailsClosed covers every way the store refuses
// rather than writing or returning a half-formed credential.
func TestMachineCredentialStoreFailsClosed(t *testing.T) {
	ctx := context.Background()
	pepper := []byte(testPepper)
	digest, err := DigestMachineToken("token", pepper)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 18, 0, 0, 0, time.UTC)

	t.Run("no backend", func(t *testing.T) {
		store := machineCredentialStore{}
		if _, err := store.RotateMachineCredential(ctx, testCredentialBinding(now, "laptop"), digest); !errors.Is(err, errMachineCredentialUnavailable) {
			t.Fatalf("rotate = %v", err)
		}
		if _, err := store.ResolveMachineCredential(ctx, digest); !errors.Is(err, errMachineCredentialUnavailable) {
			t.Fatalf("resolve = %v", err)
		}
	})

	for name, binding := range map[string]MachineCredentialBinding{
		"no identity": {MachineName: "laptop", IssuedAt: now},
		"no machine":  {IdentityID: "local", IssuedAt: now},
		"no issued":   {IdentityID: "local", MachineName: "laptop"},
	} {
		t.Run(name, func(t *testing.T) {
			credentials, _, _ := NewMachineStores(newFirestoreMemoryBackend())
			if _, err := credentials.RotateMachineCredential(ctx, binding, digest); !errors.Is(err, errMachineCredentialUnavailable) {
				t.Fatalf("rotate = %v", err)
			}
		})
	}

	binding := testCredentialBinding(now, "laptop")
	machineID := MachineID(binding.IdentityID, binding.MachineName)
	for name, fault := range map[string]func(*firestoreMemoryBackend){
		"enrollment read fails":  func(b *firestoreMemoryBackend) { b.failGet = failOnID(machineID) },
		"digest read fails":      func(b *firestoreMemoryBackend) { b.failGet = failOnCollection(machineCredentialCollection) },
		"credential write fails": func(b *firestoreMemoryBackend) { b.failSet = failOnCollection(machineCredentialCollection) },
		"enrollment write fails": func(b *firestoreMemoryBackend) { b.failSet = failOnCollection(machineEnrollmentCollection) },
	} {
		t.Run(name, func(t *testing.T) {
			backend := newFirestoreMemoryBackend()
			fault(backend)
			credentials, _, _ := NewMachineStores(backend)
			if _, err := credentials.RotateMachineCredential(ctx, binding, digest); !errors.Is(err, errFirestoreMemoryFault) {
				t.Fatalf("rotate = %v", err)
			}
		})
	}

	t.Run("revoking the previous credential fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		credentials, _, _ := NewMachineStores(backend)
		if _, err := credentials.RotateMachineCredential(ctx, binding, digest); err != nil {
			t.Fatal(err)
		}
		next, err := DigestMachineToken("next-token", pepper)
		if err != nil {
			t.Fatal(err)
		}
		backend.failDelete = failOnCollection(machineCredentialCollection)
		if _, err := credentials.RotateMachineCredential(ctx, binding, next); !errors.Is(err, errFirestoreMemoryFault) {
			t.Fatalf("rotate = %v", err)
		}
	})

	t.Run("stored credential is malformed", func(t *testing.T) {
		for _, stored := range []MachineCredentialBinding{
			{MachineName: "laptop", IdentityID: "local", IssuedAt: now},
			{MachineID: "machine_1", IdentityID: "local", IssuedAt: now},
			{MachineID: "machine_1", MachineName: "laptop", IssuedAt: now},
			{MachineID: "machine_1", MachineName: "laptop", IdentityID: "local"},
		} {
			backend := newFirestoreMemoryBackend()
			backend.putDocument(machineCredentialCollection, machineCredentialID(digest), machineCredentialDocument{Binding: stored})
			_, resolver, _ := NewMachineStores(backend)
			if _, err := resolver.ResolveMachineCredential(ctx, digest); !errors.Is(err, errMachineCredentialUnavailable) {
				t.Fatalf("resolve of %+v = %v", stored, err)
			}
		}
	})

	t.Run("read fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnCollection(machineCredentialCollection)
		_, resolver, _ := NewMachineStores(backend)
		if _, err := resolver.ResolveMachineCredential(ctx, digest); !errors.Is(err, errMachineCredentialUnavailable) {
			t.Fatalf("resolve = %v", err)
		}
	})
}

func testStoredSnapshot(publishedAt time.Time, digest string) StoredMachineSnapshot {
	return StoredMachineSnapshot{
		IdentityID: "local",
		MachineID:  "machine-id",
		Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion,
			Login:         "alice",
			Machine:       "laptop",
			PublishedAt:   publishedAt,
			Worktrees:     []machinesnapshot.Worktree{{Task: "dashboard", Repository: "sneat-dev/wb", Branch: "feature/dashboard"}},
		},
		ReceivedAt: publishedAt.Add(time.Second),
		Digest:     digest,
	}
}

// TestMachineSnapshotStoreIsAtomicIdempotentAndMonotonic proves the store
// executes ResolveLatestMachineSnapshot's ordering rules and writes only when
// the resolution says the record changed.
func TestMachineSnapshotStoreIsAtomicIdempotentAndMonotonic(t *testing.T) {
	ctx := context.Background()
	backend := newFirestoreMemoryBackend()
	_, _, snapshots := NewMachineStores(backend)
	first := testStoredSnapshot(time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC), "first")

	created, err := snapshots.StoreLatest(ctx, first)
	if err != nil || !created.Updated || created.Current.Digest != "first" {
		t.Fatalf("created = %+v, err = %v", created, err)
	}

	duplicate := first
	duplicate.ReceivedAt = first.ReceivedAt.Add(time.Minute)
	unchanged, err := snapshots.StoreLatest(ctx, duplicate)
	if err != nil || unchanged.Updated || !unchanged.Current.ReceivedAt.Equal(first.ReceivedAt) {
		t.Fatalf("duplicate = %+v, err = %v", unchanged, err)
	}

	stale := first
	stale.Digest = "stale"
	stale.Snapshot.PublishedAt = first.Snapshot.PublishedAt.Add(-time.Minute)
	if _, err := snapshots.StoreLatest(ctx, stale); !errors.Is(err, machinesnapshot.ErrStaleSnapshot) {
		t.Fatalf("stale err = %v", err)
	}

	newer := first
	newer.Digest = "newer"
	newer.Snapshot.PublishedAt = first.Snapshot.PublishedAt.Add(time.Minute)
	updated, err := snapshots.StoreLatest(ctx, newer)
	if err != nil || !updated.Updated || updated.Current.Digest != "newer" {
		t.Fatalf("updated = %+v, err = %v", updated, err)
	}

	listed, err := snapshots.ListLatest(ctx)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], newer) {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
}

// TestMachineSnapshotStoreFailsClosed covers the store's refusal paths.
func TestMachineSnapshotStoreFailsClosed(t *testing.T) {
	ctx := context.Background()
	record := testStoredSnapshot(time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC), "digest")

	t.Run("no backend", func(t *testing.T) {
		store := machineSnapshotStore{}
		if _, err := store.StoreLatest(ctx, record); !errors.Is(err, errMachineSnapshotStoreUnavailable) {
			t.Fatalf("store = %v", err)
		}
		if _, err := store.ListLatest(ctx); !errors.Is(err, errMachineSnapshotStoreUnavailable) {
			t.Fatalf("list = %v", err)
		}
	})

	t.Run("read fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnCollection(machineSnapshotCollection)
		_, _, snapshots := NewMachineStores(backend)
		if _, err := snapshots.StoreLatest(ctx, record); !errors.Is(err, errFirestoreMemoryFault) {
			t.Fatalf("store = %v", err)
		}
	})

	t.Run("write fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failSet = failOnCollection(machineSnapshotCollection)
		_, _, snapshots := NewMachineStores(backend)
		if _, err := snapshots.StoreLatest(ctx, record); !errors.Is(err, errFirestoreMemoryFault) {
			t.Fatalf("store = %v", err)
		}
	})

	t.Run("candidate is invalid", func(t *testing.T) {
		_, _, snapshots := NewMachineStores(newFirestoreMemoryBackend())
		invalid := record
		invalid.Digest = ""
		if _, err := snapshots.StoreLatest(ctx, invalid); err == nil {
			t.Fatal("an invalid candidate was stored")
		}
	})

	t.Run("query fails", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failQuery = failQueryOnCollection(machineSnapshotCollection)
		_, _, snapshots := NewMachineStores(backend)
		if _, err := snapshots.ListLatest(ctx); !errors.Is(err, errFirestoreMemoryFault) {
			t.Fatalf("list = %v", err)
		}
	})
}

// TestCollectionsListsEveryCollectionTheStoresWriteTo is the guard a
// schema-first engine depends on: a collection that is written but not listed
// would fail at runtime on inGitDB and nowhere else, so the list is compared
// against the constants and path helpers themselves.
func TestCollectionsListsEveryCollectionTheStoresWriteTo(t *testing.T) {
	listed := Collections()
	for _, want := range []string{
		machineCredentialCollection,
		machineEnrollmentCollection,
		machineSnapshotCollection,
		pollObservationCollection,
		installationStateCollection,
		installationIdentityCollection,
		installationIndexCollection,
		installationUserIndexCollection,
		installationLifecycleCollection,
		repositoryEventCollection,
		repositoryEventMetaCollection,
		repositoryEventQueueCollection,
		repositoryEventStatusCollection,
		// The shapes the path helpers build, with their dynamic document ids
		// removed: a subcollection is declared by name under its root.
		shapeOf(identityInstallationCollection("identity", "generation")),
		shapeOf(installationRepositoryCollection(identityInstallationCollection("identity", "generation"), 7, "")),
		shapeOf(installationRepositoryCollection(identityInstallationCollection("identity", "generation"), 7, "g1")),
		shapeOf(repositoryEventQueueEventsCollection("machine")),
		shapeOf(repositoryEventQueuePollsCollection("machine")),
		shapeOf(repositoryEventPendingCollection("identity")),
	} {
		if !slices.Contains(listed, want) {
			t.Fatalf("Collections() is missing %q; a schema-first engine would refuse every write to it", want)
		}
	}
	// A root must be declared before anything nested beneath it.
	for index, collection := range listed {
		root, _, nested := strings.Cut(collection, "/")
		if !nested {
			continue
		}
		if !slices.Contains(listed[:index], root) {
			t.Fatalf("%q is listed before its root %q", collection, root)
		}
	}
}

// shapeOf drops the document ids from a collection path, leaving the
// collection names a schema declaration is made of: "a/1/b/2/c" becomes
// "a/b/c".
func shapeOf(path string) string {
	segments := strings.Split(path, "/")
	names := make([]string, 0, (len(segments)+1)/2)
	for index := 0; index < len(segments); index += 2 {
		names = append(names, segments[index])
	}
	return strings.Join(names, "/")
}
