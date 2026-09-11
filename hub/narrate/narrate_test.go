// Copyright 2026 Sneat Co.

package narrate

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func at(hour, minute, second int) time.Time {
	return time.Date(2026, 9, 11, hour, minute, second, 0, time.UTC)
}

// TestWriterRendersEveryOutcomeInTheSpecifiedColumns pins the exact bytes of
// every line the hub can narrate. The format is the operator's whole view of
// a running daemon, so a column that drifts is a regression, not a detail:
// these are the lines the feature specification shows.
func TestWriterRendersEveryOutcomeInTheSpecifiedColumns(t *testing.T) {
	for _, testCase := range []struct {
		name string
		line Line
		want string
	}{
		{
			name: "push queued",
			line: Line{At: at(14, 2, 11), Event: "push", Subject: "github.com/sneat-dev/wb", Action: "queued for 1 machines"},
			want: "14:02:11 push            github.com/sneat-dev/wb          queued for 1 machines",
		},
		{
			name: "push ignored",
			line: Line{At: at(14, 2, 11), Event: "push", Subject: "github.com/sneat-dev/wb-state", Action: "ignored: not on default branch"},
			want: "14:02:11 push            github.com/sneat-dev/wb-state    ignored: not on default branch",
		},
		{
			name: "push duplicate",
			line: Line{At: at(14, 7, 15), Event: "push", Subject: "github.com/sneat-dev/wb", Action: "duplicate delivery 8a1f; dropped"},
			want: "14:07:15 push            github.com/sneat-dev/wb          duplicate delivery 8a1f; dropped",
		},
		{
			name: "repository renamed",
			line: Line{At: at(14, 5, 40), Event: "repository", Subject: "github.com/sneat-dev/wb-hub", Action: "renamed from sneat-dev/workbench-gh-app; queued for laptop"},
			want: "14:05:40 repository      github.com/sneat-dev/wb-hub      renamed from sneat-dev/workbench-gh-app; queued for laptop",
		},
		{
			name: "installation",
			line: Line{At: at(14, 6, 2), Event: "installation", Subject: "sneat-dev", Action: "entitlements refreshed"},
			want: "14:06:02 installation    sneat-dev                        entitlements refreshed",
		},
		{
			name: "poll no change",
			line: Line{At: at(14, 6, 30), Event: "poll", Subject: "github.com/sneat-dev/wb", Action: "no change"},
			want: "14:06:30 poll            github.com/sneat-dev/wb          no change",
		},
		{
			name: "poll default branch",
			line: Line{At: at(14, 2, 11), Event: "poll", Subject: "github.com/sneat-dev/wb", Action: "default branch main -> 3f1c2a9; queued for laptop"},
			want: "14:02:11 poll            github.com/sneat-dev/wb          default branch main -> 3f1c2a9; queued for laptop",
		},
		{
			name: "rejection",
			line: Line{At: at(14, 8, 0), Event: "push", Subject: "github.com", Action: "rejected: bad signature"},
			want: "14:08:00 push            github.com                       rejected: bad signature",
		},
		{
			name: "subject wider than its column pushes the action right rather than truncating",
			line: Line{At: at(9, 0, 0), Event: "poll", Subject: "github.com/sneat-dev/a-repository-with-a-very-long-name", Action: "no change"},
			want: "09:00:00 poll            github.com/sneat-dev/a-repository-with-a-very-long-name no change",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			Writer{Out: &out}.Write(testCase.line)
			if got := strings.TrimSuffix(out.String(), "\n"); got != testCase.want {
				t.Fatalf("line\n got %q\nwant %q", got, testCase.want)
			}
			if !strings.HasSuffix(out.String(), "\n") {
				t.Fatal("a narrated line must end the line it is written on")
			}
		})
	}
}

// TestQuietWriterSilencesTheConsole is what `wb daemon serve --quiet` buys.
func TestQuietWriterSilencesTheConsole(t *testing.T) {
	var out bytes.Buffer
	Writer{Out: &out, Quiet: true}.Write(Line{At: at(1, 2, 3), Event: "poll", Subject: "github.com/sneat-dev/wb", Action: "no change"})
	if out.Len() != 0 {
		t.Fatalf("--quiet still wrote %q", out.String())
	}
}

// TestZeroWriterDiscards lets a caller that has nowhere to write pass the
// zero value rather than a nil check at every call site.
func TestZeroWriterDiscards(t *testing.T) {
	Writer{}.Write(Line{At: at(1, 2, 3), Event: "poll", Subject: "github.com/sneat-dev/wb", Action: "no change"})
}
