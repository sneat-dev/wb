package remotepublishview

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
)

type refusedWriter struct{ err error }

func (w refusedWriter) Write([]byte) (int, error) { return 0, w.err }
func TestPublicationRenderingPreservesBytesAndWriterErrors(t *testing.T) {
	t.Parallel()
	for _, dry := range []bool{false, true} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "dry", false: "published"}[dry], map[bool]string{true: "json", false: "text"}[jsonOut]}, "/"), func(t *testing.T) {
				t.Parallel()
				r := remotepublish.Result{DryRun: dry, Snapshot: remotestate.Snapshot{Login: "alice", Machine: "laptop"}, Report: remotepublish.Report{Key: "alice/laptop", RepositoriesScanned: 2, Attention: 1, Worktrees: 3, Location: "commit"}}
				var out bytes.Buffer
				if err := Write(&out, r, jsonOut); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "alice") {
					t.Fatalf("bytes=%q", out.String())
				}
				if !dry && !jsonOut && out.String() != "published alice/laptop: 2 repositories scanned, 1 need attention, 3 worktrees → commit\n" {
					t.Fatalf("text=%q", out.String())
				}
				sentinel := errors.New("writer refused")
				if err := Write(refusedWriter{sentinel}, r, jsonOut); !errors.Is(err, sentinel) {
					t.Fatalf("write=%v", err)
				}
			})
		}
	}
}
func TestSnapshotJSONRetainsActualDateMarshalRefusal(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	r := remotepublish.Result{DryRun: true, Snapshot: remotestate.Snapshot{PublishedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if err := Write(&out, r, true); err == nil || out.Len() != 0 {
		t.Fatalf("marshal err=%v bytes=%q", err, out.String())
	}
}
func TestProgressFailureUsesActualCallbacks(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := NewProgress(&out, true)
	callbacks := p.Callbacks()
	callbacks.Start(1)
	callbacks.Fail(errors.New("scan refused"))
	if !strings.Contains(out.String(), "remote publish: failed: scan refused") {
		t.Fatal(out.String())
	}
	var nilProgress *Progress
	nilProgress.Callbacks().Fail(errors.New("ignored"))
}
