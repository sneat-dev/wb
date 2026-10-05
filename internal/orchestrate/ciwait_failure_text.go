package orchestrate

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func compactFailureAnnotation(value string, limit int) string {
	return truncateCIFailureText(strings.Join(strings.Fields(value), " "), limit)
}

func githubActionsRunAndJob(rawURL string) (runID, jobID string, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host != "github.com" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+4 < len(parts); index++ {
		if parts[index] == "actions" && parts[index+1] == "runs" && parts[index+3] == "job" && decimalID(parts[index+2]) && decimalID(parts[index+4]) {
			return parts[index+2], parts[index+4], true
		}
	}
	return "", "", false
}

// decimalID reports whether a run or job id is plain ASCII digits. The ids
// end up in a command WB tells an agent to run, and url.Parse has already
// decoded the path, so anything else could smuggle shell syntax in.
func decimalID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func failedJobLogExcerpt(raw string, maximumLines int) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(stripFailedJobLogLinePrefix(strings.TrimSpace(line)))
		if line != "" {
			filtered = append(filtered, redactFailedJobLogLine(line))
		}
	}
	if maximumLines > 0 && len(filtered) > maximumLines {
		filtered = append([]string{"… earlier failed-job log lines omitted …"}, filtered[len(filtered)-maximumLines:]...)
	}
	return strings.Join(filtered, "\n")
}

// stripFailedJobLogLinePrefix removes the "<job>\t<step>\t<timestamp> "
// prefix `gh run view --log-failed` puts on every line, so an excerpt shows
// the message rather than mostly-repeated job/step columns and a timestamp.
// A line that does not match this shape (no such invocation is guaranteed to
// produce it, and the caller must not assume it did) is returned unchanged.
func stripFailedJobLogLinePrefix(line string) string {
	rest := line
	if fields := strings.SplitN(line, "\t", 3); len(fields) == 3 {
		rest = fields[2]
	} else {
		return line
	}
	if index := strings.IndexByte(rest, ' '); index >= 0 {
		if _, err := time.Parse(time.RFC3339Nano, rest[:index]); err == nil {
			return rest[index+1:]
		}
	}
	return rest
}

func redactFailedJobLogLine(line string) string {
	for _, marker := range []string{"ghp_", "github_pat_"} {
		for {
			start := strings.Index(line, marker)
			if start < 0 {
				break
			}
			end := start + len(marker)
			for end < len(line) && ((line[end] >= 'a' && line[end] <= 'z') || (line[end] >= 'A' && line[end] <= 'Z') || (line[end] >= '0' && line[end] <= '9') || line[end] == '_') {
				end++
			}
			line = line[:start] + "[REDACTED]" + line[end:]
		}
	}
	return line
}

const (
	// maxFailureFindingChecks bounds how many failed checks a checks-failed
	// finding names (#600): enough to point at every likely culprit without
	// turning the finding into a copy of the checks list.
	maxFailureFindingChecks = 3
	// maxFailureFindingLineLength bounds each named check's diagnosis line.
	maxFailureFindingLineLength = 200
)

// summarizeCheckFailures composes the bounded, sanitized "which check, which
// line" text a checks-failed refusal or finding names (#600): up to
// maxFailureFindingChecks checks, each with its first available diagnosis
// line (a check-run annotation, or the job log's first "##[error]" line, or
// its first nonblank line) capped at maxFailureFindingLineLength characters.
// A check with no annotation or log excerpt available at all names only
// itself. An empty details slice - a refusal that observed no FailureDetails,
// e.g. because the failure was a policy gap rather than a red check - yields
// an empty string, leaving the caller's existing reason untouched.
func summarizeCheckFailures(details []CIFailureDetail) string {
	if len(details) == 0 {
		return ""
	}
	named := details
	if len(named) > maxFailureFindingChecks {
		named = named[:maxFailureFindingChecks]
	}
	lines := make([]string, 0, len(named))
	for _, detail := range named {
		lines = append(lines, failureFindingLine(detail))
	}
	summary := strings.Join(lines, "; ")
	if remaining := len(details) - len(named); remaining > 0 {
		summary += fmt.Sprintf(" (+%d more failed check", remaining)
		if remaining > 1 {
			summary += "s"
		}
		summary += ")"
	}
	return summary
}

// failureFindingLine names one failed check and, where available, its first
// diagnosis line, both `strconv.Quote`d so provider text stays visibly
// delimited data: it can never be misread as part of WB's own "resume
// with …" guidance appended around this text by a caller.
func failureFindingLine(detail CIFailureDetail) string {
	name := sanitizeFailureFindingText(detail.Check)
	if name == "" {
		name = "(unnamed check)"
	}
	name = truncateFailureFindingText(name)
	if line := firstFailureFindingLine(detail); line != "" {
		return strconv.Quote(name) + ": " + strconv.Quote(line)
	}
	return strconv.Quote(name)
}

