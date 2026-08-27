package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

type ciWaitProgress struct {
	live         *liveProgress
	observations int
}

func newCIWaitProgress(out io.Writer, enabled bool) *ciWaitProgress {
	return &ciWaitProgress{live: newLiveProgress(out, enabled)}
}

func (progress *ciWaitProgress) start(repository, pullRequest, target, head string) {
	identity := repository + " " + target + "@" + shortRevision(head)
	if pullRequest != "" {
		identity = repository + " PR " + pullRequest + " → " + target + "@" + shortRevision(head)
	}
	progress.live.start("ci wait: observing " + identity)
}

func (progress *ciWaitProgress) report(event orchestrate.PullRequestWaitProgress) {
	progress.observations = event.Observation
	passed, pending, failed := checkBucketCounts(event.Result.Checks)
	message := fmt.Sprintf(
		"ci wait: poll %d; checks %d passed, %d pending, %d failed",
		event.Observation, passed, pending, failed,
	)
	if event.Result.StableObservations > 0 {
		message += fmt.Sprintf("; stable %d/2", event.Result.StableObservations)
	}
	if event.NextPoll > 0 {
		message += "; next poll in " + event.NextPoll.String()
	}
	progress.live.update(message)
}

func (progress *ciWaitProgress) finish(result orchestrate.PullRequestWaitResult) {
	progress.live.finish(fmt.Sprintf(
		"ci wait: %s after %d polls; %d checks observed",
		result.Status, progress.observations, len(result.Checks),
	))
}

func (progress *ciWaitProgress) fail(err error) {
	message := "ci wait: failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message += ": " + err.Error()
	}
	progress.live.finish(message)
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
