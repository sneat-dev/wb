package gitops

import "testing"

// setupTracking returns a clone of a one-commit origin.
func setupTracking(t *testing.T) (local, seed string) {
	t.Helper()
	origin := t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main")

	seed = t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "base")
	git(t, seed, "push", "-q", "origin", "main")

	local = t.TempDir()
	git(t, local, "clone", "-q", origin, local)
	return local, seed
}

func TestTrackingCountsAheadAndBehind(t *testing.T) {
	local, seed := setupTracking(t)
	git(t, local, "commit", "-q", "--allow-empty", "-m", "local only")
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "remote only")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, local, "fetch", "-q", "origin")

	got, err := Tracking(local)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" || got.Upstream != "origin/main" {
		t.Fatalf("Branch/Upstream = %q/%q, want main/origin/main", got.Branch, got.Upstream)
	}
	if got.Ahead != 1 || got.Behind != 1 {
		t.Fatalf("Ahead/Behind = %d/%d, want 1/1", got.Ahead, got.Behind)
	}
	if !got.Diverged() {
		t.Fatal("Diverged() = false, want true when each side has a commit the other lacks")
	}
	if want := "main is 1 ahead, 1 behind origin/main"; got.Summary() != want {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), want)
	}
}

func TestTrackingBehindOnlyIsNotDiverged(t *testing.T) {
	local, seed := setupTracking(t)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "remote only")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, local, "fetch", "-q", "origin")

	got, err := Tracking(local)
	if err != nil {
		t.Fatal(err)
	}
	if got.Diverged() {
		t.Fatalf("Diverged() = true for %d ahead, %d behind; behind-only fast-forwards", got.Ahead, got.Behind)
	}
}

// A branch that names no upstream and one configured to track a ref that no
// longer resolves must be distinguishable: the first has nowhere to pull
// from, the second is a renamed or deleted branch that needs a human.
func TestTrackingSeparatesUnconfiguredFromUnresolvableUpstream(t *testing.T) {
	local, _ := setupTracking(t)
	git(t, local, "switch", "-q", "-c", "detour")

	got, err := Tracking(local)
	if err != nil {
		t.Fatal(err)
	}
	if got.Configured {
		t.Fatal("Configured = true for a branch that names no upstream")
	}
	if want := "detour has no upstream"; got.Summary() != want {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), want)
	}

	git(t, local, "config", "branch.detour.remote", "origin")
	git(t, local, "config", "branch.detour.merge", "refs/heads/gone")

	got, err = Tracking(local)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Configured {
		t.Fatal("Configured = false for a branch that names an upstream in config")
	}
	if got.Upstream != "" {
		t.Fatalf("Upstream = %q, want empty; refs/heads/gone does not resolve", got.Upstream)
	}
	if want := "detour tracks an upstream that no longer resolves"; got.Summary() != want {
		t.Fatalf("Summary() = %q, want %q", got.Summary(), want)
	}
}

// A clone with no remote-tracking refs has no baseline to call anything
// "unpushed" against: `--branches --not --remotes` subtracts nothing and
// yields the whole history. Reporting that as unlanded work would be worse
// than silence.
func TestUnpushedCommitsIsSilentWithoutRemoteRefs(t *testing.T) {
	local := t.TempDir()
	git(t, local, "init", "-q", "-b", "main")
	git(t, local, "commit", "-q", "--allow-empty", "-m", "one")
	git(t, local, "commit", "-q", "--allow-empty", "-m", "two")

	got, err := UnpushedCommits(local)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("UnpushedCommits = %v, want none; the whole history is not unlanded work", got)
	}
}

func TestUnpushedCommitsListsWorkOnAnyLocalBranch(t *testing.T) {
	local, _ := setupTracking(t)
	git(t, local, "switch", "-q", "-c", "side")
	git(t, local, "commit", "-q", "--allow-empty", "-m", "only here")
	git(t, local, "switch", "-q", "main")

	got, err := UnpushedCommits(local)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("UnpushedCommits = %v, want the one side-branch commit", got)
	}
}
