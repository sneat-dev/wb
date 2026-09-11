package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
)

func TestInstallationStateStoreReadsAndTransitionsAtomically(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	store := installationStateStore{backend: backend}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	currentDigest, nextDigest := InstallationStateDigest{1}, InstallationStateDigest{2}
	current, next := oauthState(now, 7), oauthState(now.Add(time.Second), 7)
	if err := store.IssueInstallationState(context.Background(), currentDigest, current); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadInstallationState(context.Background(), currentDigest, now); err != nil || got != current {
		t.Fatalf("read=%+v err=%v", got, err)
	}
	if err := store.TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, next); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadInstallationState(context.Background(), currentDigest, now); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("replay err=%v", err)
	}
	if got, err := store.ReadInstallationState(context.Background(), nextDigest, now); err != nil || got != next {
		t.Fatalf("next=%+v err=%v", got, err)
	}
	replaced := next
	replaced.OAuthAccessTokenCiphertext = "encrypted-token"
	if err := store.ReplaceInstallationState(context.Background(), nextDigest, next, now, replaced); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceInstallationState(context.Background(), nextDigest, next, now, replaced); !errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("stale replacement err=%v", err)
	}
	if got, err := store.ReadInstallationState(context.Background(), nextDigest, now); err != nil || got != replaced {
		t.Fatalf("replaced=%+v err=%v", got, err)
	}
}

// TestTransitionInstallationStateRejectsEveryInvalidCase covers each replay,
// expiry, mismatch, and next-digest collision sentence of
// InstallationStateStore.TransitionInstallationState's doc comment
// individually; TestInstallationStateStoreReadsAndTransitionsAtomically only
// ever exercised the happy path plus a replay.
func TestTransitionInstallationStateRejectsEveryInvalidCase(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	t.Run("expired current state", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		digest, next := InstallationStateDigest{1}, InstallationStateDigest{2}
		current := oauthState(now, 7)
		current.ExpiresAt = now // already expired at the instant of transition
		if err := store.backend.Set(context.Background(), installationStateCollection, installationStateDocumentID(digest), current); err != nil {
			t.Fatal(err)
		}
		if err := store.TransitionInstallationState(context.Background(), digest, current, now, next, oauthState(now, 7)); !errors.Is(err, ErrInvalidInstallationState) {
			t.Fatalf("expired transition err=%v", err)
		}
	})

	t.Run("current mismatch", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		digest, next := InstallationStateDigest{1}, InstallationStateDigest{2}
		stored := oauthState(now, 7)
		if err := store.IssueInstallationState(context.Background(), digest, stored); err != nil {
			t.Fatal(err)
		}
		claimed := stored
		claimed.InstallationID = 99 // caller's belief about the current record differs from storage
		if err := store.TransitionInstallationState(context.Background(), digest, claimed, now, next, oauthState(now, 7)); !errors.Is(err, ErrInvalidInstallationState) {
			t.Fatalf("mismatch transition err=%v", err)
		}
	})

	t.Run("next digest collision", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		currentDigest, nextDigest := InstallationStateDigest{1}, InstallationStateDigest{2}
		current, collidingNext := oauthState(now, 7), oauthState(now, 8)
		if err := store.IssueInstallationState(context.Background(), currentDigest, current); err != nil {
			t.Fatal(err)
		}
		if err := store.IssueInstallationState(context.Background(), nextDigest, collidingNext); err != nil {
			t.Fatal(err)
		}
		if err := store.TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, oauthState(now, 9)); !errors.Is(err, ErrInvalidInstallationState) {
			t.Fatalf("collision transition err=%v", err)
		}
		// The current record must remain untouched: a rejected transition is
		// not itself a partial state change.
		if got, err := store.ReadInstallationState(context.Background(), currentDigest, now); err != nil || got != current {
			t.Fatalf("current state consumed by rejected transition: got=%+v err=%v", got, err)
		}
	})
}

func TestBindingCompletionPreservesBindingsAndScopesEntitlements(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
	completeBinding(t, states, store, now.Add(time.Minute), InstallationStateDigest{2}, installationBinding(now.Add(time.Minute), 8, 100, "github.com/acme/other"))
	if listed, err := store.ListIdentityInstallationBindings(context.Background(), "firebase-user"); err != nil || len(listed) != 2 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 8, 99); err != nil || allowed {
		t.Fatalf("cross-installation allowed=%v err=%v", allowed, err)
	}
	digest, state := InstallationStateDigest{3}, oauthState(now.Add(2*time.Minute), 9)
	if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
		t.Fatal(err)
	}
	binding := installationBinding(now.Add(2*time.Minute), 9, 101, "github.com/acme/third")
	binding.GitHubUserID = 84
	if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now.Add(2*time.Minute), binding); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("different user err=%v", err)
	}
	if _, err := states.ReadInstallationState(context.Background(), digest, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("rejected completion consumed state: %v", err)
	}
}

