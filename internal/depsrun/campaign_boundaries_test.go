package depsrun

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/deps"
	engineprogress "github.com/sneat-dev/wb/internal/progress"
)

type campaignBuffer struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (b *campaignBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buffer.Write(p)
}
func (b *campaignBuffer) String() string { b.Lock(); defer b.Unlock(); return b.buffer.String() }
func TestBumpEarlyErrorsJoinTheActualOwnedCampaign(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"home", "missing", "corrupt", "parallel"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			directory := filepath.Join(root, "report")
			request := BumpRequest{ProjectsRoot: root, ReportDir: directory, Resume: true, Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo}}
			switch stage {
			case "home":
				blocking := filepath.Join(root, "file")
				if err := os.WriteFile(blocking, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				request.ProjectsRoot = filepath.Join(blocking, "child")
				request.ReportDir = ""
				request.Resume = false
			case "corrupt":
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "deps-bump.yaml"), []byte("[broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "parallel":
				if err := deps.WriteBumpReports(directory, deps.BumpReport{Parallel: 0}); err != nil {
					t.Fatal(err)
				}
			}
			var writer campaignBuffer
			campaign := cliprogress.NewCampaign(&writer, true, "owned bump")
			campaign.Report(engineprogress.Event{Phase: "select_repositories", State: engineprogress.Waiting})
			finishes := 0
			request.Finish = func(state string) {
				finishes++
				if state != "failed" {
					t.Fatalf("early finish=%q", state)
				}
				campaign.Finish(state)
			}
			d := DefaultDependencies(io.Discard)
			d.RunBump = func(context.Context, []deps.ReleaseEvent, []deps.Repository, deps.BumpOptions) (deps.BumpReport, error) {
				t.Fatal("early refusal reached engine")
				return deps.BumpReport{}, nil
			}
			if _, err := New(d).Bump(context.Background(), request); err == nil {
				t.Fatal("actual early native refusal missing")
			}
			if finishes != 1 || strings.Count(writer.String(), "owned bump: failed") != 1 {
				t.Fatalf("finish count=%d output=%q", finishes, writer.String())
			}
			before := writer.String()
			campaign.Report(engineprogress.Event{Detail: "late"})
			campaign.Finish("completed")
			if writer.String() != before {
				t.Fatal("late heartbeat/report access after return")
			}
		})
	}
}
