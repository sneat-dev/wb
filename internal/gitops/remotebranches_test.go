package gitops

import "testing"

func TestRemoteHasBranches(t *testing.T) {
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")

	local := t.TempDir()
	git(t, local, "init", "-q", "-b", "main")
	git(t, local, "remote", "add", "origin", origin)

	// Nothing pushed yet: the remote publishes no branches at all.
	has, err := RemoteHasBranches(local)
	if err != nil {
		t.Fatalf("RemoteHasBranches on an empty remote: %v", err)
	}
	if has {
		t.Fatal("RemoteHasBranches = true, want false before anything is pushed")
	}

	git(t, local, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, local, "push", "-q", "origin", "main")

	has, err = RemoteHasBranches(local)
	if err != nil {
		t.Fatalf("RemoteHasBranches after push: %v", err)
	}
	if !has {
		t.Fatal("RemoteHasBranches = false, want true once a branch is pushed")
	}
}

func TestRemoteHasBranchesErrorsWithoutOrigin(t *testing.T) {
	local := t.TempDir()
	git(t, local, "init", "-q", "-b", "main")

	if _, err := RemoteHasBranches(local); err == nil {
		t.Fatal("RemoteHasBranches = nil error, want failure when no origin is configured")
	}
}