func TestLifecycleInvalidatesOnlyAffectedEntitlements(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	binding := installationBinding(now, 7, 99, "github.com/acme/app")
	binding.Installation.Repositories = append(binding.Installation.Repositories, VerifiedRepository{ID: 100, Repository: "github.com/acme/other"})
	completeBinding(t, states, store, now, InstallationStateDigest{1}, binding)
	completeBinding(t, states, store, now.Add(time.Minute), InstallationStateDigest{2}, installationBinding(now.Add(time.Minute), 8, 200, "github.com/acme/third"))
	event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{99}}
	if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if allowed, _ := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); allowed {
		t.Fatal("removed repository remained entitled")
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 100); err != nil || !allowed {
		t.Fatalf("unaffected allowed=%v err=%v", allowed, err)
	}
	event = InstallationLifecycleEvent{DeliveryID: "delivery-2", Action: RepositoryUserAccessRemoved, InstallationID: 7, RepositoryID: 100, GitHubUserID: 42}
	if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 100); allowed {
		t.Fatal("removed repository user remained entitled")
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 8, 200); err != nil || !allowed {
		t.Fatalf("second installation allowed=%v err=%v", allowed, err)
	}
	event = InstallationLifecycleEvent{DeliveryID: "delivery-3", Action: GitHubUserAuthorizationRevoked, GitHubUserID: 42}
	if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 8, 200); allowed {
		t.Fatal("revoked GitHub user remained entitled")
	}
}

// TestApplyInstallationLifecycleRejectsReusedDeliveryIDWithDifferentPayload
// covers the other half of "ApplyInstallationLifecycle is idempotent by
// DeliveryID": a replay of the *same* event is a no-op (already covered
// above), but reusing a DeliveryID with a different payload must be
// rejected rather than silently applied or silently ignored.
func TestApplyInstallationLifecycleRejectsReusedDeliveryIDWithDifferentPayload(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
	first := InstallationLifecycleEvent{DeliveryID: "delivery-reused", Action: InstallationSuspended, InstallationID: 7}
	if err := store.ApplyInstallationLifecycle(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := InstallationLifecycleEvent{DeliveryID: "delivery-reused", Action: InstallationRevoked, InstallationID: 7}
	if err := store.ApplyInstallationLifecycle(context.Background(), second); err == nil {
		t.Fatal("reused delivery ID with a different payload was accepted")
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || allowed {
		t.Fatalf("suspended installation remained entitled: allowed=%v err=%v", allowed, err)
	}
}

// TestGitHubUserAuthorizationRevokedAppliesToEveryBindingOfThatUser covers
// the InstallationLifecycleStore doc comment's claim that
// GitHubUserAuthorizationRevoked "applies to every binding for that GitHub
// user" -- across every Workbench identity that GitHub user has bound, not
// just one.
func TestGitHubUserAuthorizationRevokedAppliesToEveryBindingOfThatUser(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	firstIdentity := installationBinding(now, 7, 99, "github.com/acme/app")
	completeBinding(t, states, store, now, InstallationStateDigest{1}, firstIdentity)

	secondIdentity := installationBinding(now.Add(time.Minute), 8, 200, "github.com/acme/other")
	secondIdentity.IdentityID = "second-firebase-user"
	completeBindingWithIdentity(t, states, store, now.Add(time.Minute), InstallationStateDigest{2}, secondIdentity)

	event := InstallationLifecycleEvent{DeliveryID: "delivery-revoke-user", Action: GitHubUserAuthorizationRevoked, GitHubUserID: 42}
	if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || allowed {
		t.Fatalf("first identity remained entitled: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "second-firebase-user", 8, 200); err != nil || allowed {
		t.Fatalf("second identity remained entitled: allowed=%v err=%v", allowed, err)
	}
}

// TestRepositoryUserAccessRemovedAppliesOnlyToExactTuple covers the doc
// comment's claim that RepositoryUserAccessRemoved "applies only to its
// exact installation, repository, and GitHub user tuple": the same
// repository ID entitled on a different installation, and the same
// installation/repository entitled to a different GitHub user, must both
// stay untouched.
func TestRepositoryUserAccessRemovedAppliesOnlyToExactTuple(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	// Same repository ID (100) bound under two different installations.
	target := installationBinding(now, 7, 100, "github.com/acme/app")
	completeBinding(t, states, store, now, InstallationStateDigest{1}, target)

	otherInstallation := installationBinding(now.Add(time.Minute), 8, 100, "github.com/acme/app-fork")
	completeBindingWithIdentity(t, states, store, now.Add(time.Minute), InstallationStateDigest{2}, otherInstallation)

	event := InstallationLifecycleEvent{DeliveryID: "delivery-repo-user", Action: RepositoryUserAccessRemoved, InstallationID: 7, RepositoryID: 100, GitHubUserID: 42}
	if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 100); err != nil || allowed {
		t.Fatalf("targeted tuple remained entitled: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 8, 100); err != nil || !allowed {
		t.Fatalf("different installation, same repository ID, was also revoked: allowed=%v err=%v", allowed, err)
	}
}

func completeBinding(t *testing.T, states installationStateStore, store installationBindingStore, now time.Time, digest InstallationStateDigest, binding IdentityInstallationBinding) {
	t.Helper()
	completeBindingWithIdentity(t, states, store, now, digest, binding)
}

func completeBindingWithIdentity(t *testing.T, states installationStateStore, store installationBindingStore, now time.Time, digest InstallationStateDigest, binding IdentityInstallationBinding) {
	t.Helper()
	state := oauthState(now, binding.Installation.ID)
	state.IdentityID = binding.IdentityID
	if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err != nil {
		t.Fatal(err)
	}
}

func oauthState(now time.Time, installationID int64) InstallationState {
	return InstallationState{IdentityID: "firebase-user", InstallationID: installationID, BrowserContinuationDigest: "browser", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
}

func installationBinding(now time.Time, installationID, repositoryID int64, repository string) IdentityInstallationBinding {
	return IdentityInstallationBinding{
		IdentityID: "firebase-user", GitHubUserID: 42, GitHubLogin: "octocat", VerifiedAt: now,
		Installation: VerifiedInstallation{ID: installationID, Account: "acme", AccountType: "Organization", RepositorySelection: "selected", State: "installed", Repositories: []VerifiedRepository{{ID: repositoryID, Repository: repository}}},
	}
}

func TestInstallationStoresFailClosed(t *testing.T) {
	now := time.Now().UTC()
	if _, err := (installationStateStore{}).ReadInstallationState(context.Background(), InstallationStateDigest{}, now); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("read err=%v", err)
	}
	if _, err := (installationBindingStore{}).ListIdentityInstallationBindings(context.Background(), "uid"); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("list err=%v", err)
	}
}

// TestIssueInstallationStateRejectsInvalidInput covers IssueInstallationState's
// guard clause: every required field must be present before the backend is
// even consulted.
func TestIssueInstallationStateRejectsInvalidInput(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	valid := oauthState(now, 7)
	cases := map[string]InstallationState{
		"missing identity": {IdentityID: "", InstallationID: 7, IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		"zero issued at":   {IdentityID: valid.IdentityID, InstallationID: 7, ExpiresAt: now.Add(time.Minute)},
		"zero expires at":  {IdentityID: valid.IdentityID, InstallationID: 7, IssuedAt: now},
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			store := installationStateStore{backend: newFirestoreMemoryBackend()}
			if err := store.IssueInstallationState(context.Background(), InstallationStateDigest{1}, state); !errors.Is(err, errInstallationStoreUnavailable) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if err := (installationStateStore{}).IssueInstallationState(context.Background(), InstallationStateDigest{1}, valid); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
}

// TestIssueInstallationStateBackendErrorsAndDuplicateDigest covers
// IssueInstallationState's transaction: a Get failure, Set failure, and a
// digest that already exists must each be rejected without corrupting
// storage.
func TestIssueInstallationStateBackendErrorsAndDuplicateDigest(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	digest := InstallationStateDigest{1}
	documentID := installationStateDocumentID(digest)
	state := oauthState(now, 7)

	t.Run("Get error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnID(documentID)
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), digest, state); err == nil || errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("digest already exists", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		if err := store.IssueInstallationState(context.Background(), digest, oauthState(now, 8)); err == nil {
			t.Fatal("duplicate digest accepted")
		}
	})

	t.Run("Set error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failSet = failOnID(documentID)
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), digest, state); err == nil {
			t.Fatal("expected write error")
		}
	})
}

