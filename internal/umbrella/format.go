package umbrella

import (
	"fmt"
	"regexp"
	"strings"
)

var gitTrailerLinePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*: .+$`)

// AppendTrailer adds a "Wb-Umbrella: <registry>:<id>" trailer to a commit
// message unless the same trailer is already present.
func AppendTrailer(message string, ref Ref) string {
	if ref.IsZero() {
		return message
	}
	trailer := TrailerKey + ": " + ref.String()
	normalized := strings.ReplaceAll(message, "\r\n", "\n")
	trimmedRight := strings.TrimRight(normalized, "\n")
	for _, line := range strings.Split(trimmedRight, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), trailer) {
			return message
		}
	}
	if strings.TrimSpace(trimmedRight) == "" {
		return trailer
	}
	if endsWithTrailerBlock(trimmedRight) {
		return trimmedRight + "\n" + trailer
	}
	return trimmedRight + "\n\n" + trailer
}

func endsWithTrailerBlock(message string) bool {
	paragraphs := strings.Split(message, "\n\n")
	if len(paragraphs) < 2 {
		return false
	}
	last := strings.Split(paragraphs[len(paragraphs)-1], "\n")
	for _, line := range last {
		if !gitTrailerLinePattern.MatchString(line) {
			return false
		}
	}
	return true
}

// WithPartOf prepends "Part of <link>" to a pull-request body unless a Part of
// line referencing link or ref is already present.
func WithPartOf(body string, link string, ref Ref) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return body
	}
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "part of ") {
			continue
		}
		target := strings.TrimRight(strings.TrimSpace(trimmed[len("part of "):]), ".,;")
		if strings.EqualFold(target, link) || (!ref.IsZero() && strings.EqualFold(target, ref.String())) {
			return body
		}
	}
	line := "Part of " + link
	if strings.TrimSpace(body) == "" {
		return line
	}
	return line + "\n\n" + body
}

// StatusUpdate describes one lifecycle status event for an umbrella member.
type StatusUpdate struct {
	Task        string
	Repository  string
	PullRequest string // e.g. "#846" or "owner/repo#846"
	Outcome     string // e.g. "landed", "retired", "archived", "aborted (discarded)"
	LandedSHA   string
	ArchiveRef  string
	Note        string
}

// FormatStatusLine renders a single-line status comment containing repo, PR,
// outcome, and landed SHA or archive ref. Private text never enters this line.
// Bare "#N" pull-request labels are qualified with the member repository
// ("owner/repo#N") so cross-repository umbrella comments link the member PR.
func FormatStatusLine(u StatusUpdate) string {
	repo := sanitizeOneLine(u.Repository)
	pr := formatStatusPullRequest(repo, sanitizeOneLine(u.PullRequest))
	outcome := sanitizeOneLine(u.Outcome)
	sha := shortSHA(sanitizeOneLine(u.LandedSHA))
	archiveRef := sanitizeOneLine(u.ArchiveRef)
	task := sanitizeOneLine(u.Task)
	note := sanitizeOneLine(u.Note)

	var b strings.Builder
	b.WriteString("- `")
	b.WriteString(repo)
	b.WriteString("`")
	if pr != "" {
		fmt.Fprintf(&b, " (PR %s)", pr)
	}
	b.WriteString(": ")
	b.WriteString(outcome)

	details := make([]string, 0, 4)
	if sha != "" {
		details = append(details, "landed `"+sha+"`")
	}
	if archiveRef != "" {
		details = append(details, "archive `"+archiveRef+"`")
	}
	if note != "" {
		details = append(details, note)
	}
	if task != "" {
		details = append(details, "task `"+task+"`")
	}
	if len(details) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(details, "; "))
	}
	return b.String()
}

// Member summarizes one recorded task/repository member of an umbrella.
type Member struct {
	Task           string `json:"task"`
	Repository     string `json:"repository"`
	Branch         string `json:"branch,omitempty"`
	ClaimID        string `json:"claim_id,omitempty"`
	PullRequest    int    `json:"pull_request,omitempty"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
	State          string `json:"state"`
	LandedSHA      string `json:"landed_sha,omitempty"`
	ArchiveRef     string `json:"archive_ref,omitempty"`
	RestorePointer string `json:"restore_pointer,omitempty"`
	ReadyToClose   bool   `json:"ready_to_close"`
	BlockingReason string `json:"blocking_reason,omitempty"`
}

// FormatCloseSummary builds the final closing summary comment listing every
// member, its PR, state, landed SHA, and archive restore pointer.
func FormatCloseSummary(ref Ref, members []Member) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Closing umbrella `%s` — all %d member(s) landed or archived:\n\n", ref.String(), len(members))
	b.WriteString("| Task | Repository | PR | State | Landed SHA / Archive Ref |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, member := range members {
		pr := "-"
		if member.PullRequest > 0 {
			pr = fmt.Sprintf("%s#%d", member.Repository, member.PullRequest)
		}
		pointer := "-"
		switch {
		case member.LandedSHA != "":
			pointer = "`" + shortSHA(member.LandedSHA) + "`"
		case member.RestorePointer != "":
			pointer = "`" + sanitizeOneLine(member.RestorePointer) + "`"
		case member.ArchiveRef != "":
			pointer = "`" + sanitizeOneLine(member.ArchiveRef) + "`"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s |\n",
			sanitizeOneLine(member.Task),
			sanitizeOneLine(member.Repository),
			pr,
			sanitizeOneLine(member.State),
			pointer,
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

// CloseCommand returns the exact WB command to close ref with --apply.
func CloseCommand(ref Ref) string {
	return "wb umbrella close " + ref.Display() + " --apply"
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func sanitizeOneLine(text string) string {
	replacer := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(text))
}

func formatStatusPullRequest(repo, pr string) string {
	if pr == "" || repo == "" {
		return pr
	}
	if strings.HasPrefix(pr, "#") {
		return repo + pr
	}
	return pr
}
