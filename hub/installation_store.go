package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
)

const (
	installationStateCollection     = "workbench_github_installation_states"
	installationIdentityCollection  = "workbench_github_installation_identities"
	installationIndexCollection     = "workbench_github_installation_indexes"
	installationUserIndexCollection = "workbench_github_user_indexes"
	installationLifecycleCollection = "workbench_github_installation_lifecycle"
	maxIdentityInstallations        = 40 // bounds user-wide invalidation below Firestore's 500-write transaction limit
	maxInstallationIdentities       = 10
	installationRepositoryChunkSize = 1000
)

var errInstallationStoreUnavailable = errors.New("workbench GitHub installation store is unavailable")

// NewInstallationStores wires the provider-owned installation stores to a
// host's github.com/sneat-dev/wb/api/githubapp FirestoreBackend. The returned
// binding store also implements RepositoryEntitlementResolver and
// InstallationLifecycleStore, so the same value can be assigned to every
// matching field on InstallationConnectionService, RepositoryEventService,
// and StatusService.
func NewInstallationStores(backend githubapp.FirestoreBackend) (InstallationStateStore, InstallationBindingStore, RepositoryEntitlementResolver, InstallationLifecycleStore) {
	states := installationStateStore{backend: backend}
	bindings := installationBindingStore{backend: backend}
	return states, bindings, bindings, bindings
}

type installationStateStore struct {
	backend githubapp.FirestoreBackend
}

func (store installationStateStore) IssueInstallationState(ctx context.Context, digest InstallationStateDigest, state InstallationState) error {
	if store.backend == nil || state.IdentityID == "" || state.IssuedAt.IsZero() || state.ExpiresAt.IsZero() {
		return errInstallationStoreUnavailable
	}
	documentID := installationStateDocumentID(digest)
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.FirestoreTransaction) error {
		var existing InstallationState
		found, err := transaction.Get(ctx, installationStateCollection, documentID, &existing)
		if err != nil {
			return fmt.Errorf("read GitHub installation state: %w", err)
		}
		if found {
			return errors.New("GitHub installation state digest already exists")
		}
		if err := transaction.Set(ctx, installationStateCollection, documentID, state); err != nil {
			return fmt.Errorf("write GitHub installation state: %w", err)
		}
		return nil
	})
}

func (store installationStateStore) ReadInstallationState(ctx context.Context, digest InstallationStateDigest, now time.Time) (state InstallationState, err error) {
	if store.backend == nil || now.IsZero() {
		return InstallationState{}, errInstallationStoreUnavailable
	}
	documentID := installationStateDocumentID(digest)
	found, err := store.backend.Get(ctx, installationStateCollection, documentID, &state)
	if err != nil {
		return InstallationState{}, fmt.Errorf("read GitHub installation state: %w", err)
	}
	if !found || !state.ExpiresAt.After(now) {
		return InstallationState{}, ErrInvalidInstallationState
	}
	return state, nil
}

func (store installationStateStore) TransitionInstallationState(ctx context.Context, currentDigest InstallationStateDigest, current InstallationState, now time.Time, nextDigest InstallationStateDigest, next InstallationState) error {
	if store.backend == nil || now.IsZero() {
		return errInstallationStoreUnavailable
	}
	currentID := installationStateDocumentID(currentDigest)
	nextID := installationStateDocumentID(nextDigest)
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.FirestoreTransaction) error {
		var stored InstallationState
		found, err := transaction.Get(ctx, installationStateCollection, currentID, &stored)
		if err != nil {
			return fmt.Errorf("read current GitHub installation state: %w", err)
		}
		if !found || stored != current || !stored.ExpiresAt.After(now) || currentID == nextID {
			return ErrInvalidInstallationState
		}
		var collision InstallationState
		found, err = transaction.Get(ctx, installationStateCollection, nextID, &collision)
		if err != nil {
			return fmt.Errorf("read next GitHub installation state: %w", err)
		}
		if found {
			return ErrInvalidInstallationState
		}
		if err := transaction.Delete(ctx, installationStateCollection, currentID); err != nil {
			return fmt.Errorf("consume current GitHub installation state: %w", err)
		}
		if err := transaction.Set(ctx, installationStateCollection, nextID, next); err != nil {
			return fmt.Errorf("write next GitHub installation state: %w", err)
		}
		return nil
	})
}

