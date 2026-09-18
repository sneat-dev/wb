package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// dedupeIssues removes duplicate issue numbers, keeping first-seen order.
// Round 3, minor 6: cmd/wb's --closes parsing already dedupes its own input,
// but closesLines is the one place every caller's issue list converges
// (including a future one that is not so careful), so it dedupes again here
// rather than trust every caller to have done it.
func dedupeIssues(issues []int) []int {
	if len(issues) == 0 {
		return issues
	}
	seen := make(map[int]bool, len(issues))
	deduped := make([]int, 0, len(issues))
	for _, issue := range issues {
		if seen[issue] {
			continue
		}
		seen[issue] = true
		deduped = append(deduped, issue)
	}
	return deduped
}

// closesLines renders one "Closes #N" line per issue, in the order given,
// for the top of a pull request body (#615). An empty slice renders
// nothing; a duplicate issue number renders once.
func closesLines(issues []int) string {
	issues = dedupeIssues(issues)
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
//
// Round 3, minor 6: two shapes that contain "#<digits>" are never this
// repository's own issue number, and must not be suggested as one:
//   - "owner/repo#123" names an issue in a DIFFERENT repository — detected
//     by the immediately-preceding token (back to the previous whitespace)
//     containing a "/", the one character an issue number's own prefix
//     never has;
//   - a hex colour literal spelled entirely in decimal digits, such as
//     "#123456" — GitHub, and every other "#NNN" convention, only ever
//     writes a colour as exactly 3, 4, 6, or 8 hex digits, so a decimal run
//     of exactly one of those lengths is treated as a colour, not an issue.
func SuggestClosesFromPrompt(prompt string) []int {
	matches := issueReferencePattern.FindAllStringSubmatchIndex(prompt, -1)
	seen := map[int]bool{}
	issues := make([]int, 0, len(matches))
	for _, match := range matches {
		start, digitsStart, digitsEnd := match[0], match[2], match[3]
		if precedingTokenNamesAnotherRepository(prompt, start) {
			continue
		}
		digits := prompt[digitsStart:digitsEnd]
		if looksLikeHexColorLength(len(digits)) {
			continue
		}
		number, err := strconv.Atoi(digits)
		if err != nil || seen[number] {
			continue
		}
		seen[number] = true
		issues = append(issues, number)
	}
	return issues
}

// precedingTokenNamesAnotherRepository reports whether the non-whitespace
// token immediately before position start (an "#" issue reference) contains
// a "/" — the "owner/repo" shape immediately preceding "#123" in
// "sneat-dev/wb#123", which names an issue in that repository, never this
// one's own.
func precedingTokenNamesAnotherRepository(text string, start int) bool {
	tokenStart := start
	for tokenStart > 0 && !unicode.IsSpace(rune(text[tokenStart-1])) {
		tokenStart--
	}
	return strings.Contains(text[tokenStart:start], "/")
}

// looksLikeHexColorLength reports whether a run of decimal digits this long
// is exactly a length a CSS/hex colour literal is written in — see
// SuggestClosesFromPrompt.
//
// Only 6 (#RRGGBB) and 8 (#RRGGBBAA) count: those are the lengths a real
// colour literal is actually written at in practice, and both are rare as a
// GitHub issue number. 3 (#RGB) and 4 (#RGBA) are deliberately excluded even
// though they are valid CSS shorthand — those lengths are exactly the ones
// small, everyday issue numbers land on (e.g. #591, #614), so treating them
// as colours would silently drop real issue references far more often than
// it would ever catch a genuine shorthand colour typed as plain decimal
// digits in a prompt.
func looksLikeHexColorLength(length int) bool {
	switch length {
	case 6, 8:
		return true
	default:
		return false
	}
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

// existingClosesLinePattern matches an already-written "Closes #N" line, for
// missingClosesIssues's idempotency check.
var existingClosesLinePattern = regexp.MustCompile(`(?mi)^Closes #(\d+)\s*$`)

// missingClosesIssues returns the subset of issues (deduped, first-seen
// order preserved) that body's own "Closes #N" lines do not already name.
// Round 3, minor 6: this is what makes applying --closes to an adopted pull
// request idempotent — re-running it against a body that already carries
// every line makes no edit.
func missingClosesIssues(body string, issues []int) []int {
	already := map[int]bool{}
	for _, match := range existingClosesLinePattern.FindAllStringSubmatch(body, -1) {
		if number, err := strconv.Atoi(match[1]); err == nil {
			already[number] = true
		}
	}
	missing := make([]int, 0, len(issues))
	for _, issue := range dedupeIssues(issues) {
		if !already[issue] {
			missing = append(missing, issue)
		}
	}
	return missing
}

// applyClosesToAdoptedPullRequest adds whatever "Closes #N" lines --closes
// named that an already-open, adopted pull request's body does not already
// carry (round 3, minor 6). Before this fix, --closes silently did nothing
// once a pull request already existed: the body computed for a fresh `gh pr
// create` was only ever passed to the create call, never applied to one
// just adopted. A no-op when nothing is missing, so a second run is safe.
func applyClosesToAdoptedPullRequest(ctx context.Context, worktree, repository, url, currentBody string, issues []int, options Options) error {
	missing := missingClosesIssues(currentBody, issues)
	if len(missing) == 0 {
		return nil
	}
	newBody := withClosesPrefix(currentBody, missing)
	_, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "edit", url, "--repo", repository, "--body", newBody)
	return err
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
