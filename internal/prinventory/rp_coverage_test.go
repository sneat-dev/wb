package prinventory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpCovRunner is a deterministic Runner that records the exact argv of every
// call and answers through fn, so tests can assert the commands inventory
// issues as well as the values it derives from their output. Inventory calls
// Run from a worker pool, so recorded calls need their own mutex: production
// concurrency is real, not a test artefact to paper over.
type rpCovRunner struct {
	mu    sync.Mutex
	calls [][]string
	fn    func(args []string) ([]byte, error)
}

func (r *rpCovRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	return r.fn(args)
}

func rpCovSearchPage(total int, items ...string) string {
	return `{"total_count":` + strconv.Itoa(total) + `,"items":[` + strings.Join(items, ",") + `]}`
}

func rpCovSearchItem(id, number, repository string, extra ...string) string {
	parts := []string{
		`"id":` + id,
		`"number":` + number,
		`"title":"Title ` + number + `"`,
		`"html_url":"https://github.com/` + repository + `/pull/` + number + `"`,
		`"repository_url":"https://api.github.com/repos/` + repository + `"`,
		`"user":{"login":"author"}`,
		`"created_at":"2026-01-01T00:00:00Z"`,
		`"updated_at":"2026-01-02T00:00:00Z"`,
		`"draft":false`,
	}
	parts = append(parts, extra...)
	return "{" + strings.Join(parts, ",") + "}"
}

func TestRPCovParsePagesAcceptsBothProviderShapes(t *testing.T) {
	t.Parallel()
	array := `[` + rpCovSearchPage(1, rpCovSearchItem(`"1"`, "1", "acme/app")) + `,` +
		rpCovSearchPage(2, rpCovSearchItem(`"2"`, "2", "acme/app")) + `]`
	pages, err := parsePages([]byte(array))
	if err != nil || len(pages) != 2 || pages[0].TotalCount != 1 || pages[1].TotalCount != 2 {
		t.Fatalf("array pages = %+v, err=%v", pages, err)
	}

	// What plain --paginate emits: concatenated page objects, one per page.
	stream := rpCovSearchPage(1, rpCovSearchItem(`"1"`, "1", "acme/app")) +
		rpCovSearchPage(1, rpCovSearchItem(`"2"`, "2", "acme/app"))
	pages, err = parsePages([]byte(stream))
	if err != nil || len(pages) != 2 || len(pages[1].Items) != 1 || pages[1].Items[0].Number != 2 {
		t.Fatalf("streamed pages = %+v, err=%v", pages, err)
	}

	// A single object is still a one-page stream.
	pages, err = parsePages([]byte(`{"total_count":0,"items":[]}`))
	if err != nil || len(pages) != 1 || pages[0].TotalCount != 0 {
		t.Fatalf("single-object stream = %+v, err=%v", pages, err)
	}
}

func TestRPCovParsePagesRejectsUndecodableResponses(t *testing.T) {
	t.Parallel()
	if _, err := parsePages(nil); err == nil || !strings.Contains(err.Error(), "no decodable page") {
		t.Fatalf("empty response error = %v, want no-decodable-page refusal", err)
	}
	if _, err := parsePages([]byte(`[1,2]`)); err == nil {
		t.Fatal("a JSON array of numbers decoded as a search page")
	}
	if _, err := parsePages([]byte(`{"total_count":`)); err == nil {
		t.Fatal("truncated JSON decoded as a search page")
	}
}

