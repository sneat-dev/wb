package githubchecks

import (
	"strconv"
	"strings"
	"testing"
)

func TestCIFailureTextOwnerActionsIdentityAndPrefixPolicies(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name, input, run, job string
		ok                    bool
	}{
		{"valid nested", " https://github.com/acme/app/actions/runs/12/job/34 ", "12", "34", true},
		{"invalid escape", "https://github.com/%zz", "", "", false},
		{"other host", "https://example.test/acme/app/actions/runs/12/job/34", "", "", false},
		{"short", "https://github.com/acme/app/actions/runs/12", "", "", false},
		{"run syntax", "https://github.com/acme/app/actions/runs/12%3Bcmd/job/34", "", "", false},
		{"job syntax", "https://github.com/acme/app/actions/runs/12/job/34x", "", "", false},
		{"different route", "https://github.com/acme/app/checks/12/job/34", "", "", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			run, job, ok := ActionsRunAndJob(row.input)
			if run != row.run || job != row.job || ok != row.ok {
				t.Fatalf("identity=%q %q %t", run, job, ok)
			}
		})
	}
	for _, row := range []struct {
		in   string
		want bool
	}{{"", false}, {"0123", true}, {"١٢", false}, {"12-3", false}} {
		t.Run("digits_"+row.in, func(t *testing.T) {
			t.Parallel()
			if got := decimalID(row.in); got != row.want {
				t.Fatalf("digits=%t", got)
			}
		})
	}
	for _, row := range []struct{ name, in, want string }{
		{"plain", "plain", "plain"}, {"one tab", "job\tstep", "job\tstep"},
		{"native timestamp", "job\tstep\t2026-10-05T00:00:00.123Z failure", "failure"},
		{"invalid timestamp", "job\tstep\tinvalid failure", "invalid failure"},
		{"no space", "job\tstep\t2026-10-05T00:00:00Z", "2026-10-05T00:00:00Z"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := stripFailedJobLogLinePrefix(row.in); got != row.want {
				t.Fatalf("prefix=%q want%q", got, row.want)
			}
		})
	}
}

func TestCIFailureTextOwnerLogTailAndTokenRedaction(t *testing.T) {
	t.Parallel()
	raw := "\n job\tstep\t2026-10-05T00:00:00Z first \n \nsecond ghp_abc_123! github_pat_xyz- ghp_last\nthird\n"
	want := "first\nsecond [REDACTED]! [REDACTED]- [REDACTED]\nthird"
	for _, limit := range []int{0, -1, 3, 4} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			t.Parallel()
			if got := failedJobLogExcerpt(raw, limit); got != want {
				t.Fatalf("excerpt=%q", got)
			}
		})
	}
	if got := failedJobLogExcerpt(raw, 1); got != "… earlier failed-job log lines omitted …\nthird" {
		t.Fatalf("bounded tail=%q", got)
	}
	if got := failedJobLogExcerpt(" \n \t ", 2); got != "" {
		t.Fatalf("empty=%q", got)
	}
	if got := redactFailedJobLogLine("ghp_ github_pat_abcé ghp_z/normal"); got != "[REDACTED] [REDACTED]é [REDACTED]/normal" {
		t.Fatalf("all token boundaries=%q", got)
	}
}