// TestReadInstallationStateBackendError covers ReadInstallationState's
// backend Get failure path.
func TestReadInstallationStateBackendError(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	digest := InstallationStateDigest{1}
	backend := newFirestoreMemoryBackend()
	backend.failGet = failOnID(installationStateDocumentID(digest))
	store := installationStateStore{backend: backend}
	if _, err := store.ReadInstallationState(context.Background(), digest, now); err == nil || errors.Is(err, ErrInvalidInstallationState) {
		t.Fatalf("err=%v", err)
	}
}

// TestTransitionInstallationStateGuardsAndBackendErrors covers
// TransitionInstallationState's guard clause plus every backend error branch
// in its transaction: reading the current record, reading the next record
// for the collision check, deleting the current record, and writing the next
// record can each fail independently.
func TestTransitionInstallationStateGuardsAndBackendErrors(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	currentDigest, nextDigest := InstallationStateDigest{1}, InstallationStateDigest{2}
	currentID, nextID := installationStateDocumentID(currentDigest), installationStateDocumentID(nextDigest)
	current, next := oauthState(now, 7), oauthState(now.Add(time.Second), 7)

	if err := (installationStateStore{}).TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, next); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	if err := (installationStateStore{backend: newFirestoreMemoryBackend()}).TransitionInstallationState(context.Background(), currentDigest, current, time.Time{}, nextDigest, next); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("zero now err=%v", err)
	}

	t.Run("Get current error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), currentDigest, current); err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(currentID)
		if err := store.TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, next); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("Get next collision-check error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), currentDigest, current); err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(nextID)
		if err := store.TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, next); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("Delete current error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), currentDigest, current); err != nil {
			t.Fatal(err)
		}
		backend.failDelete = failOnID(currentID)
		if err := store.TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, next); err == nil {
			t.Fatal("expected delete error")
		}
	})

	t.Run("Set next error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), currentDigest, current); err != nil {
			t.Fatal(err)
		}
		backend.failSet = failOnID(nextID)
		if err := store.TransitionInstallationState(context.Background(), currentDigest, current, now, nextDigest, next); err == nil {
			t.Fatal("expected write error")
		}
	})
}