func (store installationStateStore) ReplaceInstallationState(ctx context.Context, digest InstallationStateDigest, current InstallationState, now time.Time, next InstallationState) error {
	if store.backend == nil || now.IsZero() {
		return errInstallationStoreUnavailable
	}
	documentID := installationStateDocumentID(digest)
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.FirestoreTransaction) error {
		var stored InstallationState
		found, err := transaction.Get(ctx, installationStateCollection, documentID, &stored)
		if err != nil {
			return fmt.Errorf("read GitHub installation state for replacement: %w", err)
		}
		if !found || stored != current || !stored.ExpiresAt.After(now) {
			return ErrInvalidInstallationState
		}
		if err := transaction.Set(ctx, installationStateCollection, documentID, next); err != nil {
			return fmt.Errorf("replace GitHub installation state: %w", err)
		}
		return nil
	})
}

func installationStateDocumentID(digest InstallationStateDigest) string {
	return hex.EncodeToString(digest[:])
}

type installationBindingStore struct {
	backend githubapp.FirestoreBackend
}

type installationIdentityDocument struct {
	IdentityID      string    `firestore:"identity_id"`
	GitHubUserID    int64     `firestore:"github_user_id"`
	GitHubLogin     string    `firestore:"github_login"`
	Generation      string    `firestore:"generation"`
	InstallationIDs []int64   `firestore:"installation_ids"`
	VerifiedAt      time.Time `firestore:"verified_at"`
}

type installationDocument struct {
	ID                   int64  `firestore:"id"`
	Account              string `firestore:"account"`
	AccountType          string `firestore:"account_type,omitempty"`
	RepositorySelection  string `firestore:"repository_selection"`
	State                string `firestore:"state,omitempty"`
	ManageURL            string `firestore:"manage_url,omitempty"`
	RepositoryGeneration string `firestore:"repository_generation,omitempty"`
	RepositoryChunks     int    `firestore:"repository_chunks,omitempty"`
}

type installationRepositoryChunk struct {
	Repositories []VerifiedRepository `firestore:"repositories"`
}

type installationIndexDocument struct {
	InstallationID int64    `firestore:"installation_id"`
	IdentityIDs    []string `firestore:"identity_ids"`
}

type installationUserIndexDocument struct {
	GitHubUserID int64    `firestore:"github_user_id"`
	IdentityIDs  []string `firestore:"identity_ids"`
}

