package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPeerEvidenceFacadeReportsFileFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := BranchCleanupOptions{RequireHosts: []string{branchEvidenceHost()}, PeerEvidence: []string{filepath.Join(root, "missing")}}
	if err := validatePeerEvidence(options, nil, time.Now()); err == nil || !strings.Contains(err.Error(), "read peer evidence") {
		t.Fatalf("missing evidence error = %v", err)
	}
	path := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	options.PeerEvidence = []string{path}
	if err := validatePeerEvidence(options, nil, time.Now()); err == nil || !strings.Contains(err.Error(), "decode peer evidence") {
		t.Fatalf("invalid evidence error = %v", err)
	}
	if err := validatePeerEvidence(BranchCleanupOptions{}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
}