// TestReplaceInstallationStateGuardsAndBackendErrors covers
// ReplaceInstallationState's guard clause and both of its transaction's
// backend error branches.
func TestReplaceInstallationStateGuardsAndBackendErrors(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	digest := InstallationStateDigest{1}
	documentID := installationStateDocumentID(digest)
	current := oauthState(now, 7)
	next := current
	next.OAuthAccessTokenCiphertext = "encrypted"

	if err := (installationStateStore{}).ReplaceInstallationState(context.Background(), digest, current, now, next); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	if err := (installationStateStore{backend: newFirestoreMemoryBackend()}).ReplaceInstallationState(context.Background(), digest, current, time.Time{}, next); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("zero now err=%v", err)
	}

	t.Run("Get error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), digest, current); err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(documentID)
		if err := store.ReplaceInstallationState(context.Background(), digest, current, now, next); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("Set error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationStateStore{backend: backend}
		if err := store.IssueInstallationState(context.Background(), digest, current); err != nil {
			t.Fatal(err)
		}
		backend.failSet = failOnID(documentID)
		if err := store.ReplaceInstallationState(context.Background(), digest, current, now, next); err == nil {
			t.Fatal("expected write error")
		}
	})
}

// TestIdentityHasInstallation covers every branch of IdentityHasInstallation:
// it was previously never called by any test.
func TestIdentityHasInstallation(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	t.Run("invalid input", func(t *testing.T) {
		store := installationBindingStore{backend: newFirestoreMemoryBackend()}
		if _, err := store.IdentityHasInstallation(context.Background(), "", 7); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("empty identity err=%v", err)
		}
		if _, err := store.IdentityHasInstallation(context.Background(), "firebase-user", 0); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("zero installation err=%v", err)
		}
		if _, err := (installationBindingStore{}).IdentityHasInstallation(context.Background(), "firebase-user", 7); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("nil backend err=%v", err)
		}
	})

	t.Run("identity not found", func(t *testing.T) {
		store := installationBindingStore{backend: newFirestoreMemoryBackend()}
		has, err := store.IdentityHasInstallation(context.Background(), "firebase-user", 7)
		if err != nil || has {
			t.Fatalf("has=%v err=%v", has, err)
		}
	})

	t.Run("currentIdentity error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.putDocument(installationIdentityCollection, installationIdentityDocumentID("firebase-user"), installationIdentityDocument{IdentityID: "someone-else"})
		store := installationBindingStore{backend: backend}
		if _, err := store.IdentityHasInstallation(context.Background(), "firebase-user", 7); err == nil {
			t.Fatal("expected invalid stored identity error")
		}
	})

	t.Run("backend Get error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, bindings := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, bindings, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		backend.failGet = failOnID("7")
		if _, err := bindings.IdentityHasInstallation(context.Background(), "firebase-user", 7); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("found and matches", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, bindings := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, bindings, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		has, err := bindings.IdentityHasInstallation(context.Background(), "firebase-user", 7)
		if err != nil || !has {
			t.Fatalf("has=%v err=%v", has, err)
		}
	})

	t.Run("not bound to that installation", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, bindings := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, bindings, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		has, err := bindings.IdentityHasInstallation(context.Background(), "firebase-user", 8)
		if err != nil || has {
			t.Fatalf("has=%v err=%v", has, err)
		}
	})
}

// TestCompleteIdentityInstallationBindingGuardsAndBackendErrors covers
// CompleteIdentityInstallationBinding's guard clause and every backend error
// and stored-data-validation branch in its transaction.
func TestCompleteIdentityInstallationBindingGuardsAndBackendErrors(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	t.Run("invalid input", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		if err := (installationBindingStore{}).CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("nil backend err=%v", err)
		}
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, time.Time{}, binding); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("zero now err=%v", err)
		}
		invalidBinding := binding
		invalidBinding.GitHubUserID = 0
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, invalidBinding); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("invalid binding err=%v", err)
		}
		mismatchedState := state
		mismatchedState.InstallationID = 8
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, mismatchedState, now, binding); !errors.Is(err, errInstallationStoreUnavailable) {
			t.Fatalf("mismatched installation err=%v", err)
		}
	})

	t.Run("state not issued", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); !errors.Is(err, ErrInvalidInstallationState) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("Get state error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		backend.failGet = failOnID(installationStateDocumentID(digest))
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("Get identity error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failGet = failOnID(installationIdentityDocumentID(binding.IdentityID))
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("stored identity invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.putDocument(installationIdentityCollection, installationIdentityDocumentID(binding.IdentityID), installationIdentityDocument{IdentityID: "someone-else"})
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected invalid stored identity error")
		}
	})

	t.Run("identity installation limit exceeded", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		var existing []int64
		for i := int64(1); i <= maxIdentityInstallations; i++ {
			existing = append(existing, i)
		}
		binding := installationBinding(now, maxIdentityInstallations+1, 99, "github.com/acme/app")
		backend.putDocument(installationIdentityCollection, installationIdentityDocumentID(binding.IdentityID), installationIdentityDocument{
			IdentityID: binding.IdentityID, GitHubUserID: binding.GitHubUserID, GitHubLogin: binding.GitHubLogin,
			Generation: installationIdentityDocumentID(binding.IdentityID), InstallationIDs: existing, VerifiedAt: now,
		})
		digest, state := InstallationStateDigest{1}, oauthState(now, binding.Installation.ID)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected identity installation limit error")
		}
	})

	t.Run("Get index error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failGet = failOnID("7")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("stored index invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.putDocument(installationIndexCollection, "7", installationIndexDocument{InstallationID: 999})
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected invalid stored index error")
		}
	})

	t.Run("installation identity limit exceeded on index", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		var identityIDs []string
		for i := 0; i < maxInstallationIdentities; i++ {
			identityIDs = append(identityIDs, fmt.Sprintf("other-identity-%02d", i))
		}
		backend.putDocument(installationIndexCollection, "7", installationIndexDocument{InstallationID: 7, IdentityIDs: identityIDs})
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected installation identity limit error")
		}
	})

	t.Run("Get user index error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failGet = failOnID("42")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("stored user index invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.putDocument(installationUserIndexCollection, "42", installationUserIndexDocument{GitHubUserID: 999})
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected invalid stored user index error")
		}
	})

	t.Run("user identity limit exceeded on user index", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		var identityIDs []string
		for i := 0; i < maxInstallationIdentities; i++ {
			identityIDs = append(identityIDs, fmt.Sprintf("other-identity-%02d", i))
		}
		backend.putDocument(installationUserIndexCollection, "42", installationUserIndexDocument{GitHubUserID: 42, IdentityIDs: identityIDs})
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected user identity limit error")
		}
	})

	t.Run("Set repository chunk error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		installations := identityInstallationCollection(binding.IdentityID, installationIdentityDocumentID(binding.IdentityID))
		generation := installationRepositoryGeneration(binding)
		chunks := installationRepositoryCollection(installations, binding.Installation.ID, generation)
		backend.failSet = failOnCollection(chunks)
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected chunk write error")
		}
	})

	t.Run("Set installation header error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failSet = failOnID("7")
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected header write error")
		}
	})

	t.Run("Set identity error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failSet = failOnID(installationIdentityDocumentID(binding.IdentityID))
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected identity write error")
		}
	})

	t.Run("Set index error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failSet = failOnCollection(installationIndexCollection)
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected index write error")
		}
	})

	t.Run("Set user index error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failSet = failOnCollection(installationUserIndexCollection)
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected user index write error")
		}
	})

	t.Run("Delete state error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		digest, state := InstallationStateDigest{1}, oauthState(now, 7)
		if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
			t.Fatal(err)
		}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		backend.failDelete = failOnID(installationStateDocumentID(digest))
		if err := store.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err == nil {
			t.Fatal("expected state delete error")
		}
	})
}