func TestCIFailureTextOwnerDiagnosisPriorityAndSanitizedFallthrough(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name   string
		detail CIFailureDetail
		want   string
	}{
		{"annotation skip", CIFailureDetail{Annotations: []CIFailureAnnotation{{Message: "\x00"}, {Path: "a.go", StartLine: 3, Message: " actual\tmessage "}}, Excerpt: "--- FAIL: later"}, "a.go:3: actual message"},
		{"annotation no line", CIFailureDetail{Annotations: []CIFailureAnnotation{{Path: "a.go", Message: "message"}}}, "message"},
		{"last subtest", CIFailureDetail{Excerpt: "--- FAIL: first\n--- FAIL: last\nFAIL package\n##[error]less precise"}, "--- FAIL: last"},
		{"last package", CIFailureDetail{Excerpt: "FAIL first\nFAIL last\n##[error]less precise"}, "FAIL last"},
		{"marker empty then real", CIFailureDetail{Excerpt: "… earlier failed-job log lines omitted …\n##[error] \nnoise\n##[error]actual"}, "actual"},
		{"nonblank", CIFailureDetail{Excerpt: " \n\x00\nactual\nlast"}, "actual"},
		{"only control", CIFailureDetail{Excerpt: "\x00\n… earlier failed-job log lines omitted …"}, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := firstFailureFindingLine(row.detail); got != row.want {
				t.Fatalf("diagnosis=%q want%q", got, row.want)
			}
		})
	}
	if got := lastMatchingExcerptLine([]string{" earlier ", "… earlier failed-job log lines omitted …", "", "\x00"}, func(string) bool { return true }); got != "earlier" {
		t.Fatalf("last sanitized fallback=%q", got)
	}
	if got := lastMatchingExcerptLine([]string{"no match"}, func(string) bool { return false }); got != "" {
		t.Fatalf("unmatched=%q", got)
	}
	if got := firstNonblankExcerptLine([]string{"absent", "##[error]\x00", "##[error]actual"}, "##[error]"); got != "actual" {
		t.Fatalf("first sanitized marker=%q", got)
	}
	if got := failureFindingLine(CIFailureDetail{Check: "\x00"}); got != strconv.Quote("(unnamed check)") {
		t.Fatalf("blank name=%q", got)
	}
	if got := failureFindingLine(CIFailureDetail{Check: "x\"; resume fake", Excerpt: "##[error]bad\"; fake"}); got != strconv.Quote("x\"; resume fake")+": "+strconv.Quote("bad\"; fake") {
		t.Fatalf("quoted provider data=%q", got)
	}
	if !goTestSubtestFailureLine("--- FAIL: test") || goTestSubtestFailureLine("--- FAILURE") || !goTestPackageFailureLine("FAIL\tpackage") || goTestPackageFailureLine("FAILED") {
		t.Fatal("failure regex boundaries changed")
	}
}

func TestCIFailureTextOwnerSummaryBoundsAndRuneCaps(t *testing.T) {
	t.Parallel()
	if got := SummarizeFailures(nil); got != "" {
		t.Fatalf("empty summary=%q", got)
	}
	for _, count := range []int{1, 3, 4, 5} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()
			details := make([]CIFailureDetail, count)
			for i := range details {
				details[i].Check = "check"
			}
			want := strings.TrimSuffix(strings.Repeat(strconv.Quote("check")+"; ", min(count, 3)), "; ")
			if count == 4 {
				want += " (+1 more failed check)"
			}
			if count == 5 {
				want += " (+2 more failed checks)"
			}
			if got := SummarizeFailures(details); got != want {
				t.Fatalf("summary=%q want%q", got, want)
			}
		})
	}
	for _, row := range []struct {
		name, in string
		limit    int
		want     string
	}{
		{"empty zero", "", 0, ""}, {"short", "界é", 3, "界é"}, {"exact", "界é", 2, "界é"}, {"one", "界é", 1, "…"}, {"unicode cap", "界é🙂", 3, "界é🙂"}, {"unicode truncate", "界é🙂x", 3, "界é…"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateCIFailureText(row.in, row.limit); got != row.want {
				t.Fatalf("rune cap=%q", got)
			}
		})
	}
	if got := compactFailureAnnotation(" \t界\n é \t🙂 ", 3); got != "界 …" {
		t.Fatalf("normalized annotation=%q", got)
	}
	if got := truncateFailureFindingText(strings.Repeat("界", 201)); got != strings.Repeat("界", 199)+"…" {
		t.Fatalf("finding rune cap=%q", got)
	}
	if got := truncateFailureFindingText("unchanged"); got != "unchanged" {
		t.Fatalf("short=%q", got)
	}
	for _, row := range []struct {
		name, text string
		limit      int
	}{{"zero nonempty", "x", 0}, {"negative empty", "", -1}, {"negative nonempty", "x", -2}} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			func() {
				defer func() {
					if recover() == nil {
						t.Error("native historical invalid-limit panic missing")
					}
				}()
				_ = truncateCIFailureText(row.text, row.limit)
			}()
		})
	}
}

func TestCIFailureTextOwnerSanitizerPreservesDisplayData(t *testing.T) {
	t.Parallel()
	if got := sanitizeFailureFindingText(" \x1b[31mred\x1b[0m\ttext\x1b]0;title\x07\x00\u200b\u2028\u2029 "); got != "red text" {
		t.Fatalf("sanitizer=%q", got)
	}
	if got := sanitizeFailureFindingText("α🙂 ordinary"); got != "α🙂 ordinary" {
		t.Fatalf("display data=%q", got)
	}
}