func TestRPCovInventoryAppliesProviderDetailsToEachPullRequest(t *testing.T) {
	t.Parallel()
	details := `{"number":7,"title":"Detailed","url":"https://github.com/acme/app/pull/7",` +
		`"author":{"login":"detail-author"},"isDraft":true,` +
		`"createdAt":"2026-02-01T00:00:00Z","updatedAt":"2026-02-02T00:00:00Z",` +
		`"mergeable":"CONFLICTING","mergeStateStatus":"dirty",` +
		`"statusCheckRollup":[{"name":"build","status":"COMPLETED","conclusion":"FAILURE"},` +
		`{"name":"lint","status":"COMPLETED","conclusion":"SUCCESS"}]}`
	runner := &rpCovRunner{fn: func(args []string) ([]byte, error) {
		if args[0] == "pr" {
			return []byte(details), nil
		}
		return []byte(`[` + rpCovSearchPage(1, rpCovSearchItem(`"1"`, "3", "acme/app",
			`"pull_request":{"url":"https://api.github.com/repos/acme/app/pulls/3"}`)) + `]`), nil
	}}
	result := Inventory(context.Background(), Options{
		Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner,
		Now: func() time.Time { return time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC) },
	})
	if !result.Complete || len(result.PullRequests) != 1 {
		t.Fatalf("report = %+v, want one complete PR", result)
	}
	pr := result.PullRequests[0]
	if pr.Number != 7 || pr.Title != "Detailed" || pr.Author != "detail-author" || !pr.Draft || pr.Repository != "acme/app" {
		t.Fatalf("PR identity fields = %+v", pr)
	}
	if pr.URL != "https://github.com/acme/app/pull/7" || !pr.CreatedAt.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) ||
		!pr.UpdatedAt.Equal(time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("PR timestamps/url = %+v", pr)
	}
	if !pr.Conflict || pr.Mergeable != "CONFLICTING" || pr.MergeStateStatus != "dirty" {
		t.Fatalf("conflict evidence = %+v", pr)
	}
	if len(pr.Checks) != 2 || pr.Checks[0].Name != "build" || pr.Checks[1].Name != "lint" {
		t.Fatalf("checks = %+v, want name-sorted rollup", pr.Checks)
	}
	if len(runner.calls) != 2 || runner.calls[1][0] != "pr" || runner.calls[1][1] != "view" {
		t.Fatalf("commands = %v, want one search and one detail view", runner.calls)
	}
}

func TestRPCovApplyDetailsKeepsExistingValuesForEmptyDetailFields(t *testing.T) {
	t.Parallel()
	pr := PullRequest{
		Number: 3, Title: "original", URL: "https://github.com/acme/app/pull/3", Author: "original-author",
		Draft: true, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	applyDetails(&pr, rawDetails{})
	if pr.Number != 3 || pr.Title != "original" || pr.URL != "https://github.com/acme/app/pull/3" || pr.Author != "original-author" {
		t.Fatalf("empty detail overwrote identity: %+v", pr)
	}
	if !pr.CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !pr.UpdatedAt.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("empty detail overwrote timestamps: %+v", pr)
	}
	if pr.Draft || pr.Conflict || len(pr.Checks) != 0 || pr.Mergeable != "" || pr.MergeStateStatus != "" {
		t.Fatalf("empty detail did not clear draft/checks/conflict: %+v", pr)
	}
}

func TestRPCovApplyDetailsMarksDirtyMergeStateAsConflict(t *testing.T) {
	t.Parallel()
	pr := PullRequest{}
	applyDetails(&pr, rawDetails{MergeStateStatus: "DIRTY"})
	if !pr.Conflict {
		t.Fatalf("DIRTY merge state must read as a conflict: %+v", pr)
	}
}

func TestRPCovDetailsPropagatesCommandAndDecodeFailures(t *testing.T) {
	t.Parallel()
	failing := &rpCovRunner{fn: func([]string) ([]byte, error) { return nil, errors.New("gh offline") }}
	if _, err := details(context.Background(), failing, "acme/app", 1); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("details command error = %v", err)
	}
	garbled := &rpCovRunner{fn: func([]string) ([]byte, error) { return []byte("{"), nil }}
	if _, err := details(context.Background(), garbled, "acme/app", 1); err == nil {
		t.Fatal("details accepted malformed JSON")
	}
	good := &rpCovRunner{fn: func([]string) ([]byte, error) { return []byte(`{"number":9}`), nil }}
	detail, err := details(context.Background(), good, "acme/app", 9)
	if err != nil || detail.Number != 9 {
		t.Fatalf("details = %+v, err=%v", detail, err)
	}
}