func (store installationBindingStore) IdentityHasInstallation(ctx context.Context, identityID string, installationID int64) (bool, error) {
	if store.backend == nil || strings.TrimSpace(identityID) == "" || installationID <= 0 {
		return false, errInstallationStoreUnavailable
	}
	identity, found, err := store.currentIdentity(ctx, identityID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	var installation installationDocument
	found, err = store.backend.Get(ctx, identityInstallationCollection(identityID, identity.Generation), strconv.FormatInt(installationID, 10), &installation)
	if err != nil {
		return false, fmt.Errorf("read GitHub installation binding: %w", err)
	}
	return found && installation.ID == installationID, nil
}

func (store installationBindingStore) CompleteIdentityInstallationBinding(ctx context.Context, stateDigest InstallationStateDigest, expectedState InstallationState, now time.Time, binding IdentityInstallationBinding) error {
	if store.backend == nil || now.IsZero() || validateInstallationBinding(binding, binding.IdentityID) != nil ||
		expectedState.IdentityID != binding.IdentityID || expectedState.InstallationID != binding.Installation.ID {
		return errInstallationStoreUnavailable
	}
	repositoryGeneration := installationRepositoryGeneration(binding)
	stateID := installationStateDocumentID(stateDigest)
	identityID := installationIdentityDocumentID(binding.IdentityID)
	installationID := strconv.FormatInt(binding.Installation.ID, 10)
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.FirestoreTransaction) error {
		var storedState InstallationState
		found, err := transaction.Get(ctx, installationStateCollection, stateID, &storedState)
		if err != nil {
			return fmt.Errorf("read OAuth installation state: %w", err)
		}
		if !found || storedState != expectedState || !storedState.ExpiresAt.After(now) {
			return ErrInvalidInstallationState
		}

		var identity installationIdentityDocument
		found, err = transaction.Get(ctx, installationIdentityCollection, identityID, &identity)
		if err != nil {
			return fmt.Errorf("read GitHub installation identity: %w", err)
		}
		if found && (identity.IdentityID != binding.IdentityID || identity.GitHubUserID <= 0 || identity.Generation == "") {
			return errors.New("stored GitHub installation identity is invalid")
		}
		if found && identity.GitHubUserID != binding.GitHubUserID {
			return ErrUnauthorized
		}
		if !found {
			identity = installationIdentityDocument{IdentityID: binding.IdentityID, Generation: identityID}
		}
		identity.GitHubUserID = binding.GitHubUserID
		identity.GitHubLogin = binding.GitHubLogin
		identity.VerifiedAt = binding.VerifiedAt
		if !slices.Contains(identity.InstallationIDs, binding.Installation.ID) {
			identity.InstallationIDs = append(identity.InstallationIDs, binding.Installation.ID)
			slices.Sort(identity.InstallationIDs)
		}
		if len(identity.InstallationIDs) > maxIdentityInstallations {
			return errors.New("GitHub identity installation limit exceeded")
		}

		var index installationIndexDocument
		found, err = transaction.Get(ctx, installationIndexCollection, installationID, &index)
		if err != nil {
			return fmt.Errorf("read GitHub installation index: %w", err)
		}
		if found && index.InstallationID != binding.Installation.ID {
			return errors.New("stored GitHub installation index is invalid")
		}
		if !found {
			index.InstallationID = binding.Installation.ID
		}
		if !slices.Contains(index.IdentityIDs, binding.IdentityID) {
			index.IdentityIDs = append(index.IdentityIDs, binding.IdentityID)
			slices.Sort(index.IdentityIDs)
		}
		if len(index.IdentityIDs) > maxInstallationIdentities {
			return errors.New("GitHub installation identity limit exceeded")
		}
		userIndexID := strconv.FormatInt(binding.GitHubUserID, 10)
		var userIndex installationUserIndexDocument
		found, err = transaction.Get(ctx, installationUserIndexCollection, userIndexID, &userIndex)
		if err != nil {
			return fmt.Errorf("read GitHub user index: %w", err)
		}
		if found && userIndex.GitHubUserID != binding.GitHubUserID {
			return errors.New("stored GitHub user index is invalid")
		}
		if !found {
			userIndex.GitHubUserID = binding.GitHubUserID
		}
		if !slices.Contains(userIndex.IdentityIDs, binding.IdentityID) {
			userIndex.IdentityIDs = append(userIndex.IdentityIDs, binding.IdentityID)
			slices.Sort(userIndex.IdentityIDs)
		}
		if len(userIndex.IdentityIDs) > maxInstallationIdentities {
			return errors.New("GitHub user identity limit exceeded")
		}

		installations := identityInstallationCollection(binding.IdentityID, identity.Generation)
		header := installationDocument{
			ID: binding.Installation.ID, Account: binding.Installation.Account, AccountType: binding.Installation.AccountType,
			RepositorySelection: binding.Installation.RepositorySelection, State: binding.Installation.State,
			ManageURL: binding.Installation.ManageURL, RepositoryGeneration: repositoryGeneration,
			RepositoryChunks: (len(binding.Installation.Repositories) + installationRepositoryChunkSize - 1) / installationRepositoryChunkSize,
		}
		chunks := installationRepositoryCollection(installations, binding.Installation.ID, repositoryGeneration)
		for offset := 0; offset < len(binding.Installation.Repositories); offset += installationRepositoryChunkSize {
			end := min(offset+installationRepositoryChunkSize, len(binding.Installation.Repositories))
			chunk := installationRepositoryChunk{Repositories: append([]VerifiedRepository(nil), binding.Installation.Repositories[offset:end]...)}
			if err := transaction.Set(ctx, chunks, fmt.Sprintf("%06d", offset/installationRepositoryChunkSize), chunk); err != nil {
				return fmt.Errorf("write GitHub repository entitlements: %w", err)
			}
		}
		if err := transaction.Set(ctx, installations, installationID, header); err != nil {
			return fmt.Errorf("write GitHub installation binding: %w", err)
		}
		if err := transaction.Set(ctx, installationIdentityCollection, identityID, identity); err != nil {
			return fmt.Errorf("write GitHub installation identity: %w", err)
		}
		if err := transaction.Set(ctx, installationIndexCollection, installationID, index); err != nil {
			return fmt.Errorf("write GitHub installation index: %w", err)
		}
		if err := transaction.Set(ctx, installationUserIndexCollection, userIndexID, userIndex); err != nil {
			return fmt.Errorf("write GitHub user index: %w", err)
		}
		if err := transaction.Delete(ctx, installationStateCollection, stateID); err != nil {
			return fmt.Errorf("consume OAuth installation state: %w", err)
		}
		return nil
	})
}

