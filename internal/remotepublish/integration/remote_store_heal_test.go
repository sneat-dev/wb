package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
)

// The provider `wb remote` opens is the remote store, so it heals the exact
// wb#899 wedge — a claim and snapshot staged but never committed — and the
// next claim lands. The clone's origin is the configured GitHub URL, rewritten
// to a local bare repository so nothing reaches the network.
//
//nolint:paralleltest // newRemoteFixture sets process-wide environment.
func TestOpenedRemoteStoreRecoversAStrandedStagedClaim(t *testing.T) {
	f := newRemoteFixture(t, "mac")
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state", Machine: "mac"}
	clonePath, err := remotepublish.ClonePath(f.projectsRoot, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, filepath.Dir(clonePath), "clone", "-q", f.origin, clonePath)
	remoteGit(t, clonePath, "remote", "set-url", "origin", remotepublish.CloneURL(cfg))
	remoteGit(t, clonePath, "config", "url."+f.origin+".insteadOf", remotepublish.CloneURL(cfg))
	stranded := filepath.Join(clonePath, filepath.FromSlash(gitrepo.ClaimPath("stranded")))
	if err := os.MkdirAll(filepath.Dir(stranded), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stranded, []byte("task: stranded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, clonePath, "add", "claims")

	provider, err := remotepublish.Open(cfg, f.projectsRoot, testExitFactory)
	if err != nil {
		t.Fatal(err)
	}
	claim := remotestate.Claim{SchemaVersion: remotestate.ClaimSchemaVersion, Task: "next-task", Login: "me", Machine: "mac", ClaimedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	if _, err := provider.Claim(context.Background(), claim, remotestate.ClaimNormal, ""); err != nil {
		t.Fatalf("Claim over a stranded staged claim = %v, want recovery", err)
	}
	files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, gitrepo.ClaimPath("next-task")) || strings.Contains(files, gitrepo.ClaimPath("stranded")) {
		t.Fatalf("origin files = %q, want next-task and not the stranded claim", files)
	}
	if status := remoteGit(t, clonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("clone status = %q, want clean", status)
	}
}
