package progress

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
)

type Checks struct {
	live         *Live
	observations int
}

func NewChecks(out io.Writer, enabled bool) *Checks {
	return NewChecksWithHeartbeat(out, enabled, Heartbeat)
}

func NewChecksWithHeartbeat(out io.Writer, enabled bool, heartbeat time.Duration) *Checks {
	return &Checks{live: NewLiveWithHeartbeat(out, enabled, heartbeat)}
}

func (progress *Checks) Start(repository, pullRequest, target, head string) {
	identity := repository
	if target != "" || head != "" {
		identity += " " + target + "@" + shortRevision(head)
	}
	if pullRequest != "" && (target != "" || head != "") {
		identity = repository + " PR " + pullRequest + " → " + target + "@" + shortRevision(head)
	} else if pullRequest != "" {
		identity = repository + " PR " + pullRequest
	}
	progress.live.Start("ci wait: observing " + identity)
}

func (progress *Checks) Report(event orchestrate.PullRequestWaitProgress) {
	progress.observations = event.Observation
	passed, pending, failed := checkBucketCounts(event.Result.Checks)
	completed := passed + failed
	message := fmt.Sprintf("ci wait: poll %d; checks %d/%d completed", event.Observation, completed, len(event.Result.Checks))
	if names := activeCheckNames(event.Result.Checks, 3); names != "" {
		message += "; running: " + names
	}
	if failed > 0 {
		message += fmt.Sprintf("; %d failed", failed)
	} else if pending > 0 {
		message += fmt.Sprintf("; %d pending", pending)
	}
	if event.Result.StableObservations > 0 {
		message += fmt.Sprintf("; stable %d/2", event.Result.StableObservations)
	}
	if event.NextPoll > 0 {
		message += "; next poll in " + event.NextPoll.String()
	}
	progress.live.Update(message)
}

func activeCheckNames(checks []orchestrate.RemoteCheck, limit int) string {
	names := make([]string, 0, limit)
	remaining := 0
	for _, check := range checks {
		if check.Bucket == "pass" || check.Bucket == "skipping" || check.Bucket == "fail" || check.Bucket == "cancel" {
			continue
		}
		name := strings.TrimPrefix(check.Name, "check-run:")
		name = strings.TrimPrefix(name, "status:")
		if len(names) < limit {
			names = append(names, name)
		} else {
			remaining++
		}
	}
	if remaining > 0 {
		names = append(names, fmt.Sprintf("+%d more", remaining))
	}
	return strings.Join(names, ", ")
}

func (progress *Checks) Finish(result orchestrate.PullRequestWaitResult) {
	progress.live.Finish(fmt.Sprintf(
		"ci wait: %s after %d polls; %d checks observed",
		result.Status, progress.observations, len(result.Checks),
	))
}

func (progress *Checks) FinishOperation(message string) {
	progress.live.Finish(message)
}

func (progress *Checks) OperationReporter(operation string) progresspkg.Reporter {
	return func(event progresspkg.Event) {
		parts := []string{operation}
		if event.Phase != "" {
			parts = append(parts, strings.ReplaceAll(event.Phase, "_", " "))
		}
		if event.Completed > 0 || event.Total > 0 {
			parts = append(parts, fmt.Sprintf("%d/%d", event.Completed, event.Total))
		}
		if event.Detail != "" {
			parts = append(parts, event.Detail)
		}
		if event.State != "" && event.State != progresspkg.Running {
			parts = append(parts, string(event.State))
		}
		progress.live.Update(strings.Join(parts, ": "))
	}
}

func (progress *Checks) Fail(err error) {
	message := "ci wait: failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message += ": " + err.Error()
	}
	progress.live.Finish(message)
}

func checkBucketCounts(checks []orchestrate.RemoteCheck) (passed, pending, failed int) {
	for _, check := range checks {
		switch check.Bucket {
		case "pass", "skipping":
			passed++
		case "fail", "cancel":
			failed++
		default:
			pending++
		}
	}
	return passed, pending, failed
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

// Update renders a caller-specific operation message using the same progress sink.
func (p *Checks) Update(message string) { p.live.Update(message) }