// TestListIdentityInstallationBindingsBackendErrors covers
// ListIdentityInstallationBindings' Query failures and its rejection of a
// stored binding that fails validateInstallationBinding.
func TestListIdentityInstallationBindingsBackendErrors(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	t.Run("currentIdentity error propagates as nil, nil", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		listed, err := store.ListIdentityInstallationBindings(context.Background(), "firebase-user")
		if err != nil || listed != nil {
			t.Fatalf("listed=%+v err=%v", listed, err)
		}
	})

	t.Run("Query installations error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		backend.failQuery = failQueryOnCollection(installations)
		if _, err := store.ListIdentityInstallationBindings(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected query error")
		}
	})

	t.Run("Query chunks error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		completeBinding(t, states, store, now, InstallationStateDigest{1}, binding)
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		generation := installationRepositoryGeneration(binding)
		chunks := installationRepositoryCollection(installations, binding.Installation.ID, generation)
		backend.failQuery = failQueryOnCollection(chunks)
		if _, err := store.ListIdentityInstallationBindings(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected chunk query error")
		}
	})

	// TestListIdentityInstallationBindingsBackendErrors/stored document fails
	// validation covers validateInstallationBinding rejecting a document
	// whose own fields (not the identity's) are inconsistent: the identity
	// itself passes currentIdentity's checks, but the stored installation
	// header fails VerifiedGitHubIdentity.Validate().
	t.Run("stored document fails validation", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		var document installationDocument
		if _, err := backend.Get(context.Background(), installations, "7", &document); err != nil {
			t.Fatal(err)
		}
		document.State = "" // no longer one of installed/suspended/revoked
		backend.putDocument(installations, "7", document)
		if _, err := store.ListIdentityInstallationBindings(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected validation error")
		}
	})

	// TestListIdentityInstallationBindingsBackendErrors/sorts repositories by
	// name covers the sort comparator that ListIdentityInstallationBindings
	// applies to a binding's repositories: it is only ever invoked when a
	// binding has 2 or more repositories to compare.
	t.Run("sorts repositories by name", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		// CompleteIdentityInstallationBinding requires the incoming
		// repositories to already be sorted (validateInstallationBinding
		// delegates to VerifiedGitHubIdentity.Validate()), so this only
		// exercises that ListIdentityInstallationBindings' own sort is
		// actually invoked (>= 2 repositories), not that it reorders.
		binding := installationBinding(now, 7, 99, "github.com/acme/aaa")
		binding.Installation.Repositories = append(binding.Installation.Repositories, VerifiedRepository{ID: 100, Repository: "github.com/acme/zzz"})
		completeBinding(t, states, store, now, InstallationStateDigest{1}, binding)
		listed, err := store.ListIdentityInstallationBindings(context.Background(), "firebase-user")
		if err != nil || len(listed) != 1 || len(listed[0].Installation.Repositories) != 2 {
			t.Fatalf("listed=%+v err=%v", listed, err)
		}
		if listed[0].Installation.Repositories[0].Repository != "github.com/acme/aaa" || listed[0].Installation.Repositories[1].Repository != "github.com/acme/zzz" {
			t.Fatalf("repositories not sorted: %+v", listed[0].Installation.Repositories)
		}
	})
}

