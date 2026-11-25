package githubobserver

import (
	"regexp"
	"strings"
)

// pullRequestURLPattern is the canonical shape gh prints for a created pull
// request on any host: https://<host>[/<path>]/pull/<positive number>, the
// number bounded so it always fits an int.
var pullRequestURLPattern = regexp.MustCompile(`^https://[^\s/]+(?:/[^\s/]+)*/pull/[1-9][0-9]{0,17}$`)

// PullRequestURLFromOutput returns the last line of gh's output that is a pull
// request URL. gh can print notices (for example "Event subscriptions are in
// preview") around the URL, and such text must never be taken as the URL.
func PullRequestURLFromOutput(output string) string {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); pullRequestURLPattern.MatchString(line) {
			return line
		}
	}
	return ""
}
