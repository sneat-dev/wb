package syncreport

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// tailCovRequireUnixFilesystem skips the tests below whose premise is real
// Unix filesystem semantics: permission bits that actually deny access,
// TMPDIR resolution, and unprivileged symlink creation. Windows and a root
// user both defeat those premises, so those paths cannot be exercised there;
// every other test in this package runs unguarded on every platform.
func tailCovRequireUnixFilesystem(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("path is not reachable on Windows: chmod/TMPDIR/symlink semantics differ")
	}
	if os.Geteuid() == 0 {
		t.Skip("path is not reachable as root: filesystem permission bits are not enforced")
	}
}

// tailCovReplaceRecord returns validRecord with the first occurrence of old
// replaced, failing loudly if the fixture no longer contains it.
func tailCovReplaceRecord(t *testing.T, old, replacement string) string {
	t.Helper()
	if !strings.Contains(validRecord, old) {
		t.Fatalf("validRecord does not contain %q", old)
	}
	return strings.Replace(validRecord, old, replacement, 1)
}

// TestTailCovParseRejectsEveryInvalidFrontmatterField walks each rule Validate
// enforces, proving the record that violates exactly that rule is refused with
// a message naming the field.
func TestTailCovParseRejectsEveryInvalidFrontmatterField(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name:    "no opening delimiter",
			raw:     "schema_version: 1\n",
			wantErr: "must start with YAML frontmatter delimiter",
		},
		{
			name:    "no closing delimiter",
			raw:     "---\nschema_version: 1\n",
			wantErr: "must end with delimiter",
		},
		{
			name:    "unsupported schema version",
			raw:     tailCovReplaceRecord(t, "schema_version: 1", "schema_version: 2"),
			wantErr: "schema_version must be 1",
		},
		{
			name:    "report id with a leading dash",
			raw:     tailCovReplaceRecord(t, "report_id: sync-20260908T145950Z", "report_id: -not-an-id"),
			wantErr: "report_id must start with a letter or digit",
		},
		{
			name:    "repository without an owner",
			raw:     tailCovReplaceRecord(t, "repository: sneat-co/schoolus", `repository: "schoolus"`),
			wantErr: "must be owner/name",
		},
		{
			name:    "repository with two slashes",
			raw:     tailCovReplaceRecord(t, "repository: sneat-co/schoolus", `repository: "sneat-co/school/us"`),
			wantErr: "must be owner/name",
		},
		{
			name:    "empty repository",
			raw:     tailCovReplaceRecord(t, "repository: sneat-co/schoolus", `repository: ""`),
			wantErr: "must be owner/name",
		},
		{
			name:    "finding is not a lowercase slug",
			raw:     tailCovReplaceRecord(t, "finding: unpushed_commits", "finding: Unpushed Commits"),
			wantErr: "finding must be a lowercase slug",
		},
		{
			name:    "unsupported severity",
			raw:     tailCovReplaceRecord(t, "severity: attention", "severity: warning"),
			wantErr: "severity must be attention, error, or info",
		},
		{
			name:    "unsupported state",
			raw:     tailCovReplaceRecord(t, "state: open", "state: closed"),
			wantErr: "state must be open or resolved",
		},
		{
			name:    "observed_at is not RFC3339",
			raw:     tailCovReplaceRecord(t, `observed_at: "2026-09-08T14:59:50Z"`, "observed_at: yesterday"),
			wantErr: "observed_at must be RFC3339",
		},
		{
			name:    "head_sha is not forty lowercase hex characters",
			raw:     tailCovReplaceRecord(t, "head_sha: 4afefc42745fe329f2ca4a32388a337096b55cdb", "head_sha: "+strings.ToUpper("4afefc42745fe329f2ca4a32388a337096b55cdb")),
			wantErr: "head_sha must be an empty value or a 40-character lowercase Git object ID",
		},
		{
			name:    "blank title",
			raw:     tailCovReplaceRecord(t, "title: Six unpushed commits", `title: "   "`),
			wantErr: "title is required",
		},
		{
			name:    "blank suggested_action",
			raw:     tailCovReplaceRecord(t, "suggested_action: Finish active worktrees before cleanup.", `suggested_action: ""`),
			wantErr: "suggested_action is required",
		},
		{
			name:    "blank markdown body",
			raw:     strings.Split(validRecord, "## Analysis")[0],
			wantErr: "markdown analysis body is required",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := Parse([]byte(testCase.raw)); err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Parse error = %v, want a message containing %q", err, testCase.wantErr)
			}
		})
	}
}

// TestTailCovParsePreservesBodyAndAcceptsAnOmittedHeadSHA pins the positive
// side of the same rules: head_sha is optional and the Markdown body is kept
// verbatim.
func TestTailCovParsePreservesBodyAndAcceptsAnOmittedHeadSHA(t *testing.T) {
	withoutSHA := strings.Replace(validRecord, "head_sha: 4afefc42745fe329f2ca4a32388a337096b55cdb\n", "", 1)
	record, err := Parse([]byte(withoutSHA))
	if err != nil {
		t.Fatalf("Parse without head_sha = %v", err)
	}
	if record.HeadSHA != "" {
		t.Fatalf("HeadSHA = %q, want empty", record.HeadSHA)
	}
	if record.Body != "## Analysis\n\nThe commits belong to active work and must be preserved.\n" {
		t.Fatalf("Body = %q, want the Markdown body preserved verbatim", record.Body)
	}
	if record.Repository != "sneat-co/schoolus" || record.Finding != "unpushed_commits" {
		t.Fatalf("record = %+v, want the frontmatter fields decoded", record)
	}
}

// TestTailCovValidateRepositoryChecksOwnerAndName covers the standalone
// repository validator, including names that only fail on one side of the
// slash.
func TestTailCovValidateRepositoryChecksOwnerAndName(t *testing.T) {
	bad := []string{
		"",
		"schoolus",
		"/name",
		"owner/",
		"a/b/c",
		"-owner/name",
		"owner/-name",
		"owner/na me",
		"owner/name/",
	}
	for _, repository := range bad {
		if err := ValidateRepository(repository); err == nil {
			t.Errorf("ValidateRepository(%q) = nil, want an owner/name error", repository)
		}
	}
	good := []string{"sneat-co/schoolus", "a/b", "A.b_c-d/e"}
	for _, repository := range good {
		if err := ValidateRepository(repository); err != nil {
			t.Errorf("ValidateRepository(%q) = %v, want nil", repository, err)
		}
	}
}
