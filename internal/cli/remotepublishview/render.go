package remotepublishview

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
)

func Write(out io.Writer, result remotepublish.Result, jsonOut bool) error {
	if result.DryRun {
		if jsonOut {
			return json.NewEncoder(out).Encode(result.Snapshot)
		}
		// Snapshot has only concrete YAML-supported fields, as its producer
		// already relies on in the snapshot digest. JSON date errors
		// remain reachable and propagate in the branch above.
		data, _ := remotestate.Encode(result.Snapshot)
		_, err := out.Write(data)
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(result.Report)
	}
	report := result.Report
	_, err := fmt.Fprintf(out, "published %s: %d repositories scanned, %d need attention, %d worktrees → %s\n", report.Key, report.RepositoriesScanned, report.Attention, report.Worktrees, report.Location)
	return err
}