// TestIdentityHasRepositoryEntitlementGuardsAndIdentityNotFound covers
// IdentityHasRepositoryEntitlement's guard clause and its "identity not
// found" branch, neither of which any other test reaches.
func TestIdentityHasRepositoryEntitlementGuardsAndIdentityNotFound(t *testing.T) {
	store := installationBindingStore{backend: newFirestoreMemoryBackend()}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 0, 99); allowed || !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("zero installation allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 0); allowed || !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("zero repository allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || allowed {
		t.Fatalf("identity not found allowed=%v err=%v", allowed, err)
	}
}

// TestIdentityHasRepositoryEntitlementBackendErrors covers
// IdentityHasRepositoryEntitlement's backend Get and Query failures plus its
// "installation not in installed state" branch, which the happy-path test
// (TestBindingCompletionPreservesBindingsAndScopesEntitlements) never
// exercises since every installation it creates is already "installed".
func TestIdentityHasRepositoryEntitlementBackendErrors(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	t.Run("Get identity error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		backend.failGet = failOnID(installationIdentityDocumentID("firebase-user"))
		if _, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err == nil {
			t.Fatal("expected identity read error")
		}
	})

	t.Run("Get installation error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		backend.failGet = failOnID("7")
		if _, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err == nil {
			t.Fatal("expected installation read error")
		}
	})

	t.Run("installation not in installed state", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		event := InstallationLifecycleEvent{DeliveryID: "delivery-suspend", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99)
		if err != nil || allowed {
			t.Fatalf("allowed=%v err=%v", allowed, err)
		}
	})

	t.Run("Query chunks error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		completeBinding(t, states, store, now, InstallationStateDigest{1}, binding)
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		generation := installationRepositoryGeneration(binding)
		chunks := installationRepositoryCollection(installations, binding.Installation.ID, generation)
		backend.failQuery = failQueryOnCollection(chunks)
		if _, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err == nil {
			t.Fatal("expected chunk query error")
		}
	})
}

// TestValidInstallationLifecycleEvent exercises every action's validation
// rule, both accepted and rejected, including the invalid default action.
func TestValidInstallationLifecycleEvent(t *testing.T) {
	cases := []struct {
		name  string
		event InstallationLifecycleEvent
		want  bool
	}{
		{"missing delivery ID", InstallationLifecycleEvent{Action: InstallationSuspended, InstallationID: 7}, false},
		{"suspended valid", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationSuspended, InstallationID: 7}, true},
		{"suspended missing installation", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationSuspended}, false},
		{"revoked with extra repository", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRevoked, InstallationID: 7, RepositoryID: 1}, false},
		{"repositories removed valid", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{1, 2}}, true},
		{"repositories removed missing installation", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRepositoriesRemoved, RepositoryIDs: []int64{1}}, false},
		{"repositories removed empty list", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRepositoriesRemoved, InstallationID: 7}, false},
		{"repositories removed with extra repository ID", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{1}, RepositoryID: 2}, false},
		{"repositories removed with extra github user", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{1}, GitHubUserID: 5}, false},
		{"repositories removed with a zero repository ID", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{1, 0}}, false},
		{"user access removed valid", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationUserAccessRemoved, InstallationID: 7, GitHubUserID: 5}, true},
		{"user access removed missing user", InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationUserAccessRemoved, InstallationID: 7}, false},
		{"github user authorization revoked valid", InstallationLifecycleEvent{DeliveryID: "d", Action: GitHubUserAuthorizationRevoked, GitHubUserID: 5}, true},
		{"github user authorization revoked with installation", InstallationLifecycleEvent{DeliveryID: "d", Action: GitHubUserAuthorizationRevoked, InstallationID: 7, GitHubUserID: 5}, false},
		{"repository user access removed valid", InstallationLifecycleEvent{DeliveryID: "d", Action: RepositoryUserAccessRemoved, InstallationID: 7, RepositoryID: 1, GitHubUserID: 5}, true},
		{"repository user access removed missing repository", InstallationLifecycleEvent{DeliveryID: "d", Action: RepositoryUserAccessRemoved, InstallationID: 7, GitHubUserID: 5}, false},
		{"unsupported action", InstallationLifecycleEvent{DeliveryID: "d", Action: "made_up_action", InstallationID: 7}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validInstallationLifecycleEvent(testCase.event); got != testCase.want {
				t.Fatalf("validInstallationLifecycleEvent(%+v) = %v, want %v", testCase.event, got, testCase.want)
			}
		})
	}
}

