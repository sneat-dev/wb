package cmdrun

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"io"
	"time"
)

func printHistory(out io.Writer, result runexec.HistoryResult, jsonOut bool) error {
	days, path, summary := result.Days, result.Path, result.Summary
	if jsonOut {
		return json.NewEncoder(out).Encode(summary)
	}
	if _, err := fmt.Fprintf(out, "Governed commands · %d days · %s\n", days, path); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "operations %d · failed %d · running %d · wall %s · CPU %s\n",
		summary.Operations, summary.Failed, summary.Running,
		time.Duration(summary.WallMS)*time.Millisecond,
		time.Duration(summary.UserCPUMS+summary.SystemCPUMS)*time.Millisecond); err != nil {
		return err
	}
	for _, kind := range summary.Kinds {
		if _, err := fmt.Fprintf(out, "%-20s %4d runs  %2d failed  p50 %s  p95 %s  total %s\n",
			kind.Kind, kind.Operations, kind.Failed,
			time.Duration(kind.P50MS)*time.Millisecond,
			time.Duration(kind.P95MS)*time.Millisecond,
			time.Duration(kind.WallMS)*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

func printQueue(out io.Writer, listing runqueue.QueueListing, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(listing)
	}
	if _, err := fmt.Fprintf(out, "WB CPU queue · budget %d · heavy k %d\n", listing.Budget, listing.HeavyK); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "running (%d):\n", len(listing.Running)); err != nil {
		return err
	}
	for _, entry := range listing.Running {
		if _, err := fmt.Fprintf(out, "  pid %-8d %-16s %-8s units %-3d %s\n", entry.PID, entry.Summary, entry.Age.Round(time.Second), entry.Units, entry.Worktree); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "waiting (%d):\n", len(listing.Waiting)); err != nil {
		return err
	}
	for _, entry := range listing.Waiting {
		if _, err := fmt.Fprintf(out, "  pid %-8d %-16s %-8s %s\n", entry.PID, entry.Summary, entry.Age.Round(time.Second), entry.Worktree); err != nil {
			return err
		}
	}
	return nil
}
