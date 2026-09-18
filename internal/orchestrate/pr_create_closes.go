package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// closesLines renders one "Closes #N" line per issue, in the order given,
// for the top of a pull request body (#615). An empty slice renders nothing.
func closesLines(issues []int) string {
	if len(issues) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, issue := range issues {
		fmt.Fprintf(&builder, "Closes #%d\n", issue)
	}
	builder.WriteString("\n")
	return builder.String()
}

// withClosesPrefix prepends closesLines(issues) to body, exactly once, at
// the very top.
func withClosesPrefix(body string, issues []int) string {
	prefix := closesLines(issues)
	if prefix == "" {
		return body
	}
	return prefix + body
}

// issueReferencePattern matches a bare "#123" issue reference, the shape a
// task's original prompt or Work Log names an issue in.
var issueReferencePattern = regexp.MustCompile(`#(\d+)`)

// SuggestClosesFromPrompt finds issue numbers named in a task's original
// prompt (Work Log), in first-seen order with duplicates removed. It never
// adds them itself (#615): the caller prints them as a suggestion, and only
// --closes adds them to the pull request body.
func SuggestClosesFromPrompt(prompt string) []int {
	matches := issueReferencePattern.FindAllStringSubmatch(prompt, -1)
	seen := map[int]bool{}
	issues := make([]int, 0, len(matches))
	for _, match := range matches {
		number, err := strconv.Atoi(match[1])
		if err != nil || seen[number] {
			continue
		}
		seen[number] = true
		issues = append(issues, number)
	}
	return issues
}

// closingIssuesReferencesResponse is the shape of the GraphQL query below.
type closingIssuesReferencesResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ClosingIssuesReferences struct {
					Nodes []struct {
						Number int `json:"number"`
					} `json:"nodes"`
				} `json:"closingIssuesReferences"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// closingIssuesReferences reads GitHub's own record of which issues a pull
// request's merge will close — the same field the merged-body "Closes #N"
// convention feeds — via GraphQL, since the REST pulls endpoint does not
// expose it.
func closingIssuesReferences(ctx context.Context, repository, number string) ([]int, error) {
	owner, name, found := strings.Cut(repository, "/")
	if !found {
		return nil, fmt.Errorf("repository %q must be owner/name", repository)
	}
	pullNumber, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil {
		return nil, fmt.Errorf("pull request number %q: %w", number, err)
	}
	query := "query($owner:String!,$name:String!,$number:Int!){" +
		"repository(owner:$owner,name:$name){" +
		"pullRequest(number:$number){closingIssuesReferences(first:50){nodes{number}}}}}"
	response := githubExecute(ctx, "", "api", "graphql",
		"-f", "query="+query,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-F", "number="+strconv.Itoa(pullNumber))
	if response.Err != nil {
		message := strings.TrimSpace(string(response.Stderr))
		if message == "" {
			message = strings.TrimSpace(string(response.Stdout))
		}
		return nil, fmt.Errorf("read closing issues for %s#%s: %s", repository, number, message)
	}
	var decoded closingIssuesReferencesResponse
	if err := json.Unmarshal(response.Stdout, &decoded); err != nil {
		return nil, fmt.Errorf("decode closing issues for %s#%s: %w", repository, number, err)
	}
	issues := make([]int, 0, len(decoded.Data.Repository.PullRequest.ClosingIssuesReferences.Nodes))
	for _, node := range decoded.Data.Repository.PullRequest.ClosingIssuesReferences.Nodes {
		issues = append(issues, node.Number)
	}
	return issues, nil
}

// formatClosesFinding renders `wb pr land`'s informational finding for the
// linked issues a landing closes — or "no linked issue" when there are
// none, which is informational, never a refusal (#615).
func formatClosesFinding(issues []int) string {
	if len(issues) == 0 {
		return "no linked issue"
	}
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("#%d", issue))
	}
	return "closes: " + strings.Join(parts, ", ")
}