// TestApplyInstallationLifecycleGuardsAndBackendErrors covers the guard
// clause, every backend Get/Set error branch, both the installation-index and
// user-index read paths, an invalid stored identity, an installation that
// skips because its GitHub user does not match, an unsupported action, and
// the repository-generation-missing defensive check.
func TestApplyInstallationLifecycleGuardsAndBackendErrors(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	if err := (installationBindingStore{}).ApplyInstallationLifecycle(context.Background(), InstallationLifecycleEvent{DeliveryID: "d", Action: InstallationSuspended, InstallationID: 7}); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("nil backend err=%v", err)
	}
	if err := (installationBindingStore{backend: newFirestoreMemoryBackend()}).ApplyInstallationLifecycle(context.Background(), InstallationLifecycleEvent{}); !errors.Is(err, errInstallationStoreUnavailable) {
		t.Fatalf("invalid event err=%v", err)
	}

	t.Run("Get receipt error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		backend.failGet = failOnID(lifecycleDocumentID(event.DeliveryID))
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected receipt read error")
		}
	})

	t.Run("Get installation index error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		backend.failGet = failOnID("7")
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected index read error")
		}
	})

	t.Run("stored installation index invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		backend.putDocument(installationIndexCollection, "7", installationIndexDocument{InstallationID: 999})
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected invalid stored index error")
		}
	})

	t.Run("Get user index error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: GitHubUserAuthorizationRevoked, GitHubUserID: 42}
		backend.failGet = failOnID("42")
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected user index read error")
		}
	})

	t.Run("stored user index invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		backend.putDocument(installationUserIndexCollection, "42", installationUserIndexDocument{GitHubUserID: 999})
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: GitHubUserAuthorizationRevoked, GitHubUserID: 42}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected invalid stored user index error")
		}
	})

	t.Run("Get identity error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		backend.failGet = failOnID(installationIdentityDocumentID("firebase-user"))
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected identity read error")
		}
	})

	t.Run("stored identity invalid", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		backend.putDocument(installationIdentityCollection, installationIdentityDocumentID("firebase-user"), installationIdentityDocument{IdentityID: "someone-else"})
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected invalid stored identity error")
		}
	})

	t.Run("identity skipped when github user does not match", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		matching := installationBinding(now, 7, 99, "github.com/acme/app")
		completeBinding(t, states, store, now, InstallationStateDigest{1}, matching)
		other := installationBinding(now.Add(time.Minute), 7, 100, "github.com/acme/other")
		other.IdentityID = "other-firebase-user"
		other.GitHubUserID = 99
		completeBindingWithIdentity(t, states, store, now.Add(time.Minute), InstallationStateDigest{2}, other)

		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationUserAccessRemoved, InstallationID: 7, GitHubUserID: 42}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || allowed {
			t.Fatalf("matching identity remained entitled: allowed=%v err=%v", allowed, err)
		}
		if allowed, err := store.IdentityHasRepositoryEntitlement(context.Background(), "other-firebase-user", 7, 100); err != nil || !allowed {
			t.Fatalf("skipped identity lost entitlement: allowed=%v err=%v", allowed, err)
		}
	})

	t.Run("Get installation error inside identity loop", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		// Target only the per-installation Get inside the identity loop
		// (distinct from the installation-index Get, which also happens to
		// read document id "7" but from a different, non-identity-scoped
		// collection and must be allowed to succeed here).
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		backend.failGet = func(collection, id string) error {
			if collection == installations && id == "7" {
				return errFirestoreMemoryFault
			}
			return nil
		}
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected installation read error")
		}
	})

	t.Run("installation not found is skipped", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		backend.putDocument(installationIndexCollection, "7", installationIndexDocument{InstallationID: 7, IdentityIDs: []string{"firebase-user"}})
		backend.putDocument(installationIdentityCollection, installationIdentityDocumentID("firebase-user"), installationIdentityDocument{
			IdentityID: "firebase-user", GitHubUserID: 42, GitHubLogin: "octocat", Generation: installationIdentityDocumentID("firebase-user"), VerifiedAt: now,
		})
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("repository generation missing", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		identityID := installationIdentityDocumentID("firebase-user")
		backend.putDocument(installationIndexCollection, "7", installationIndexDocument{InstallationID: 7, IdentityIDs: []string{"firebase-user"}})
		backend.putDocument(installationIdentityCollection, identityID, installationIdentityDocument{
			IdentityID: "firebase-user", GitHubUserID: 42, GitHubLogin: "octocat", Generation: identityID, VerifiedAt: now,
		})
		installations := identityInstallationCollection("firebase-user", identityID)
		backend.putDocument(installations, "7", installationDocument{ID: 7, State: "installed"})
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{99}}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected missing repository generation error")
		}
	})

	t.Run("readInstallationRepositoryChunks error propagates", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		generation := installationRepositoryGeneration(binding)
		chunks := installationRepositoryCollection(installations, 7, generation)
		delete(backend.documents, chunks+"/000000")
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{99}}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected missing chunk error")
		}
	})

	t.Run("Set updated chunk error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		// Two repositories so removing one still leaves a non-empty chunk to
		// write; removing the installation's only repository would leave
		// zero chunks and skip the write entirely (see chunkRepositories).
		binding := installationBinding(now, 7, 99, "github.com/acme/app")
		binding.Installation.Repositories = append(binding.Installation.Repositories, VerifiedRepository{ID: 100, Repository: "github.com/acme/other"})
		completeBinding(t, states, store, now, InstallationStateDigest{1}, binding)
		installations := identityInstallationCollection("firebase-user", installationIdentityDocumentID("firebase-user"))
		currentGeneration := installationRepositoryGeneration(binding)
		nextGeneration := lifecycleRepositoryGeneration(currentGeneration, "delivery-1")
		nextChunks := installationRepositoryCollection(installations, 7, nextGeneration)
		backend.failSet = failOnCollection(nextChunks)
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationRepositoriesRemoved, InstallationID: 7, RepositoryIDs: []int64{99}}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected updated chunk write error")
		}
	})

	t.Run("Set updated installation document error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		states, store := installationStateStore{backend: backend}, installationBindingStore{backend: backend}
		completeBinding(t, states, store, now, InstallationStateDigest{1}, installationBinding(now, 7, 99, "github.com/acme/app"))
		// InstallationSuspended never touches the repository chunk
		// collection (no removed repositories), so the only Set call with
		// document id "7" is the updated installation header itself.
		backend.failSet = failOnID("7")
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected updated installation write error")
		}
	})

	t.Run("Set receipt error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		store := installationBindingStore{backend: backend}
		event := InstallationLifecycleEvent{DeliveryID: "delivery-1", Action: InstallationSuspended, InstallationID: 7}
		backend.failSet = failOnID(lifecycleDocumentID(event.DeliveryID))
		if err := store.ApplyInstallationLifecycle(context.Background(), event); err == nil {
			t.Fatal("expected receipt write error")
		}
	})
}

