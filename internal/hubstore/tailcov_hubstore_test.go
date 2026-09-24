package hubstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/hubconfig"
)

// tailCovAccessManifest is the ACL selection inGitDB reads before it hands a
// database back. It is written under the project's .ingitdb/access directory,
// exactly where a real operator would put it.
const tailCovAccessManifest = "enabled: true\ndatabase: wb\npolicies:\n  - tailcov-lockdown.yaml\n"

// tailCovAccessPolicy is a syntactically complete dtql.org/access/v1 policy for
// the `wb` database. It exists to put the store into the one state where
// inGitDB wraps the database in its policy decorator instead of returning the
// plain engine.
const tailCovAccessPolicy = `apiVersion: dtql.org/access/v1
composition: dalgo-hierarchical-v1
default: deny
kind: AccessPolicy
metadata:
  name: tailcov-lockdown
  visibility: public
scopes:
- path: /
  rules:
  - effect: allow
    id: tailcov-everything
    operations:
    - get
    - exists
    - query
    - insert
    - set
    - update
    - delete
target:
  database: wb
`

// tailCovWriteAccessConfig lays down an access configuration directory so the
// tests can drive inGitDB's manifest reader without depending on any file
// outside the temp directory.
func tailCovWriteAccessConfig(t *testing.T, projectPath, manifest, policy string) {
	t.Helper()
	accessDir := filepath.Join(projectPath, ".ingitdb", "access")
	if err := os.MkdirAll(accessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(accessDir, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if policy != "" {
		if err := os.WriteFile(filepath.Join(accessDir, "tailcov-lockdown.yaml"), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTailCovInGitDBRefusesABrokenAccessManifest pins the fail-loud rule for a
// half-installed access configuration: once the .ingitdb/access directory
// exists, a missing manifest is an error, and Open must surface it instead of
// quietly handing back an unenforced store.
func TestTailCovInGitDBRefusesABrokenAccessManifest(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	tailCovWriteAccessConfig(t, path, "", "")

	store, closer, err := Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err == nil {
		t.Fatalf("Open accepted a project with an access directory but no manifest: %v, %v", store, closer)
	}
	if store != nil {
		t.Fatalf("Open returned a store alongside the error: %v", store)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("Open error = %v, want it to name the project path %q", err, path)
	}
	if !strings.Contains(err.Error(), "manifest.yaml") {
		t.Fatalf("Open error = %v, want the manifest diagnostic", err)
	}
	if closer == nil {
		t.Fatal("Open must always return a non-nil Closer, even on failure")
	}
	if closeErr := closer.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
}

// TestTailCovInGitDBRefusesADatabaseWithoutSchemaManagement covers the state
// inGitDB itself creates when access policies are enabled: NewDatabase returns
// the policy decorator, which deliberately does not forward mutation
// capabilities. A hub store must be declared before it can be written to, so
// Open has to refuse such a database rather than return a store that cannot
// declare its collections.
func TestTailCovInGitDBRefusesADatabaseWithoutSchemaManagement(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	tailCovWriteAccessConfig(t, path, tailCovAccessManifest, tailCovAccessPolicy)

	store, closer, err := Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: path})
	if err == nil {
		t.Fatalf("Open accepted an ingitdb database with access policies applied: %v", store)
	}
	if !strings.Contains(err.Error(), "schema management") {
		t.Fatalf("Open error = %v, want the schema-management refusal", err)
	}
	if store != nil {
		t.Fatalf("Open returned a store alongside the error: %v", store)
	}
	if closer == nil {
		t.Fatal("Open must always return a non-nil Closer, even on failure")
	}
	if closeErr := closer.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	// The failure must not leave a half-declared project behind for the next
	// start to trip over.
	if _, statErr := os.Stat(filepath.Join(path, "root-collections.yaml")); statErr == nil {
		t.Fatal("a refused Open declared collections anyway")
	}
}