// installationRepositoryGeneration hashes the repository set and verification
// timestamp. json.Marshal cannot fail here: every field reachable from
// IdentityInstallationBinding is a plain int64/string/time.Time, none of
// which json can reject, so this deliberately does not return an error the
// caller could never receive.
func installationRepositoryGeneration(binding IdentityInstallationBinding) string {
	payload, _ := json.Marshal(struct {
		Repositories []VerifiedRepository
		VerifiedAt   time.Time
	}{binding.Installation.Repositories, binding.VerifiedAt})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (store installationBindingStore) ListIdentityInstallationBindings(ctx context.Context, identityID string) ([]IdentityInstallationBinding, error) {
	if store.backend == nil || strings.TrimSpace(identityID) == "" {
		return nil, errInstallationStoreUnavailable
	}
	identity, found, err := store.currentIdentity(ctx, identityID)
	if err != nil || !found {
		return nil, err
	}
	var documents []installationDocument
	installations := identityInstallationCollection(identityID, identity.Generation)
	if err := store.backend.Query(ctx, installations, nil, maxIdentityInstallations, &documents); err != nil {
		return nil, fmt.Errorf("list GitHub installation bindings: %w", err)
	}
	bindings := make([]IdentityInstallationBinding, 0, len(documents))
	for _, document := range documents {
		var chunks []installationRepositoryChunk
		collection := installationRepositoryCollection(installations, document.ID, document.RepositoryGeneration)
		if err := store.backend.Query(ctx, collection, nil, 0, &chunks); err != nil {
			return nil, fmt.Errorf("list GitHub repository entitlements: %w", err)
		}
		repositories := make([]VerifiedRepository, 0)
		for _, chunk := range chunks {
			repositories = append(repositories, chunk.Repositories...)
		}
		slices.SortFunc(repositories, func(a, b VerifiedRepository) int {
			return strings.Compare(a.Repository, b.Repository)
		})
		binding := IdentityInstallationBinding{
			IdentityID:   identity.IdentityID,
			GitHubUserID: identity.GitHubUserID,
			GitHubLogin:  identity.GitHubLogin,
			Installation: VerifiedInstallation{
				ID:                  document.ID,
				Account:             document.Account,
				AccountType:         document.AccountType,
				RepositorySelection: document.RepositorySelection,
				Repositories:        repositories,
				State:               document.State,
				ManageURL:           document.ManageURL,
			},
			VerifiedAt: identity.VerifiedAt,
		}
		if err := validateInstallationBinding(binding, identityID); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func (store installationBindingStore) IdentityHasRepositoryEntitlement(ctx context.Context, identityID string, installationID, repositoryID int64) (bool, error) {
	if installationID <= 0 || repositoryID <= 0 {
		return false, errInstallationStoreUnavailable
	}
	identity, found, err := store.currentIdentity(ctx, identityID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	installations := identityInstallationCollection(identityID, identity.Generation)
	var installation installationDocument
	found, err = store.backend.Get(ctx, installations, strconv.FormatInt(installationID, 10), &installation)
	if err != nil {
		return false, fmt.Errorf("read GitHub installation binding: %w", err)
	}
	if !found || installation.ID != installationID || installation.State != "installed" {
		return false, nil
	}
	var chunks []installationRepositoryChunk
	collection := installationRepositoryCollection(installations, installationID, installation.RepositoryGeneration)
	if err := store.backend.Query(ctx, collection, nil, 0, &chunks); err != nil {
		return false, fmt.Errorf("list GitHub repository entitlements: %w", err)
	}
	for _, chunk := range chunks {
		for _, repository := range chunk.Repositories {
			if repository.ID == repositoryID {
				return true, nil
			}
		}
	}
	return false, nil
}

// ApplyInstallationLifecycle is idempotent by DeliveryID and atomically
// removes or disables every affected entitlement before it returns success.
// See InstallationLifecycleStore for the full contract.
func (store installationBindingStore) ApplyInstallationLifecycle(ctx context.Context, event InstallationLifecycleEvent) error {
	if store.backend == nil || !validInstallationLifecycleEvent(event) {
		return errInstallationStoreUnavailable
	}
	eventID := lifecycleDocumentID(event.DeliveryID)
	return store.backend.UpdateAtomic(ctx, func(transaction githubapp.FirestoreTransaction) error {
		var recorded InstallationLifecycleEvent
		found, err := transaction.Get(ctx, installationLifecycleCollection, eventID, &recorded)
		if err != nil {
			return fmt.Errorf("read GitHub installation lifecycle receipt: %w", err)
		}
		if found {
			if !reflect.DeepEqual(recorded, event) {
				return errors.New("GitHub installation lifecycle delivery ID was reused")
			}
			return nil
		}
		var identityIDs []string
		if event.Action == GitHubUserAuthorizationRevoked {
			var index installationUserIndexDocument
			found, err = transaction.Get(ctx, installationUserIndexCollection, strconv.FormatInt(event.GitHubUserID, 10), &index)
			if err != nil {
				return fmt.Errorf("read GitHub user index: %w", err)
			}
			if found && index.GitHubUserID != event.GitHubUserID {
				return errors.New("stored GitHub user index is invalid")
			}
			identityIDs = index.IdentityIDs
		} else {
			var index installationIndexDocument
			found, err = transaction.Get(ctx, installationIndexCollection, strconv.FormatInt(event.InstallationID, 10), &index)
			if err != nil {
				return fmt.Errorf("read GitHub installation index: %w", err)
			}
			if found && index.InstallationID != event.InstallationID {
				return errors.New("stored GitHub installation index is invalid")
			}
			identityIDs = index.IdentityIDs
		}
		type pendingUpdate struct {
			installations  string
			installationID string
			document       installationDocument
			chunks         []installationRepositoryChunk
		}
		updates := make([]pendingUpdate, 0, len(identityIDs))
		for _, identityID := range identityIDs {
			var identity installationIdentityDocument
			identityDocumentID := installationIdentityDocumentID(identityID)
			identityFound, getErr := transaction.Get(ctx, installationIdentityCollection, identityDocumentID, &identity)
			if getErr != nil {
				return fmt.Errorf("read GitHub installation identity: %w", getErr)
			}
			if !identityFound || identity.IdentityID != identityID || identity.Generation == "" {
				return errors.New("stored GitHub installation identity is invalid")
			}
			if (event.Action == GitHubUserAuthorizationRevoked || event.Action == InstallationUserAccessRemoved || event.Action == RepositoryUserAccessRemoved) && identity.GitHubUserID != event.GitHubUserID {
				continue
			}
			installationIDs := []int64{event.InstallationID}
			if event.Action == GitHubUserAuthorizationRevoked {
				installationIDs = identity.InstallationIDs
			}
			installations := identityInstallationCollection(identityID, identity.Generation)
			for _, numericInstallationID := range installationIDs {
				installationID := strconv.FormatInt(numericInstallationID, 10)
				var installation installationDocument
				installationFound, getErr := transaction.Get(ctx, installations, installationID, &installation)
				if getErr != nil {
					return fmt.Errorf("read GitHub installation binding: %w", getErr)
				}
				if !installationFound {
					continue
				}
				switch event.Action {
				case InstallationSuspended:
					installation.State = "suspended"
				case InstallationRevoked, InstallationUserAccessRemoved, GitHubUserAuthorizationRevoked:
					installation.State = "revoked"
				case InstallationRepositoriesRemoved, RepositoryUserAccessRemoved:
					if installation.RepositoryGeneration == "" && installation.RepositoryChunks == 0 {
						return errors.New("stored GitHub installation repository generation is missing")
					}
					chunks, readErr := readInstallationRepositoryChunks(ctx, transaction, installations, installation)
					if readErr != nil {
						return readErr
					}
					removed := event.RepositoryIDs
					if event.Action == RepositoryUserAccessRemoved {
						removed = []int64{event.RepositoryID}
					}
					repositories := removeRepositories(chunks, removed)
					generation := lifecycleRepositoryGeneration(installation.RepositoryGeneration, event.DeliveryID)
					chunks = chunkRepositories(repositories)
					installation.RepositoryGeneration = generation
					installation.RepositoryChunks = len(chunks)
					updates = append(updates, pendingUpdate{installations: installations, installationID: installationID, document: installation, chunks: chunks})
					continue
				}
				updates = append(updates, pendingUpdate{installations: installations, installationID: installationID, document: installation})
			}
		}
		for _, update := range updates {
			if len(update.chunks) > 0 {
				chunks := installationRepositoryCollection(update.installations, update.document.ID, update.document.RepositoryGeneration)
				for i, chunk := range update.chunks {
					if err := transaction.Set(ctx, chunks, fmt.Sprintf("%06d", i), chunk); err != nil {
						return fmt.Errorf("write lifecycle repository entitlements: %w", err)
					}
				}
			}
			if err := transaction.Set(ctx, update.installations, update.installationID, update.document); err != nil {
				return fmt.Errorf("write GitHub installation lifecycle state: %w", err)
			}
		}
		if err := transaction.Set(ctx, installationLifecycleCollection, eventID, event); err != nil {
			return fmt.Errorf("write GitHub installation lifecycle receipt: %w", err)
		}
		return nil
	})
}

func validInstallationLifecycleEvent(event InstallationLifecycleEvent) bool {
	if event.DeliveryID == "" {
		return false
	}
	switch event.Action {
	case InstallationSuspended, InstallationRevoked:
		return event.InstallationID > 0 && len(event.RepositoryIDs) == 0 && event.RepositoryID == 0 && event.GitHubUserID == 0
	case InstallationRepositoriesRemoved:
		if event.InstallationID <= 0 || len(event.RepositoryIDs) == 0 || event.RepositoryID != 0 || event.GitHubUserID != 0 {
			return false
		}
		for _, id := range event.RepositoryIDs {
			if id <= 0 {
				return false
			}
		}
		return true
	case InstallationUserAccessRemoved:
		return event.InstallationID > 0 && event.GitHubUserID > 0 && event.RepositoryID == 0 && len(event.RepositoryIDs) == 0
	case GitHubUserAuthorizationRevoked:
		return event.InstallationID == 0 && event.GitHubUserID > 0 && event.RepositoryID == 0 && len(event.RepositoryIDs) == 0
	case RepositoryUserAccessRemoved:
		return event.InstallationID > 0 && event.GitHubUserID > 0 && event.RepositoryID > 0 && len(event.RepositoryIDs) == 0
	default:
		return false
	}
}

func readInstallationRepositoryChunks(ctx context.Context, transaction githubapp.FirestoreTransaction, installations string, installation installationDocument) ([]installationRepositoryChunk, error) {
	collection := installationRepositoryCollection(installations, installation.ID, installation.RepositoryGeneration)
	chunks := make([]installationRepositoryChunk, 0, installation.RepositoryChunks)
	for i := 0; i < installation.RepositoryChunks; i++ {
		var chunk installationRepositoryChunk
		found, err := transaction.Get(ctx, collection, fmt.Sprintf("%06d", i), &chunk)
		if err != nil {
			return nil, fmt.Errorf("read GitHub repository entitlements: %w", err)
		}
		if !found {
			return nil, errors.New("stored GitHub repository entitlement chunk is missing")
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func removeRepositories(chunks []installationRepositoryChunk, removed []int64) []VerifiedRepository {
	removedIDs := make(map[int64]bool, len(removed))
	for _, id := range removed {
		removedIDs[id] = true
	}
	repositories := make([]VerifiedRepository, 0)
	for _, chunk := range chunks {
		for _, repository := range chunk.Repositories {
			if !removedIDs[repository.ID] {
				repositories = append(repositories, repository)
			}
		}
	}
	return repositories
}

func chunkRepositories(repositories []VerifiedRepository) []installationRepositoryChunk {
	chunks := make([]installationRepositoryChunk, 0, (len(repositories)+installationRepositoryChunkSize-1)/installationRepositoryChunkSize)
	for offset := 0; offset < len(repositories); offset += installationRepositoryChunkSize {
		end := min(offset+installationRepositoryChunkSize, len(repositories))
		chunks = append(chunks, installationRepositoryChunk{Repositories: append([]VerifiedRepository(nil), repositories[offset:end]...)})
	}
	return chunks
}

func lifecycleRepositoryGeneration(current, deliveryID string) string {
	digest := sha256.Sum256([]byte(current + "\x00" + deliveryID))
	return hex.EncodeToString(digest[:])
}

func lifecycleDocumentID(deliveryID string) string {
	digest := sha256.Sum256([]byte(deliveryID))
	return hex.EncodeToString(digest[:])
}

func (store installationBindingStore) currentIdentity(ctx context.Context, identityID string) (installationIdentityDocument, bool, error) {
	var identity installationIdentityDocument
	found, err := store.backend.Get(ctx, installationIdentityCollection, installationIdentityDocumentID(identityID), &identity)
	if err != nil {
		return installationIdentityDocument{}, false, fmt.Errorf("read GitHub installation identity: %w", err)
	}
	if !found {
		return installationIdentityDocument{}, false, nil
	}
	if identity.IdentityID != identityID || identity.GitHubUserID <= 0 || identity.GitHubLogin == "" || identity.Generation == "" || identity.VerifiedAt.IsZero() {
		return installationIdentityDocument{}, false, errors.New("stored GitHub installation identity is invalid")
	}
	return identity, true, nil
}

func installationIdentityDocumentID(identityID string) string {
	digest := sha256.Sum256([]byte(identityID))
	return hex.EncodeToString(digest[:])
}

func identityInstallationCollection(identityID, generation string) string {
	return installationIdentityCollection + "/" + installationIdentityDocumentID(identityID) + "/generations/" + generation + "/installations"
}

func installationRepositoryCollection(installations string, installationID int64, generation string) string {
	base := installations + "/" + strconv.FormatInt(installationID, 10)
	if generation == "" {
		return base + "/repositories"
	}
	return base + "/repository_generations/" + generation + "/chunks"
}

func validateInstallationBinding(binding IdentityInstallationBinding, identityID string) error {
	if binding.IdentityID != identityID || binding.GitHubUserID <= 0 || binding.VerifiedAt.IsZero() {
		return errors.New("stored GitHub installation binding is invalid")
	}
	identity := VerifiedGitHubIdentity{
		UserID:        binding.GitHubUserID,
		Login:         binding.GitHubLogin,
		Installations: []VerifiedInstallation{binding.Installation},
	}
	if err := identity.Validate(); err != nil {
		return errors.New("stored GitHub installation binding is invalid")
	}
	return nil
}

var _ InstallationStateStore = installationStateStore{}
var _ InstallationBindingStore = installationBindingStore{}
var _ RepositoryEntitlementResolver = installationBindingStore{}
var _ InstallationLifecycleStore = installationBindingStore{}