// firstFailureFindingLine prefers GitHub's own deduplicated annotation (the
// terminal check-run endpoint WB already reads for landing receipts),
// rendered as "path:line: message" so a lint finding keeps its file and
// line; then the job log excerpt's last "--- FAIL"/"FAIL" line (a go test
// failure marker, which is a more specific pointer than an arbitrary line
// near it); then the excerpt's first "##[error]" line; then its first
// nonblank line at all. Every source here is provider text: this only
// formats, sanitizes, and bounds it, never parses it as anything but a
// string.
func firstFailureFindingLine(detail CIFailureDetail) string {
	for _, annotation := range detail.Annotations {
		if message := sanitizeFailureFindingText(annotation.Message); message != "" {
			line := message
			if path := sanitizeFailureFindingText(annotation.Path); path != "" && annotation.StartLine > 0 {
				line = fmt.Sprintf("%s:%d: %s", path, annotation.StartLine, message)
			}
			return truncateFailureFindingText(line)
		}
	}
	lines := strings.Split(detail.Excerpt, "\n")
	if line := lastMatchingExcerptLine(lines, goTestSubtestFailureLine); line != "" {
		return truncateFailureFindingText(line)
	}
	if line := lastMatchingExcerptLine(lines, goTestPackageFailureLine); line != "" {
		return truncateFailureFindingText(line)
	}
	if line := firstNonblankExcerptLine(lines, "##[error]"); line != "" {
		return truncateFailureFindingText(line)
	}
	if line := firstNonblankExcerptLine(lines, ""); line != "" {
		return truncateFailureFindingText(line)
	}
	return ""
}

// goTestSubtestFailureLineMarker matches a subtest's own failure line,
// "--- FAIL: Name (0.00s)". This names the actual test that failed, so it
// outranks the package summary's bare "FAIL" line below even when that
// summary line comes later in the excerpt (#600 round 3, minor 9).
var goTestSubtestFailureLineMarker = regexp.MustCompile(`^--- FAIL\b`)

func goTestSubtestFailureLine(line string) bool {
	return goTestSubtestFailureLineMarker.MatchString(line)
}

// goTestPackageFailureLineMarker matches the package summary's bare "FAIL"
// or "FAIL\tpackage\t0.01s" line: a fallback for when no subtest failure
// line ("--- FAIL: ...") is present in the excerpt at all.
var goTestPackageFailureLineMarker = regexp.MustCompile(`^FAIL\b`)

func goTestPackageFailureLine(line string) bool {
	return goTestPackageFailureLineMarker.MatchString(line)
}

// lastMatchingExcerptLine returns the LAST excerpt line satisfying match,
// skipping the "earlier lines omitted" placeholder failedJobLogExcerpt
// inserts. The last such line, not the first, because a job can retry or
// report several failures and the final one is closest to what actually
// stopped the run.
func lastMatchingExcerptLine(lines []string, match func(string) bool) string {
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" || line == "… earlier failed-job log lines omitted …" || !match(line) {
			continue
		}
		if sanitized := sanitizeFailureFindingText(line); sanitized != "" {
			return sanitized
		}
	}
	return ""
}

// firstNonblankExcerptLine returns the first excerpt line matching marker
// (with everything up to and including the marker stripped), or, with an
// empty marker, the first nonblank line at all. It skips the
// "earlier lines omitted" placeholder failedJobLogExcerpt inserts.
func firstNonblankExcerptLine(lines []string, marker string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "… earlier failed-job log lines omitted …" {
			continue
		}
		if marker != "" {
			index := strings.Index(line, marker)
			if index < 0 {
				continue
			}
			line = strings.TrimSpace(line[index+len(marker):])
			if line == "" {
				continue
			}
		}
		if sanitized := sanitizeFailureFindingText(line); sanitized != "" {
			return sanitized
		}
	}
	return ""
}

// ansiEscapeSequence matches a terminal control sequence (CSI, e.g.
// "\x1b[31m", or OSC, e.g. "\x1b]0;title\x07"): a single stray control
// character strip leaves the rest of the sequence ("[31m") behind as
// visible junk, so the whole sequence is removed as one unit first.
var ansiEscapeSequence = regexp.MustCompile(`\x1b(\[[0-9;?]*[a-zA-Z]|\][^\x07\x1b]*(\x07|\x1b\\))`)

// sanitizeFailureFindingText strips terminal escape sequences and control,
// format, and line/paragraph-separator characters from provider text before
// it reaches a finding. The text is a GitHub-sourced check name, annotation
// message, or job-log line: display data, never a template or a command, so
// this only removes characters a terminal or a JSON encoder could not render
// safely - it never rewrites or interprets the content.
func sanitizeFailureFindingText(text string) string {
	text = ansiEscapeSequence.ReplaceAllString(text, "")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			return -1
		default:
			return r
		}
	}, strings.TrimSpace(text))
}

// truncateFailureFindingText caps a diagnosis line at
// maxFailureFindingLineLength runes so one long line cannot dominate a
// bounded finding.
func truncateFailureFindingText(text string) string {
	return truncateCIFailureText(text, maxFailureFindingLineLength)
}

// truncateCIFailureText shares the existing rune cap used by annotation and finding presentation.
func truncateCIFailureText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}