func TestRPCovRepositorySlugRejectsUnparseableAndUnrelatedURLs(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"https://api.github.com/repos/acme/app": "acme/app",
		"http://[::1":                           "",
		"https://api.github.com/user/1":         "",
		"":                                      "",
	} {
		if got := repositorySlug(raw); got != want {
			t.Errorf("repositorySlug(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRPCovInventoryOwnerReportsCutoffParseFailureWithoutQuerying(t *testing.T) {
	t.Parallel()
	runner := &rpCovRunner{fn: func([]string) ([]byte, error) {
		t.Fatal("an unparseable cutoff must not reach the provider")
		return nil, nil
	}}
	result := Inventory(context.Background(), Options{
		Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner, CreatedBefore: "yesterday",
	})
	if result.Complete || result.Counts.OwnersFailed != 1 {
		t.Fatalf("report = %+v, want one failed owner", result)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "invalid created-before cutoff") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %v, want none", runner.calls)
	}
}

func TestRPCovInventoryOwnerReportsUndecodableProviderResponse(t *testing.T) {
	t.Parallel()
	runner := &rpCovRunner{fn: func([]string) ([]byte, error) { return []byte("not json"), nil }}
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner})
	if result.Complete {
		t.Fatal("undecodable provider response reported complete")
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "response invalid") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestRPCovInventoryOwnerReportsIncompletePagination(t *testing.T) {
	t.Parallel()
	runner := &rpCovRunner{fn: func([]string) ([]byte, error) {
		return []byte(`[` + rpCovSearchPage(5, rpCovSearchItem(`"1"`, "1", "acme/app")) + `]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner})
	if result.Complete {
		t.Fatal("a truncated page set reported complete")
	}
	if len(result.PullRequests) != 1 {
		t.Fatalf("PRs = %+v, want the single retrieved PR retained", result.PullRequests)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "incomplete GitHub pagination: retrieved 1 of 5") {
		t.Fatalf("diagnostics = %+v, want the exact shortfall", result.Diagnostics)
	}
}

func TestRPCovInventoryOwnerRejectsPRWithoutRepositoryIdentity(t *testing.T) {
	t.Parallel()
	runner := &rpCovRunner{fn: func([]string) ([]byte, error) {
		return []byte(`[{"total_count":1,"items":[{"id":"1","number":4,"title":"nowhere",` +
			`"html_url":"https://github.com/x/y/pull/4","repository_url":"https://api.github.com/user/4",` +
			`"user":{"login":"a"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]}]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner})
	if len(result.PullRequests) != 0 {
		t.Fatalf("PRs = %+v, want none without a repository identity", result.PullRequests)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "has no repository identity") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestRPCovInventoryOwnerRejectsDuplicatePRIdentityAcrossPages(t *testing.T) {
	t.Parallel()
	item := rpCovSearchItem(`"1"`, "5", "acme/app")
	runner := &rpCovRunner{fn: func([]string) ([]byte, error) {
		return []byte(`[` + rpCovSearchPage(2, item, item) + `]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner})
	if len(result.PullRequests) != 1 {
		t.Fatalf("PRs = %+v, want one deduplicated row", result.PullRequests)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "duplicate PR identity across provider pages: acme/app#5") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestRPCovNormalizeOwnersDefaultsQualifierTrimmsAndDeduplicates(t *testing.T) {
	t.Parallel()
	owners := normalizeOwners([]Owner{
		{Login: "  acme  "},
		{Login: "acme", Qualifier: "org"},
		{Login: "", Qualifier: "org"},
		{Login: "team", Qualifier: "team"},
		{Login: "someone", Qualifier: "user"},
	})
	if len(owners) != 2 {
		t.Fatalf("owners = %+v, want the defaulted org and the user qualifier", owners)
	}
	if owners[0].Login != "acme" || owners[0].Qualifier != "org" {
		t.Fatalf("first owner = %+v, want acme organziation with the default qualifier", owners[0])
	}
	if owners[1].Login != "someone" || owners[1].Qualifier != "user" {
		t.Fatalf("second owner = %+v", owners[1])
	}
}

func TestRPCovInventoryDeduplicatesSortsAndOrdersDiagnostics(t *testing.T) {
	t.Parallel()
	duplicate := rpCovSearchItem(`"4"`, "4", "acme/zebra")
	runner := &rpCovRunner{fn: func(args []string) ([]byte, error) {
		query := strings.Join(args, " ")
		switch {
		case strings.Contains(query, "org-broken"):
			return []byte("broken"), nil
		case strings.Contains(query, "org-c"):
			noIdentity := `{"id":"5","number":5,"title":"nowhere","html_url":"https://github.com/x/y/pull/5",` +
				`"repository_url":"https://api.github.com/user/5","user":{"login":"a"},` +
				`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
			return []byte(`[` + rpCovSearchPage(10, noIdentity, duplicate, duplicate) + `]`), nil
		}
		items := []string{
			rpCovSearchItem(`"1"`, "9", "acme/zebra"),
			rpCovSearchItem(`"2"`, "2", "acme/zebra"),
			rpCovSearchItem(`"3"`, "1", "acme/alpha"),
		}
		return []byte(`[` + rpCovSearchPage(len(items), items...) + `]`), nil
	}}
	result := Inventory(context.Background(), Options{
		Owners: []Owner{
			{Login: "org-broken", Qualifier: "org"},
			{Login: "org-c", Qualifier: "org"},
			{Login: "org-a", Qualifier: "org"},
		},
		Runner: runner, Parallel: 2,
	})
	if len(result.PullRequests) != 4 {
		t.Fatalf("PRs = %+v, want four rows", result.PullRequests)
	}
	got := []string{}
	for _, pr := range result.PullRequests {
		got = append(got, pr.Repository+"#"+strconv.Itoa(pr.Number))
	}
	want := []string{"acme/alpha#1", "acme/zebra#2", "acme/zebra#4", "acme/zebra#9"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("PR order = %v, want %v", got, want)
	}
	if result.Counts.OwnersCompleted != 1 || result.Counts.OwnersFailed != 2 || result.Complete {
		t.Fatalf("counts = %+v complete=%t", result.Counts, result.Complete)
	}
	if len(result.Diagnostics) != 4 {
		t.Fatalf("diagnostics = %+v, want one owner failure plus three from org-c", result.Diagnostics)
	}
	sameOwnerMessages := []string{}
	for index := 1; index < len(result.Diagnostics); index++ {
		prev, next := result.Diagnostics[index-1], result.Diagnostics[index]
		if prev.Owner > next.Owner || (prev.Owner == next.Owner && prev.Message > next.Message) {
			t.Fatalf("diagnostics are not sorted by owner then message: %+v", result.Diagnostics)
		}
		if prev.Owner == next.Owner {
			sameOwnerMessages = append(sameOwnerMessages, next.Message)
		}
	}
	if len(sameOwnerMessages) < 2 {
		t.Fatalf("diagnostics do not exercise the same-owner comparator branch: %+v", result.Diagnostics)
	}
}

func TestRPCovRenderMarkdownExcludesArchivedAndRendersCutoffAndDiagnostics(t *testing.T) {
	t.Parallel()
	report := Report{
		SnapshotAt:       time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC),
		Complete:         false,
		Owners:           []Owner{{Login: "acme", Qualifier: "org"}, {Login: "other", Qualifier: "user"}},
		EffectiveFilters: Filters{State: "open", IncludeArchived: false, CreatedBefore: "2026-08-11T00:00:00Z"},
		Counts:           Counts{OwnersRequested: 2, OwnersCompleted: 1, OwnersFailed: 1, PullRequests: 1},
		PullRequests: []PullRequest{{
			Repository: "acme/app", Number: 1, Title: "a|b\nc", URL: "https://github.com/acme/app/pull/1",
			Author: "a", Mergeable: "UNKNOWN", MergeStateStatus: "clean",
			Checks: []Check{{Name: "ci", Conclusion: "SUCCESS"}},
		}},
		Diagnostics: []Diagnostic{{Owner: "other", Severity: "error", Message: "rate limited"}},
	}
	md := RenderMarkdown(report)
	for _, want := range []string{
		"Archived repositories: `excluded`",
		"Created before: `2026-08-11T00:00:00Z`",
		"Complete: `false`",
		"Owners: `acme, other`",
		"a\\|b c",
		"ci:SUCCESS",
		"## Diagnostics",
		"- `error` other: rate limited",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRPCovIdStringHandlesEmptyStringNumericAndInvalidValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"absent", nil, ""},
		{"string", json.RawMessage(`"abc"`), "abc"},
		{"number", json.RawMessage(`123`), "123"},
		{"invalid", json.RawMessage(`{`), ""},
	} {
		if got := idString(test.raw); got != test.want {
			t.Errorf("idString(%s) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestRPCovOwnerStringAndInventoryOwnerQueryIncludesQualifier(t *testing.T) {
	t.Parallel()
	if got := (Owner{Login: "acme", Qualifier: "org"}).String(); got != "org:acme" {
		t.Fatalf("Owner.String() = %q", got)
	}
	runner := &rpCovRunner{fn: func([]string) ([]byte, error) { return []byte(`[{"total_count":0,"items":[]}]`), nil }}
	Inventory(context.Background(), Options{
		Owners: []Owner{{Login: "acme", Qualifier: "user"}}, Runner: runner, ExcludeArchived: true,
	})
	if len(runner.calls) != 1 || !strings.Contains(strings.Join(runner.calls[0], " "), "user:acme") {
		t.Fatalf("query argv = %v, want the user qualifier", runner.calls)
	}
}

// rpCovInstallFakeGh writes an executable `gh` shim onto PATH whose stdout is
// payload, so the real execRunner path can be exercised without network or
// credentials.
func rpCovInstallFakeGh(t *testing.T, payload string) {
	t.Helper()
	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -eu\ncat " + rpCovShellQuote(payloadPath) + "\n"
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func rpCovShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func TestRPCovExecRunnerReadsThroughTheGhBoundaryAndInventoryDefaultsToIt(t *testing.T) {
	payload := `[` + rpCovSearchPage(1, rpCovSearchItem(`"1"`, "1", "acme/app")) + `]`
	rpCovInstallFakeGh(t, payload)

	out, err := execRunner{}.Run(context.Background(), "api", "--method", "GET", "/search/issues")
	if err != nil {
		t.Fatalf("execRunner.Run: %v", err)
	}
	if strings.TrimSpace(string(out)) != payload {
		t.Fatalf("execRunner.Run output = %q", out)
	}

	// Inventory with no Runner uses the same boundary, so the shim answers it.
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}})
	if !result.Complete || len(result.PullRequests) != 1 {
		t.Fatalf("default-runner inventory = %+v", result)
	}
}

// rpCovFailGh installs a gh shim that exits non-zero so the real execRunner
// error path is observable.
func rpCovFailGh(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'gh: not logged in' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRPCovInventoryWithDefaultRunnerReportsProviderFailure(t *testing.T) {
	rpCovFailGh(t)
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}})
	if result.Complete || result.Counts.OwnersFailed != 1 {
		t.Fatalf("report = %+v, want one failed owner", result)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "GitHub owner query failed") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}
