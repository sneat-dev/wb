//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

//nolint:paralleltest // the receive fixture and placement config use process-wide environment
func TestE2ESessionReceivePathRetainsPublishedSharedRegistration(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	shared := filepath.Join(t.TempDir(), "shared")
	config := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	mustWriteBranchConfig(t, filepath.Join(config, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+shared+"\n")
	spec := SessionReceiveSpec{AuthorityID: fixture.request.HandoffID, OperationID: fixture.request.HandoffID,
		MemberKey: "primary", RepositoryRemote: fixture.remote, Branch: fixture.request.Branch,
		Commit: fixture.request.BundleCommit, PinBranch: "wb-session/" + fixture.request.HandoffID,
		SourceWorkCommit: fixture.request.SourceWorkCommit, HandoverPath: fixture.request.HandoverPath,
		HandoverDigest: fixture.request.HandoverDigest}
	planned, err := SessionReceiveMemberPath(fixture.projectsRoot, spec)
	if err != nil || !strings.HasPrefix(planned, shared+string(filepath.Separator)) {
		t.Fatalf("shared planned checkout = %q, %v", planned, err)
	}
	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	if created.WorktreeDir != planned {
		t.Fatalf("published path %q differs from plan %q", created.WorktreeDir, planned)
	}
	changed := filepath.Join(t.TempDir(), "changed-placement")
	mustWriteBranchConfig(t, filepath.Join(config, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+changed+"\n")
	replayed, err := SessionReceiveMemberPath(fixture.projectsRoot, spec)
	if err != nil || replayed != created.WorktreeDir {
		t.Fatalf("registered pin path changed after placement config: got %q, err=%v, want %q", replayed, err, created.WorktreeDir)
	}
	bad := spec
	bad.AuthorityID = "bad/authority"
	if _, err := SessionReceiveMemberPath(fixture.projectsRoot, bad); err == nil {
		t.Fatal("unsafe authority ID admitted")
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "exact admitted handoff authority") {
		t.Fatalf("unfenced local replay accepted: %v", err)
	}
	if _, err := sessionmove.EncodeRequest(fixture.request); err != nil {
		t.Fatalf("fixture lost immutable request validity: %v", err)
	}
	if _, err := os.Lstat(created.WorktreeDir); err != nil {
		t.Fatalf("path inspection mutated published checkout: %v", err)
	}
}
