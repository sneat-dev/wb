package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/api/githubapp"
)

const (
	machineCredentialCollection = "workbench_machine_credentials"
	machineEnrollmentCollection = "workbench_machine_enrollments"
)

var errMachineCredentialUnavailable = errors.New("workbench machine credential is unavailable")

// NewMachineStores wires the hub-owned machine stores to a host's
// github.com/sneat-dev/wb/api/githubapp DocumentStore. One value satisfies
// both MachineCredentialStore and MachineCredentialResolver, so the same
// backing records answer enrollment and bearer resolution.
//
// The returned values are what MachineEnrollmentService.Store,
// NewMachineBearerResolver and MachineSnapshotService.Store expect, which is
// every persistence a loopback hub needs before an App is involved.
func NewMachineStores(backend githubapp.DocumentStore) (MachineCredentialStore, MachineCredentialResolver, MachineSnapshotStore) {
	credentials := machineCredentialStore{backend: backend}
	return credentials, credentials, machineSnapshotStore{backend: backend}
}

type machineCredentialDocument struct {
	Binding MachineCredentialBinding `firestore:"binding"`
}

type machineEnrollmentDocument struct {
	Binding      MachineCredentialBinding `firestore:"binding"`
	CredentialID string                   `firestore:"credential_id"`
}

// machineCredentialStore binds the opaque enrollment contract to private
// documents. Only peppered token digests are used as credential keys;
// plaintext tokens never reach this adapter.
type machineCredentialStore struct {
	backend githubapp.DocumentStore
}

func (store machineCredentialStore) RotateMachineCredential(ctx context.Context, binding MachineCredentialBinding, digest MachineTokenDigest) (stored MachineCredentialBinding, err error) {
	if store.backend == nil || strings.TrimSpace(binding.IdentityID) == "" || strings.TrimSpace(binding.MachineName) == "" || binding.IssuedAt.IsZero() {
		return MachineCredentialBinding{}, errMachineCredentialUnavailable
	}
	stored = binding
	// The machine id is derived, not generated, so re-enrolling the same
	// machine name under the same identity rotates the credential in place
	// rather than accumulating a new machine on every daemon restart.
	stored.MachineID = MachineID(binding.IdentityID, binding.MachineName)
	enrollmentID := stored.MachineID
	credentialID := machineCredentialID(digest)
	err = store.backend.UpdateAtomic(ctx, func(transaction githubapp.DocumentTransaction) error {
		var current machineEnrollmentDocument
		found, getErr := transaction.Get(ctx, machineEnrollmentCollection, enrollmentID, &current)
		if getErr != nil {
			return fmt.Errorf("read current machine enrollment: %w", getErr)
		}
		var collision machineCredentialDocument
		credentialFound, credentialErr := transaction.Get(ctx, machineCredentialCollection, credentialID, &collision)
		if credentialErr != nil {
			return fmt.Errorf("check machine credential digest: %w", credentialErr)
		}
		if credentialFound {
			return errors.New("machine credential digest already exists")
		}
		if found && current.CredentialID != "" && current.CredentialID != credentialID {
			if deleteErr := transaction.Delete(ctx, machineCredentialCollection, current.CredentialID); deleteErr != nil {
				return fmt.Errorf("revoke previous machine credential: %w", deleteErr)
			}
		}
		if setErr := transaction.Set(ctx, machineCredentialCollection, credentialID, machineCredentialDocument{Binding: stored}); setErr != nil {
			return fmt.Errorf("write machine credential: %w", setErr)
		}
		if setErr := transaction.Set(ctx, machineEnrollmentCollection, enrollmentID, machineEnrollmentDocument{Binding: stored, CredentialID: credentialID}); setErr != nil {
			return fmt.Errorf("write machine enrollment: %w", setErr)
		}
		return nil
	})
	if err != nil {
		return MachineCredentialBinding{}, err
	}
	return stored, nil
}

func (store machineCredentialStore) ResolveMachineCredential(ctx context.Context, digest MachineTokenDigest) (MachineCredentialBinding, error) {
	if store.backend == nil {
		return MachineCredentialBinding{}, errMachineCredentialUnavailable
	}
	var document machineCredentialDocument
	found, err := store.backend.Get(ctx, machineCredentialCollection, machineCredentialID(digest), &document)
	if err != nil || !found {
		return MachineCredentialBinding{}, errMachineCredentialUnavailable
	}
	if strings.TrimSpace(document.Binding.MachineID) == "" || strings.TrimSpace(document.Binding.MachineName) == "" ||
		strings.TrimSpace(document.Binding.IdentityID) == "" || document.Binding.IssuedAt.IsZero() {
		return MachineCredentialBinding{}, errMachineCredentialUnavailable
	}
	return document.Binding, nil
}

// MachineID derives the stable machine document id from the identity and the
// machine name. A self-hosting daemon needs it to recognise the machine it
// already enrolled without keeping a second record of the id.
func MachineID(identityID, machineName string) string {
	digest := sha256.Sum256([]byte(identityID + "\x00" + machineName))
	return "machine_" + hex.EncodeToString(digest[:])
}

func machineCredentialID(digest MachineTokenDigest) string {
	return hex.EncodeToString(digest[:])
}

var (
	_ MachineCredentialStore    = machineCredentialStore{}
	_ MachineCredentialResolver = machineCredentialStore{}
)