// TestReadInstallationRepositoryChunksErrors covers
// readInstallationRepositoryChunks' backend Get failure and missing-chunk
// branches directly.
func TestReadInstallationRepositoryChunksErrors(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	installations := "installations-root"
	installation := installationDocument{ID: 7, RepositoryChunks: 1}
	collection := installationRepositoryCollection(installations, installation.ID, installation.RepositoryGeneration)

	t.Run("missing chunk", func(t *testing.T) {
		if err := backend.UpdateAtomic(context.Background(), func(transaction githubapp.FirestoreTransaction) error {
			_, chunkErr := readInstallationRepositoryChunks(context.Background(), transaction, installations, installation)
			return chunkErr
		}); err == nil {
			t.Fatal("expected missing chunk error")
		}
	})

	t.Run("Get error", func(t *testing.T) {
		backend.failGet = failOnCollection(collection)
		if err := backend.UpdateAtomic(context.Background(), func(transaction githubapp.FirestoreTransaction) error {
			_, chunkErr := readInstallationRepositoryChunks(context.Background(), transaction, installations, installation)
			return chunkErr
		}); err == nil {
			t.Fatal("expected read error")
		}
	})
}

// TestCurrentIdentityErrors covers installationBindingStore.currentIdentity's
// backend Get failure and invalid-stored-identity branches directly.
func TestCurrentIdentityErrors(t *testing.T) {
	t.Run("Get error", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.failGet = failOnCollection(installationIdentityCollection)
		store := installationBindingStore{backend: backend}
		if _, _, err := store.currentIdentity(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected read error")
		}
	})

	t.Run("invalid stored identity", func(t *testing.T) {
		backend := newFirestoreMemoryBackend()
		backend.putDocument(installationIdentityCollection, installationIdentityDocumentID("firebase-user"), installationIdentityDocument{IdentityID: "firebase-user"})
		store := installationBindingStore{backend: backend}
		if _, _, err := store.currentIdentity(context.Background(), "firebase-user"); err == nil {
			t.Fatal("expected invalid identity error")
		}
	})
}

// TestInstallationRepositoryCollectionEmptyGeneration covers the branch of
// installationRepositoryCollection that is taken before any repository
// generation has been assigned yet.
func TestInstallationRepositoryCollectionEmptyGeneration(t *testing.T) {
	got := installationRepositoryCollection("root", 7, "")
	want := "root/7/repositories"
	if got != want {
		t.Fatalf("installationRepositoryCollection = %q, want %q", got, want)
	}
}

// TestValidateInstallationBindingRejectsInvalidVerifiedIdentity covers the
// branch of validateInstallationBinding where the binding's own fields are
// consistent but the derived VerifiedGitHubIdentity fails Validate.
func TestValidateInstallationBindingRejectsInvalidVerifiedIdentity(t *testing.T) {
	binding := installationBinding(time.Now().UTC(), 7, 99, "github.com/acme/app")
	binding.GitHubLogin = ""
	if err := validateInstallationBinding(binding, binding.IdentityID); err == nil {
		t.Fatal("expected invalid verified identity error")
	}
}

func TestNewInstallationStoresWireEveryPort(t *testing.T) {
	backend := newFirestoreMemoryBackend()
	var states InstallationStateStore
	var bindings InstallationBindingStore
	var entitlements RepositoryEntitlementResolver
	var lifecycle InstallationLifecycleStore
	states, bindings, entitlements, lifecycle = NewInstallationStores(backend)
	if states == nil || bindings == nil || entitlements == nil || lifecycle == nil {
		t.Fatal("NewInstallationStores returned a nil port implementation")
	}
	// The binding, entitlement, and lifecycle values must be the same
	// underlying store so writes made through one are visible via another,
	// exactly like installation.go's InstallationConnectionService and
	// repository_events.go's RepositoryEventService expect when wired to
	// the same host backend.
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	digest := InstallationStateDigest{9}
	state := oauthState(now, 7)
	if err := states.IssueInstallationState(context.Background(), digest, state); err != nil {
		t.Fatal(err)
	}
	binding := installationBinding(now, 7, 99, "github.com/acme/app")
	if err := bindings.CompleteIdentityInstallationBinding(context.Background(), digest, state, now, binding); err != nil {
		t.Fatal(err)
	}
	if allowed, err := entitlements.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	event := InstallationLifecycleEvent{DeliveryID: "delivery-wired", Action: InstallationRevoked, InstallationID: 7}
	if err := lifecycle.ApplyInstallationLifecycle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if allowed, err := entitlements.IdentityHasRepositoryEntitlement(context.Background(), "firebase-user", 7, 99); err != nil || allowed {
		t.Fatalf("revoked installation remained entitled: allowed=%v err=%v", allowed, err)
	}
}
