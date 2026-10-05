package orchestrate

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

func TestImportedMainLineageRefusesActualOriginAdvanceAfterFetch(t *testing.T) {
	t.Parallel()
	repository, _, imported, _, _, _ := importedMainReceiptFixture(t)
	tree := gitMergeGraphTest(t, repository, "rev-parse", imported+"^{tree}")
	advanced := gitMergeGraphTest(t, repository, "commit-tree", tree, "-p", imported, "-m", "private origin advances after actual fetch")
	if advanced == imported {
		t.Fatal("native descendant did not advance")
	}
	fetch := []string{"fetch", "--no-tags", "origin", "refs/heads/main"}
	observed, ancestryReads := 0, 0
	run := &importedMainOwnerRunner{Runner: defaultRunner, dir: repository, args: fetch, ordinal: 1}
	run.after = func(ctx context.Context, dir string, args []string, result runner.Result) {
		if dir != repository {
			return
		}
		if len(args) > 0 && args[0] == "merge-base" {
			ancestryReads++
		}
		if !reflect.DeepEqual(args, fetch) {
			return
		}
		observed++
		if observed != 1 || result.ExitCode != 0 {
			t.Fatalf("actual fetch stage=%d %+v", observed, result)
		}
		if actual := gitMergeGraphTest(t, repository, "rev-parse", "--verify", "FETCH_HEAD^{commit}"); actual != imported {
			t.Fatalf("authentic fetched head=%s want=%s", actual, imported)
		}
		// Transfer the real native commit object, leaving FETCH_HEAD untouched.
		gitMergeGraphTest(t, repository, "push", "origin", advanced+":refs/heads/main")
	}
	got, err := verifyImportedMainLineage(t.Context(), run, repository, imported, "", 0)
	if got != "" || err == nil || !strings.Contains(err.Error(), "origin/main moved during attestation") || !strings.Contains(err.Error(), imported) || !strings.Contains(err.Error(), advanced) || observed != 1 || run.seen != 1 || ancestryReads != 0 {
		t.Fatalf("attestation=%s %v fetch=%d/%d later ancestry=%d", got, err, observed, run.seen, ancestryReads)
	}
	fetched := gitMergeGraphTest(t, repository, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	remote := strings.Fields(gitMergeGraphTest(t, repository, "ls-remote", "--heads", "origin", "refs/heads/main"))
	if fetched != imported || len(remote) != 2 || remote[0] != advanced || remote[1] != "refs/heads/main" {
		t.Fatalf("physical mismatch lost: fetched=%s remote=%v", fetched, remote)
	}
	if contains, e := isMergeAncestor(t.Context(), repository, imported, advanced); e != nil || !contains {
		t.Fatalf("genuine forward DAG=%t %v", contains, e)
	}
}
