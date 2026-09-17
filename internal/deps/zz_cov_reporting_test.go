package deps

// Behaviour tests for the reporting and parsing surface of internal/deps:
// bump_report.go, report.go, peers_report.go, graph_report.go, graph_svg.go,
// and types.go. Everything here asserts exact observable output — rendered
// Markdown rows, JSON/YAML field names and values, the files a write helper
// creates, and the exact refusal messages — rather than merely executing
// lines.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/quality"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func depsCovReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

func depsCovDirNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read directory %s: %v", directory, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func depsCovWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func depsCovMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func depsCovJSONDocument(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("JSON report must end with a newline: %q", raw)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("report is not valid JSON: %v\n%s", err, raw)
	}
	return document
}

func depsCovJSONObject(t *testing.T, document map[string]any, key string) map[string]any {
	t.Helper()
	nested, ok := document[key].(map[string]any)
	if !ok {
		t.Fatalf("JSON key %q is %T, want an object", key, document[key])
	}
	return nested
}

func depsCovJSONArray(t *testing.T, document map[string]any, key string) []any {
	t.Helper()
	array, ok := document[key].([]any)
	if !ok {
		t.Fatalf("JSON key %q is %T, want an array", key, document[key])
	}
	return array
}

func depsCovMustContain(t *testing.T, rendered, expected string) {
	t.Helper()
	if !strings.Contains(rendered, expected) {
		t.Fatalf("rendered output does not contain %q:\n%s", expected, rendered)
	}
}

func depsCovMustNotContain(t *testing.T, rendered, unexpected string) {
	t.Helper()
	if strings.Contains(rendered, unexpected) {
		t.Fatalf("rendered output unexpectedly contains %q:\n%s", unexpected, rendered)
	}
}

// ---------------------------------------------------------------------------
// types.go
// ---------------------------------------------------------------------------

func TestDepsCovReportingParseValidationMode(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		value string
		want  ValidationMode
	}{
		{value: "", want: ValidationModeFull},
		{value: "   ", want: ValidationModeFull},
		{value: "full", want: ValidationModeFull},
		{value: "  full  ", want: ValidationModeFull},
		{value: "fast", want: ValidationModeFast},
		{value: "\tfast\n", want: ValidationModeFast},
	} {
		mode, err := ParseValidationMode(fixture.value)
		if err != nil {
			t.Fatalf("ParseValidationMode(%q): %v", fixture.value, err)
		}
		if mode != fixture.want {
			t.Fatalf("ParseValidationMode(%q) = %q, want %q", fixture.value, mode, fixture.want)
		}
	}
	for _, fixture := range []struct {
		value string
		want  string
	}{
		{value: "none", want: `unknown validation mode "none" (want full or fast)`},
		{value: "  none  ", want: `unknown validation mode "  none  " (want full or fast)`},
		{value: "Full", want: `unknown validation mode "Full" (want full or fast)`},
	} {
		mode, err := ParseValidationMode(fixture.value)
		if err == nil {
			t.Fatalf("ParseValidationMode(%q) accepted an unknown mode as %q", fixture.value, mode)
		}
		if err.Error() != fixture.want {
			t.Fatalf("ParseValidationMode(%q) error = %q, want %q", fixture.value, err.Error(), fixture.want)
		}
		if mode != "" {
			t.Fatalf("ParseValidationMode(%q) returned %q alongside an error", fixture.value, mode)
		}
	}
}

func TestDepsCovReportingParseTarget(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		ecosystem string
		value     string
		want      Target
	}{
		{
			ecosystem: "github-actions", value: " acme/cicd@v1.2.3 ",
			want: Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.2.3"},
		},
		{
			ecosystem: " go ", value: "example.com/mod@v1.2.3",
			want: Target{Ecosystem: EcosystemGo, Dependency: "example.com/mod", Version: "v1.2.3"},
		},
		{
			ecosystem: "npm", value: "@acme/widget@1.2.3",
			want: Target{Ecosystem: EcosystemNPM, Dependency: "@acme/widget", Version: "1.2.3"},
		},
		{
			ecosystem: "go", value: " example.com/mod@v0.0.0-20260101000000-abcdefabcdef ",
			want: Target{Ecosystem: EcosystemGo, Dependency: "example.com/mod", Version: "v0.0.0-20260101000000-abcdefabcdef"},
		},
	} {
		target, err := ParseTarget(fixture.ecosystem, fixture.value)
		if err != nil {
			t.Fatalf("ParseTarget(%q, %q): %v", fixture.ecosystem, fixture.value, err)
		}
		if target != fixture.want {
			t.Fatalf("ParseTarget(%q, %q) = %+v, want %+v", fixture.ecosystem, fixture.value, target, fixture.want)
		}
	}

	for _, fixture := range []struct {
		ecosystem string
		value     string
		want      string
	}{
		{
			ecosystem: "bower", value: "acme/cicd@v1.0.0",
			want: `unsupported dependency ecosystem "bower" (want github-actions, go, or npm)`,
		},
		{
			ecosystem: "go", value: "example.com/mod",
			want: `invalid dependency target "example.com/mod" (want fully-qualified-dependency@version)`,
		},
		{
			ecosystem: "go", value: "example.com/mod@",
			want: `invalid dependency target "example.com/mod@" (want fully-qualified-dependency@version)`,
		},
		{
			ecosystem: "go", value: "@v1.2.3",
			want: `invalid dependency target "@v1.2.3" (want fully-qualified-dependency@version)`,
		},
		{
			ecosystem: "go", value: " @v1.2.3",
			want: `invalid dependency target " @v1.2.3" (want fully-qualified-dependency@version)`,
		},
		{
			ecosystem: "go", value: "example.com/mod@ ",
			want: `invalid dependency target "example.com/mod@ " (want fully-qualified-dependency@version)`,
		},
		{
			ecosystem: "github-actions", value: "acmecicd@v1.2.3",
			want: `GitHub Actions dependency "acmecicd" must be a full owner/repository identity`,
		},
		{
			ecosystem: "github-actions", value: "acme/cicd/extra@v1.2.3",
			want: `GitHub Actions dependency "acme/cicd/extra" must be a full owner/repository identity`,
		},
		{
			ecosystem: "github-actions", value: "acme /cicd@v1.2.3",
			want: `GitHub Actions dependency "acme /cicd" must be a full owner/repository identity`,
		},
	} {
		target, err := ParseTarget(fixture.ecosystem, fixture.value)
		if err == nil {
			t.Fatalf("ParseTarget(%q, %q) accepted %+v", fixture.ecosystem, fixture.value, target)
		}
		if err.Error() != fixture.want {
			t.Fatalf("ParseTarget(%q, %q) error = %q, want %q", fixture.ecosystem, fixture.value, err.Error(), fixture.want)
		}
		if target != (Target{}) {
			t.Fatalf("ParseTarget(%q, %q) returned %+v alongside an error", fixture.ecosystem, fixture.value, target)
		}
	}
}

// sortRepositoryReport is the deterministic ordering every report writer
// depends on: changed files are sorted, and decisions sort by file, then
// dependency, then observed before-ref.
func TestDepsCovReportingSortRepositoryReport(t *testing.T) {
	t.Parallel()
	report := RepositoryReport{
		ChangedFiles: []string{"go.sum", "go.mod", "README.md"},
		Decisions: []Decision{
			{File: "go.mod", Dependency: "example.com/z", BeforeRef: "v0.2.0"},
			{File: "go.mod", Dependency: "example.com/a", BeforeRef: "v0.1.0"},
			{File: "package.json", Dependency: "nx", BeforeRef: "22.0.0"},
			{File: "go.mod", Dependency: "example.com/a", BeforeRef: "v0.0.1"},
		},
	}
	sortRepositoryReport(&report)
	if !reflect.DeepEqual(report.ChangedFiles, []string{"README.md", "go.mod", "go.sum"}) {
		t.Fatalf("changed files = %v", report.ChangedFiles)
	}
	want := []string{"go.mod|example.com/a|v0.0.1", "go.mod|example.com/a|v0.1.0", "go.mod|example.com/z|v0.2.0", "package.json|nx|22.0.0"}
	got := make([]string, 0, len(report.Decisions))
	for _, decision := range report.Decisions {
		got = append(got, decision.File+"|"+decision.Dependency+"|"+decision.BeforeRef)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decisions = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// report.go
// ---------------------------------------------------------------------------

func depsCovReportingReport(worktree string) Report {
	resolved := strings.Repeat("a", 40)
	commit := strings.Repeat("b", 40)
	return Report{
		SchemaVersion:  1,
		Operation:      "deps-set-npm-acme-widget-1-2-3",
		Status:         "completed",
		Target:         Target{Ecosystem: EcosystemNPM, Dependency: "@acme/widget", Version: "1.2.3", Resolved: resolved},
		GitHubDir:      "/github",
		BaseRef:        "main",
		ValidationMode: ValidationModeFull,
		Parallel:       3,
		Order: &OrderReport{
			Selection: "0-2",
			Layers: []OrderLayerReport{
				{Index: 0, Repositories: []string{"acme/provider"}, Status: "completed"},
				{Index: 1, Repositories: []string{"acme/app"}, Status: "failed"},
				{Index: 2, Repositories: []string{}, Status: "not_selected"},
			},
			Cycles: []GraphOrderCycle{{Layer: 1, Repositories: []string{"acme/app", "acme/consumer"}, Path: "acme/app -> acme/consumer -> acme/app"}},
		},
		Repositories: []RepositoryReport{
			{
				Repository:   "acme/app",
				CanonicalDir: filepath.Join(worktree, "canonical"),
				WorktreeDir:  worktree,
				Branch:       "wb/deps/set",
				Ref:          "main",
				Status:       "completed",
				Reason:       "all | checks passed\nand merged",
				Decisions: []Decision{{
					Dependency: "nx", Ecosystem: EcosystemNPM, File: ".github/workflows/ci.yml", Selector: "dependencies.nx",
					BeforeRef: "main", TargetVersion: "1.2.3", ResolvedRef: resolved, AfterRef: resolved,
					AfterVersion: "1.2.3", Action: "updated", Reason: "exact target applied",
				}},
				DependencyDeltas: []DependencyDelta{{
					SourcePR: "https://github.com/acme/app/pull/7", SourceHead: commit, Consumer: "acme/app",
					Ecosystem: EcosystemNPM, Package: "nx", Manifest: "package.json", Selector: "dependencies.nx",
					Before: "1.2.2", RequestedAfter: "1.2.3", CandidateAfter: "1.2.3",
					Lockfile: "package-lock.json", LockfileSelector: "packages|node_modules/nx|version",
					LockfileVersion: "1.2.3", Reviewed: true,
				}},
				ChangedFiles: []string{".github/workflows/ci.yml"},
				Verifications: []quality.VerificationEntry{{
					Language: "go", Check: quality.CheckTest, Command: "go test ./...",
					Status: quality.StatusPassed, Detail: "flaky on first attempt", Attempts: 2,
				}},
				Commit: commit, Pushed: true, PR: "https://github.com/acme/app/pull/7",
				Checks: []RemoteCheck{
					{Name: "CI", Bucket: "pass", Link: "https://github.com/acme/app/actions/runs/1"},
					{Name: "lint", Bucket: "pass"},
				},
				Merged: true,
			},
			{
				Repository: "acme/clean", Ref: "main", Status: "skipped", Reason: "no work needed",
			},
		},
	}
}

func TestDepsCovReportingReportMarkdown(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	report := depsCovReportingReport(worktree)
	markdown := report.Markdown()

	for _, expected := range []string{
		"# WB exact dependency set: @acme/widget@1.2.3\n\n",
		"- Ecosystem: `npm`\n",
		"- Resolved reference: `" + strings.Repeat("a", 40) + "`\n",
		"- Status: `completed`\n",
		"- Base ref: `main`\n",
		"- Validation mode: `full`\n",
		"- Parallelism: `3`\n\n",
		"## Dependency order\n\nProvider-first layers; selected layers: `0-2`.\n",
		"| `00` | `completed` | `acme/provider` |\n",
		"| `01` | `failed` | `acme/app` |\n",
		"| `02` | `not_selected` | — |\n",
		"- Layer `01`: `acme/app -> acme/consumer -> acme/app`\n",
		"## Repository index\n\n",
		"| `acme/app` | `completed` | all \\| checks passed and merged | `1` | `" + strings.Repeat("b", 40) + "` | [PR](https://github.com/acme/app/pull/7) | `true` |\n",
		"| `acme/clean` | `skipped` | no work needed | `0` | `` |  | `false` |\n",
		"## acme/app\n\n- Status: `completed` — all | checks passed\nand merged\n",
		"- Base: `origin/main`\n",
		"- Canonical clone: [",
		"- Operation worktree: [",
		"- Inspect diff: `git -C '" + worktree + "' diff origin/main`\n",
		"### Dependency decisions\n\n",
		"| [.github/workflows/ci.yml](file://" + filepath.ToSlash(filepath.Join(worktree, ".github/workflows/ci.yml")) + ") | `main` | `unknown` | `1.2.3` | `" + strings.Repeat("a", 40) + "` | `" + strings.Repeat("a", 40) + "` | `updated` | exact target applied |\n",
		"### Exact dependency PR deltas\n\n",
		"| https://github.com/acme/app/pull/7 | `" + strings.Repeat("b", 40) + "` | `package.json:dependencies.nx` | `1.2.2` | `1.2.3` | `1.2.3` | `package-lock.json` | `packages|node_modules/nx|version` | `1.2.3` | true |\n",
		"### Changed files\n\n",
		"- [.github/workflows/ci.yml](file://" + filepath.ToSlash(filepath.Join(worktree, ".github/workflows/ci.yml")) + ")\n",
		"### Local verification\n\n",
		"- `go test ./...`: `passed` — flaky on first attempt\n",
		"### GitHub checks\n\n",
		"- [CI](https://github.com/acme/app/actions/runs/1): `pass`\n",
		"- `lint`: `pass`\n",
		"## acme/clean\n\n- Status: `skipped` — no work needed\n",
	} {
		depsCovMustContain(t, markdown, expected)
	}
	// Only the repository with a canonical clone and a worktree renders those
	// lines; acme/clean has neither.
	if strings.Count(markdown, "- Canonical clone: [") != 1 || strings.Count(markdown, "- Operation worktree: [") != 1 {
		t.Fatalf("clone/worktree lines were not scoped to acme/app:\n%s", markdown)
	}
	if strings.Count(markdown, "## acme/clean") != 1 {
		t.Fatalf("acme/clean section is not rendered exactly once:\n%s", markdown)
	}
	cleanSection := markdown[strings.Index(markdown, "## acme/clean"):]
	depsCovMustNotContain(t, cleanSection, "- Canonical clone: [")
	depsCovMustNotContain(t, cleanSection, "### GitHub checks")
	depsCovMustNotContain(t, cleanSection, "### Dependency decisions")
}

// An unordered run must render no ordering section at all, and an absent
// OrderReport must not panic the nil-receiver path.
func TestDepsCovReportingReportMarkdownSkipsAbsentOrder(t *testing.T) {
	t.Parallel()
	unordered := Report{Repositories: []RepositoryReport{{Repository: "acme/app", Ref: "main", Status: "skipped", Reason: "no change"}}}
	markdown := unordered.Markdown()
	depsCovMustContain(t, markdown, "| `acme/app` | `skipped` | no change | `0` | `` |  | `false` |\n")
	depsCovMustNotContain(t, markdown, "## Dependency order")
	depsCovMustNotContain(t, markdown, "## Release order")

	emptyOrder := Report{Order: &OrderReport{Selection: "0-1"}}
	depsCovMustNotContain(t, emptyOrder.Markdown(), "## Dependency order")
}

func TestDepsCovReportingReportJSONUsesYAMLFieldNames(t *testing.T) {
	t.Parallel()
	report := depsCovReportingReport(t.TempDir())
	raw, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	document := depsCovJSONDocument(t, raw)
	for _, key := range []string{"schema_version", "operation", "status", "target", "github_dir", "base_ref", "validation_mode", "parallel", "order", "repositories"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("JSON is missing key %q: %s", key, raw)
		}
	}
	for _, leaked := range []string{"SchemaVersion", "Operation", "Target", "Repositories", "ValidationMode"} {
		if _, ok := document[leaked]; ok {
			t.Fatalf("JSON leaked the Go field name %q: %s", leaked, raw)
		}
	}
	if _, ok := document["verification"]; ok {
		t.Fatalf("empty verification list was emitted despite omitempty: %s", raw)
	}
	if document["schema_version"] != float64(1) || document["parallel"] != float64(3) || document["validation_mode"] != "full" {
		t.Fatalf("JSON scalars = %v", document)
	}
	target := depsCovJSONObject(t, document, "target")
	if target["ecosystem"] != "npm" || target["dependency"] != "@acme/widget" || target["version"] != "1.2.3" || target["resolved"] != strings.Repeat("a", 40) {
		t.Fatalf("target = %+v", target)
	}
	order := depsCovJSONObject(t, document, "order")
	if len(depsCovJSONArray(t, order, "layers")) != 3 || len(depsCovJSONArray(t, order, "cycles")) != 1 {
		t.Fatalf("order = %+v", order)
	}
	repositories := depsCovJSONArray(t, document, "repositories")
	if len(repositories) != 2 {
		t.Fatalf("repositories = %+v", repositories)
	}
	first, ok := repositories[0].(map[string]any)
	if !ok || first["repository"] != "acme/app" || first["merged"] != true {
		t.Fatalf("first repository = %+v", repositories[0])
	}
	decisions := depsCovJSONArray(t, first, "decisions")
	decision, ok := decisions[0].(map[string]any)
	if !ok || decision["file"] != ".github/workflows/ci.yml" || decision["target_version"] != "1.2.3" {
		t.Fatalf("decision = %+v", decisions[0])
	}
	verifications := depsCovJSONArray(t, first, "verifications")
	verification, ok := verifications[0].(map[string]any)
	if !ok || verification["command"] != "go test ./..." || verification["detail"] != "flaky on first attempt" {
		t.Fatalf("verification = %+v", verifications[0])
	}
}

func TestDepsCovReportingWriteReportsCreatesExactlyTwoArtifacts(t *testing.T) {
	t.Parallel()
	report := depsCovReportingReport(t.TempDir())
	directory := filepath.Join(t.TempDir(), "nested", "reports")
	if err := WriteReports(directory, report); err != nil {
		t.Fatalf("WriteReports: %v", err)
	}
	if names := depsCovDirNames(t, directory); !reflect.DeepEqual(names, []string{"deps-set.md", "deps-set.yaml"}) {
		t.Fatalf("report directory = %v, want exactly the Markdown and YAML artifacts", names)
	}
	if got, want := string(depsCovReadFile(t, filepath.Join(directory, "deps-set.md"))), report.Markdown(); got != want {
		t.Fatalf("deps-set.md differs from Report.Markdown():\n%s", got)
	}
	wantYAML, err := report.YAML()
	if err != nil {
		t.Fatalf("Report.YAML: %v", err)
	}
	if got := string(depsCovReadFile(t, filepath.Join(directory, "deps-set.yaml"))); got != string(wantYAML) {
		t.Fatalf("deps-set.yaml differs from Report.YAML():\n%s", got)
	}
}

func TestDepsCovReportingWriteReportsSurfacesEveryFilesystemFailure(t *testing.T) {
	t.Parallel()
	report := depsCovReportingReport(t.TempDir())
	file := filepath.Join(t.TempDir(), "plain-file")
	depsCovWriteFile(t, file, "not a directory")
	if err := WriteReports(filepath.Join(file, "reports"), report); err == nil {
		t.Fatal("a report directory nested under a file must fail")
	}

	markdownBlocked := t.TempDir()
	depsCovMkdir(t, filepath.Join(markdownBlocked, "deps-set.md"))
	err := WriteReports(markdownBlocked, report)
	if err == nil || !strings.Contains(err.Error(), "deps-set.md") {
		t.Fatalf("error = %v, want the Markdown write refusal naming deps-set.md", err)
	}
	if _, statErr := os.Stat(filepath.Join(markdownBlocked, "deps-set.yaml")); statErr == nil {
		t.Fatal("deps-set.yaml must not be written after the Markdown write failed")
	}

	yamlBlocked := t.TempDir()
	depsCovMkdir(t, filepath.Join(yamlBlocked, "deps-set.yaml"))
	err = WriteReports(yamlBlocked, report)
	if err == nil || !strings.Contains(err.Error(), "deps-set.yaml") {
		t.Fatalf("error = %v, want the YAML write refusal naming deps-set.yaml", err)
	}
}

// ---------------------------------------------------------------------------
// peers_report.go
// ---------------------------------------------------------------------------

func TestDepsCovReportingPeerReportMarkdown(t *testing.T) {
	t.Parallel()
	report := PeerReport{
		SchemaVersion: 1, Package: "@acme/widget", Version: "2.1.0", Source: "test registry",
		Against: "/checkout", AgainstName: "@acme/host",
		ObservedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		Peers: []PeerRow{
			{Peer: "react", Required: "^18.0.0", Installed: "18.3.1", InstalledSource: "pnpm-lock.yaml", Verdict: PeerSatisfied},
			{Peer: "@acme/old", Required: "^2.0.0", Installed: "1.4.0", InstalledSource: "pnpm-lock.yaml", Verdict: PeerUnsatisfied, Reason: "the target resolves 1.4.0, which ^2.0.0 does not admit"},
			{Peer: "vue", Required: "^3.0.0", Verdict: PeerMissing, Reason: "the target checkout neither declares nor resolves this package"},
			{Peer: "@acme/opt", Required: "^1.0.0", Optional: true, Verdict: PeerOptionalMissing, Reason: "the publisher marks this peer optional in peerDependenciesMeta, and the target does not provide it"},
			{Peer: "typescript", Required: "5.0.0 - 6.0.0", Installed: "5.4.2", InstalledSource: "package.json", Verdict: PeerUnevaluated, Reason: "hyphen ranges are not evaluated"},
		},
		Summary: PeerSummary{Total: 5, Satisfied: 1, Unsatisfied: 1, Missing: 1, OptionalMissing: 1, Unevaluated: 1},
	}
	markdown := report.Markdown()
	for _, expected := range []string{
		"# WB npm peer compatibility\n\n",
		"- Package: `@acme/widget`\n",
		"- Published version: `2.1.0`\n",
		"- Against: `/checkout` (`@acme/host`)\n",
		"- Source: `test registry`\n",
		"- Observed at: `2026-09-02T12:00:00Z`\n\n",
		"| Peer | Required | Installed | Source | Verdict | Reason |\n",
		"| `react` | `^18.0.0` | `18.3.1` | `pnpm-lock.yaml` | `satisfied` |  |\n",
		"| `@acme/old` | `^2.0.0` | `1.4.0` | `pnpm-lock.yaml` | `unsatisfied` | the target resolves 1.4.0, which ^2.0.0 does not admit |\n",
		"| `vue` | `^3.0.0` | — | — | `missing` | the target checkout neither declares nor resolves this package |\n",
		"| `@acme/opt *(optional)*` | `^1.0.0` | — | — | `optional_missing` | the publisher marks this peer optional in peerDependenciesMeta, and the target does not provide it |\n",
		"| `typescript` | `5.0.0 - 6.0.0` | `5.4.2` | `package.json` | `unevaluated` | hyphen ranges are not evaluated |\n",
		"\n5 peer(s): 1 satisfied, 1 unsatisfied, 1 missing, 1 optional missing, 1 unevaluated.\n",
		"\nThis package cannot be used in the target checkout as it stands. ",
	} {
		depsCovMustContain(t, markdown, expected)
	}

	unevaluated := PeerReport{
		Package: "react", Against: "/checkout",
		ObservedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		Peers:      []PeerRow{{Peer: "typescript", Required: "5.0.0 - 6.0.0", Installed: "5.4.2", Verdict: PeerUnevaluated, Reason: "hyphen ranges are not evaluated"}},
		Summary:    PeerSummary{Total: 1, Unevaluated: 1},
	}
	unevaluatedMarkdown := unevaluated.Markdown()
	depsCovMustContain(t, unevaluatedMarkdown, "\nNothing blocks reuse among the rows WB evaluated. ")
	depsCovMustContain(t, unevaluatedMarkdown, "which is not the same as judging them compatible.\n")
	depsCovMustNotContain(t, unevaluatedMarkdown, "Every peer requirement is met")
	depsCovMustNotContain(t, unevaluatedMarkdown, "- Published version:")
	depsCovMustNotContain(t, unevaluatedMarkdown, "- Against: `/checkout` (`")
	depsCovMustNotContain(t, unevaluatedMarkdown, "- Source:")

	clean := PeerReport{
		Package: "react", Against: "/checkout",
		ObservedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		Peers:      []PeerRow{{Peer: "react", Required: "^18.0.0", Installed: "18.3.1", InstalledSource: "pnpm-lock.yaml", Verdict: PeerSatisfied}},
		Summary:    PeerSummary{Total: 1, Satisfied: 1},
	}
	depsCovMustContain(t, clean.Markdown(), "\nEvery peer requirement is met by the target checkout.\n")

	noPeers := PeerReport{Package: "react", Against: "/checkout", ObservedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
	noPeersMarkdown := noPeers.Markdown()
	depsCovMustContain(t, noPeersMarkdown, "\nThis package declares no peer dependencies: it requires nothing of its host.\n")
	depsCovMustNotContain(t, noPeersMarkdown, "| Peer |")
	depsCovMustNotContain(t, noPeersMarkdown, "peer(s):")
}

func TestDepsCovReportingPeerReportJSONAndYAML(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	report := PeerReport{
		SchemaVersion: 1, Package: "@acme/widget", Version: "2.1.0", Source: "test registry",
		Against: "/checkout", AgainstName: "@acme/host", ObservedAt: observed,
		Peers: []PeerRow{
			{Peer: "react", Required: "^18.0.0", Installed: "18.3.1", InstalledSource: "pnpm-lock.yaml", Verdict: PeerSatisfied},
			{Peer: "@acme/opt", Required: "^1.0.0", Optional: true, Verdict: PeerOptionalMissing},
		},
		Summary: PeerSummary{Total: 2, Satisfied: 1, OptionalMissing: 1},
	}
	raw, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	document := depsCovJSONDocument(t, raw)
	for _, key := range []string{"schema_version", "package", "version", "source", "against", "against_name", "observed_at", "peers", "summary"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("JSON is missing key %q: %s", key, raw)
		}
	}
	if document["package"] != "@acme/widget" || document["against_name"] != "@acme/host" {
		t.Fatalf("JSON header = %v", document)
	}
	peers := depsCovJSONArray(t, document, "peers")
	if len(peers) != 2 {
		t.Fatalf("peers = %+v", peers)
	}
	optional, ok := peers[1].(map[string]any)
	if !ok || optional["optional"] != true || optional["verdict"] != PeerOptionalMissing {
		t.Fatalf("optional peer row = %+v", peers[1])
	}
	if _, ok := optional["installed"]; ok {
		t.Fatalf("empty installed value was emitted despite omitempty: %+v", optional)
	}
	summary := depsCovJSONObject(t, document, "summary")
	if summary["total"] != float64(2) || summary["optional_missing"] != float64(1) || summary["satisfied"] != float64(1) {
		t.Fatalf("summary = %+v", summary)
	}

	minimalRaw, err := (PeerReport{SchemaVersion: 1, Package: "react", Against: "/checkout"}).JSON()
	if err != nil {
		t.Fatalf("minimal JSON: %v", err)
	}
	minimal := depsCovJSONDocument(t, minimalRaw)
	for _, absent := range []string{"version", "source", "against_name"} {
		if _, ok := minimal[absent]; ok {
			t.Fatalf("empty %q was emitted despite omitempty: %s", absent, minimalRaw)
		}
	}

	yamlRaw, err := report.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	var loaded PeerReport
	if err := yaml.Unmarshal(yamlRaw, &loaded); err != nil {
		t.Fatalf("YAML round trip: %v\n%s", err, yamlRaw)
	}
	if loaded.Package != report.Package || loaded.Version != report.Version || loaded.AgainstName != report.AgainstName || loaded.Summary != report.Summary || len(loaded.Peers) != 2 {
		t.Fatalf("loaded peer report = %+v", loaded)
	}
	if !loaded.ObservedAt.Equal(observed) {
		t.Fatalf("observed_at round-tripped to %s, want %s", loaded.ObservedAt, observed)
	}
	if loaded.Peers[1].Optional != true || loaded.Peers[0].InstalledSource != "pnpm-lock.yaml" {
		t.Fatalf("loaded peers = %+v", loaded.Peers)
	}
}

func TestDepsCovReportingPeerCell(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct{ value, want string }{
		{value: "", want: "—"},
		{value: "18.3.1", want: "`18.3.1`"},
		{value: "pnpm-lock.yaml", want: "`pnpm-lock.yaml`"},
	} {
		if got := peerCell(fixture.value); got != fixture.want {
			t.Fatalf("peerCell(%q) = %q, want %q", fixture.value, got, fixture.want)
		}
	}
}

// ---------------------------------------------------------------------------
// graph_report.go + graph_svg.go
// ---------------------------------------------------------------------------

func depsCovReportingGraph() Graph {
	return Graph{
		SchemaVersion: 1,
		Ecosystem:     EcosystemGo,
		BaseRef:       "main",
		Filters:       GraphFilters{Dependencies: []string{"example.com/provider", "example.com/external"}},
		Summary: GraphSummary{
			Repositories: 8, Modules: 4, Requirements: 11, InternalRequirements: 7,
			ExternalDependencies: 4, Selections: 7, AmbiguousProviders: 1,
		},
		Order: GraphOrder{
			Layers: []GraphOrderLayer{
				{Index: 0, Repositories: []string{"acme/provider"}},
				{Index: 1, Repositories: []string{"acme/app", "acme/consumer"}},
			},
			Cycles: []GraphOrderCycle{{Layer: 1, Repositories: []string{"acme/app", "acme/consumer"}, Path: "acme/app -> acme/consumer -> acme/app"}},
		},
		Repositories: []GraphRepository{
			{Slug: "acme/provider", Organization: "acme", Modules: []string{"example.com/provider"}},
			{Slug: "acme/app", Organization: "acme", Modules: []string{"example.com/app", "example.com/app/extra"}},
			{Slug: "acme/consumer", Organization: "acme"},
			{Slug: "acme/a", Organization: "acme"},
			{Slug: "acme/b", Organization: "acme"},
			{Slug: "acme/very-long-repository-identity-name-for-truncation", Organization: "acme"},
			{Slug: "acme/indirect-provider", Organization: "acme"},
			{Slug: "other-org/isolated", Organization: "other-org"},
		},
		Modules: []GraphModule{
			{Path: "example.com/app", Repository: "acme/app", Manifest: "go.mod"},
			{Path: "example.com/app/extra", Repository: "acme/app", Manifest: "go.mod"},
			{Path: "example.com/consumer", Repository: "acme/consumer", Manifest: "go.mod"},
			{Path: "example.com/provider", Repository: "acme/provider", Manifest: "go.mod"},
		},
		Requirements: []GraphRequirement{
			{Dependency: "example.com/nowhere", Version: "v1.0.0", ConsumerModule: "example.com/app", ConsumerRepository: "", Manifest: "go.mod"},
			{Dependency: "example.com/ambiguous", Version: "v1.0.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/app", Manifest: "go.mod", ProviderCandidates: []string{"acme/one", "acme/two"}},
			{Dependency: "example.com/resolved", Version: "v1.0.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/app", Manifest: "go.mod", ProviderRepository: "acme/one", ProviderCandidates: []string{"acme/one", "acme/two"}},
			{Dependency: "example.com/provider", Version: "v0.9.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/app", Manifest: "go.mod", ProviderModule: "example.com/provider", ProviderRepository: "acme/provider"},
			{Dependency: "example.com/provider", Version: "v0.9.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/app", Manifest: "go.sum", ProviderModule: "example.com/provider", ProviderRepository: "acme/provider", Indirect: true},
			{Dependency: "example.com/provider", Version: "v1.0.0", ConsumerModule: "example.com/consumer", ConsumerRepository: "acme/consumer", Manifest: "go.mod", ProviderModule: "example.com/provider", ProviderRepository: "acme/provider"},
			{Dependency: "example.com/a-mod", Version: "v1.0.0", ConsumerModule: "example.com/b", ConsumerRepository: "acme/b", Manifest: "go.mod", ProviderModule: "example.com/a-mod", ProviderRepository: "acme/a"},
			{Dependency: "example.com/b-mod", Version: "v1.0.0", ConsumerModule: "example.com/a", ConsumerRepository: "acme/a", Manifest: "go.mod", ProviderModule: "example.com/b-mod", ProviderRepository: "acme/b"},
			{Dependency: "example.com/very-long-dependency-name-that-needs-truncation", Version: "v1.0.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/app", Manifest: "go.mod", ProviderRepository: "acme/very-long-repository-identity-name-for-truncation", ProviderCandidates: []string{"acme/very-long-repository-identity-name-for-truncation", "acme/two", "acme/three"}},
			{Dependency: "example.com/escaped", Version: "v1.0.0", ConsumerModule: "example.com/app", ConsumerRepository: "acme/<b>", Manifest: "go.mod"},
			{Dependency: "example.com/indirect-only", Version: "v1.0.0", ConsumerModule: "example.com/consumer", ConsumerRepository: "acme/consumer", Manifest: "go.mod", ProviderRepository: "acme/indirect-provider", Indirect: true},
		},
		DiscoverySkips:         []GraphDiscoverySkip{{Repository: "acme/website", Reason: "no go.mod"}},
		DefaultBranchFallbacks: []GraphDefaultBranchFallback{{Repository: "acme/legacy", Ref: "master"}},
		ManifestWarnings:       []GraphManifestWarning{{Repository: "acme/tpl", Manifest: "templates/go.mod", Reason: "unparseable"}},
		AmbiguousModules:       []GraphAmbiguousModuleWarning{{Module: "example.com/dup", Repository: "acme/one", Manifest: "go.mod", Duplicates: []string{"acme/two"}, Reason: "stale clone"}},
	}
}

func TestDepsCovReportingGraphMarkdown(t *testing.T) {
	t.Parallel()
	graph := depsCovReportingGraph()
	markdown := graph.Markdown()
	for _, expected := range []string{
		"# WB dependency graph\n\n",
		"- Ecosystem: `go`\n",
		"- Base ref: `main`\n",
		"- Repositories: `8`\n",
		"- Modules: `4`\n",
		"- Requirements: `11` (`7` internal)\n",
		"- External dependencies: `4`\n",
		"- Ambiguous internal providers: `1`\n",
		"- Observed dependency/version selections: `7`\n",
		"- Dependency filters: `example.com/provider`, `example.com/external`\n",
		"\n## Release order\n\n",
		"| `00` | `acme/provider` | `1` |\n",
		"| `01` | `acme/app`, `acme/consumer` | `2` |\n",
		"## Requirement evidence\n\n",
		"| `example.com/nowhere` | `v1.0.0` | `` | `example.com/app` | `go.mod` | `direct` | `external` | — |\n",
		"| `example.com/ambiguous` | `v1.0.0` | `acme/app` | `example.com/app` | `go.mod` | `direct` | `ambiguous: acme/one, acme/two` | [inspect consumer](https://codegrapher.dev/github.com/acme/app) |\n",
		"| `example.com/resolved` | `v1.0.0` | `acme/app` | `example.com/app` | `go.mod` | `direct` | `acme/one; declarations: acme/one, acme/two` | [inspect consumer](https://codegrapher.dev/github.com/acme/app) |\n",
		"| `example.com/provider` | `v0.9.0` | `acme/app` | `example.com/app` | `go.sum` | `indirect` | `acme/provider` | [inspect consumer](https://codegrapher.dev/github.com/acme/app) |\n",
		"| `example.com/escaped` | `v1.0.0` | `acme/<b>` | `example.com/app` | `go.mod` | `direct` | `external` | [inspect consumer](https://codegrapher.dev/github.com/acme/%3Cb%3E) |\n",
	} {
		depsCovMustContain(t, markdown, expected)
	}
	depsCovMustNotContain(t, markdown, "## Release order\n\n\n## Release order")

	// An unfiltered graph must not render a filter line at all.
	unfiltered := depsCovReportingGraph()
	unfiltered.Filters = GraphFilters{}
	depsCovMustNotContain(t, unfiltered.Markdown(), "- Dependency filters:")
}

func TestDepsCovReportingGraphYAMLAndJSONRoundTrip(t *testing.T) {
	t.Parallel()
	graph := depsCovReportingGraph()
	yamlRaw, err := graph.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	var loaded Graph
	if err := yaml.Unmarshal(yamlRaw, &loaded); err != nil {
		t.Fatalf("YAML round trip: %v\n%s", err, yamlRaw)
	}
	if !reflect.DeepEqual(loaded, graph) {
		t.Fatalf("YAML round trip lost fields:\nloaded: %+v\nwant:   %+v", loaded, graph)
	}

	jsonRaw, err := graph.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(jsonRaw, &document); err != nil {
		t.Fatalf("graph JSON is not valid: %v\n%s", err, jsonRaw)
	}
	if document["schema_version"] != float64(1) || document["ecosystem"] != "go" || document["base_ref"] != "main" {
		t.Fatalf("JSON header = %v", document)
	}
	summary := depsCovJSONObject(t, document, "summary")
	if summary["repositories"] != float64(8) || summary["ambiguous_providers"] != float64(1) {
		t.Fatalf("summary = %+v", summary)
	}
	if len(depsCovJSONArray(t, document, "repositories")) != 8 || len(depsCovJSONArray(t, document, "requirements")) != 11 {
		t.Fatalf("JSON arrays were truncated: %s", jsonRaw)
	}
	if !strings.Contains(string(jsonRaw), `\u003cb\u003e`) {
		t.Fatalf("Graph.JSON must escape HTML in labels: %s", jsonRaw)
	}
	if !strings.Contains(string(jsonRaw), "\n  \"schema_version\": 1,") {
		t.Fatalf("Graph.JSON must be two-space indented: %s", jsonRaw)
	}
}

func TestDepsCovReportingGraphOutputDrivesEveryFormat(t *testing.T) {
	t.Parallel()
	graph := depsCovReportingGraph()
	for _, fixture := range []struct {
		format string
		want   []byte
	}{
		{format: "markdown", want: []byte(graph.Markdown())},
	} {
		got, err := graph.Output(fixture.format, GraphViewSelections)
		if err != nil {
			t.Fatalf("Output(%q): %v", fixture.format, err)
		}
		if string(got) != string(fixture.want) {
			t.Fatalf("Output(%q) differs from the dedicated renderer", fixture.format)
		}
	}
	for _, fixture := range []struct {
		format string
		render func() ([]byte, error)
	}{
		{format: "yaml", render: graph.YAML},
		{format: "json", render: graph.JSON},
		{format: "svg", render: func() ([]byte, error) { return graph.SVG(GraphViewSelections) }},
		{format: "html", render: func() ([]byte, error) { return graph.HTML(GraphViewSelections) }},
	} {
		got, err := graph.Output(fixture.format, GraphViewSelections)
		if err != nil {
			t.Fatalf("Output(%q): %v", fixture.format, err)
		}
		want, err := fixture.render()
		if err != nil {
			t.Fatalf("render %q: %v", fixture.format, err)
		}
		if string(got) != string(want) {
			t.Fatalf("Output(%q) differs from the dedicated renderer", fixture.format)
		}
	}
	for _, format := range []string{"dot", ""} {
		got, err := graph.Output(format, GraphViewRepositories)
		if err == nil {
			t.Fatalf("Output(%q) accepted an unknown format and returned %d bytes", format, len(got))
		}
		want := `unknown --format "` + format + `" (want markdown, yaml, json, svg, or html)`
		if err.Error() != want {
			t.Fatalf("Output(%q) error = %q, want %q", format, err.Error(), want)
		}
		if got != nil {
			t.Fatalf("Output(%q) returned bytes alongside an error", format)
		}
	}
}

func TestDepsCovReportingGraphHTML(t *testing.T) {
	t.Parallel()
	graph := depsCovReportingGraph()
	htmlBytes, err := graph.HTML(GraphViewDependencies)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	rendered := string(htmlBytes)
	for _, expected := range []string{
		`<button type="button" role="tab" data-select-view="repos" aria-selected="false">Repositories</button>`,
		`<button type="button" role="tab" data-select-view="dependencies" aria-selected="true">Dependencies</button>`,
		`<button type="button" role="tab" data-select-view="selections" aria-selected="false">Versions</button>`,
		`<section class="graph-panel" data-panel-view="repos">`,
		`<section class="graph-panel active" data-panel-view="dependencies">`,
		`<option value="acme">acme</option>`,
		`<option value="other-org">other-org</option>`,
		`<div class="metric"><b>8</b><span>repositories</span></div>`,
		`<div class="metric"><b>1</b><span>ambiguous providers</span></div>`,
		`href="https://codegrapher.dev/github.com/acme/app"`,
		`<td><code>example.com/provider</code></td>`,
		`Canonical requirement evidence`,
	} {
		depsCovMustContain(t, rendered, expected)
	}
	if strings.Count(rendered, `<option value="acme">acme</option>`) != 1 {
		t.Fatalf("organization list is not deduplicated:\n%s", rendered)
	}
	if strings.Contains(rendered, "<script src=") || strings.Contains(rendered, "<link rel=") {
		t.Fatal("HTML report contains an external asset")
	}

	escaped := depsCovReportingGraph()
	escaped.BaseRef = "main<&>"
	escaped.Ecosystem = Ecosystem("go<&>")
	escapedHTML, err := escaped.HTML(GraphViewRepositories)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	depsCovMustContain(t, string(escapedHTML), "Base: origin/main&lt;&amp;&gt;")
	depsCovMustContain(t, string(escapedHTML), "Ecosystem: go&lt;&amp;&gt;")

	if _, err := graph.HTML(GraphView("bogus")); err == nil || err.Error() != `unknown dependency graph view "bogus" (want repos, dependencies, or selections)` {
		t.Fatalf("HTML(bogus) error = %v", err)
	}
}

func TestDepsCovReportingGraphViewLabel(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		view GraphView
		want string
	}{
		{view: GraphViewRepositories, want: "Repositories"},
		{view: GraphViewDependencies, want: "Dependencies"},
		{view: GraphViewSelections, want: "Versions"},
		{view: GraphView("modules"), want: "modules"},
		{view: GraphView(""), want: ""},
	} {
		if got := graphViewLabel(fixture.view); got != fixture.want {
			t.Fatalf("graphViewLabel(%q) = %q, want %q", fixture.view, got, fixture.want)
		}
	}
}

func TestDepsCovReportingWriteGraphReports(t *testing.T) {
	t.Parallel()
	graph := depsCovReportingGraph()
	directory := filepath.Join(t.TempDir(), "nested", "graph")
	paths, err := WriteGraphReports(directory, graph, GraphViewSelections)
	if err != nil {
		t.Fatalf("WriteGraphReports: %v", err)
	}
	if paths.Markdown != filepath.Join(directory, "deps-graph.md") ||
		paths.YAML != filepath.Join(directory, "deps-graph.yaml") ||
		paths.JSON != filepath.Join(directory, "deps-graph.json") ||
		paths.SVG != filepath.Join(directory, "deps-graph.svg") ||
		paths.HTML != filepath.Join(directory, "deps-graph.html") {
		t.Fatalf("paths = %+v", paths)
	}
	if names := depsCovDirNames(t, directory); !reflect.DeepEqual(names, []string{"deps-graph.html", "deps-graph.json", "deps-graph.md", "deps-graph.svg", "deps-graph.yaml"}) {
		t.Fatalf("graph report directory = %v", names)
	}
	wantYAML, err := graph.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	wantJSON, err := graph.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	wantSVG, err := graph.SVG(GraphViewSelections)
	if err != nil {
		t.Fatalf("SVG: %v", err)
	}
	wantHTML, err := graph.HTML(GraphViewSelections)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	for path, want := range map[string][]byte{
		paths.Markdown: []byte(graph.Markdown()),
		paths.YAML:     wantYAML,
		paths.JSON:     wantJSON,
		paths.SVG:      wantSVG,
		paths.HTML:     wantHTML,
	} {
		if got := depsCovReadFile(t, path); string(got) != string(want) {
			t.Fatalf("%s does not match its renderer output", path)
		}
	}

	invalid := filepath.Join(t.TempDir(), "invalid")
	invalidPaths, err := WriteGraphReports(invalid, graph, GraphView("bogus"))
	if err == nil || err.Error() != `unknown dependency graph view "bogus" (want repos, dependencies, or selections)` {
		t.Fatalf("WriteGraphReports error = %v", err)
	}
	if invalidPaths.Markdown != filepath.Join(invalid, "deps-graph.md") {
		t.Fatalf("paths must be populated before the failure: %+v", invalidPaths)
	}
	if names := depsCovDirNames(t, invalid); len(names) != 0 {
		t.Fatalf("an invalid view must write no artifact: %v", names)
	}

	file := filepath.Join(t.TempDir(), "plain-file")
	depsCovWriteFile(t, file, "not a directory")
	if _, err := WriteGraphReports(filepath.Join(file, "graph"), graph, GraphViewSelections); err == nil {
		t.Fatal("a graph report directory nested under a file must fail")
	}

	blocked := t.TempDir()
	depsCovMkdir(t, filepath.Join(blocked, "deps-graph.md"))
	if _, err := WriteGraphReports(blocked, graph, GraphViewSelections); err == nil || !strings.Contains(err.Error(), "deps-graph.md") {
		t.Fatalf("error = %v, want the artifact write refusal naming deps-graph.md", err)
	}
	if _, statErr := os.Stat(filepath.Join(blocked, "deps-graph.svg")); statErr == nil {
		t.Fatal("later artifacts must not be written after the first write failed")
	}
}

func TestDepsCovReportingGraphSVG(t *testing.T) {
	t.Parallel()
	graph := depsCovReportingGraph()
	repositories, err := graph.SVG(GraphViewRepositories)
	if err != nil {
		t.Fatalf("SVG repositories: %v", err)
	}
	rendered := string(repositories)
	for _, expected := range []string{
		`role="img"`,
		`aria-labelledby="title_repos desc_repos"`,
		`data-view="repos"`,
		"Release wave 00",
		"Release wave 01",
		`class="edge indirect"`,
		`class="edge behind"`,
		`class="edge-label"`,
		`class="node repository behind"`,
		`tabindex="0"`,
		"…",
	} {
		depsCovMustContain(t, rendered, expected)
	}
	depsCovMustNotContain(t, rendered, "acme/<b>")

	dependencies, err := graph.SVG(GraphViewDependencies)
	if err != nil {
		t.Fatalf("SVG dependencies: %v", err)
	}
	depsCovMustContain(t, string(dependencies), "Layer 00")
	depsCovMustNotContain(t, string(dependencies), "Release wave 00")

	selections, err := graph.SVG(GraphViewSelections)
	if err != nil {
		t.Fatalf("SVG selections: %v", err)
	}
	selectionRendered := string(selections)
	depsCovMustContain(t, selectionRendered, `class="node selection behind"`)
	depsCovMustContain(t, selectionRendered, `class="node selection fleet-highest"`)
	depsCovMustContain(t, selectionRendered, "fleet-highest observed version")
	depsCovMustContain(t, selectionRendered, "behind fleet-highest observed version")

	// A one-node projection must be padded to the minimum canvas size.
	tiny := Graph{Ecosystem: EcosystemGo, BaseRef: "main", Repositories: []GraphRepository{{Slug: "acme/only"}}}
	tinySVG, err := tiny.SVG(GraphViewRepositories)
	if err != nil {
		t.Fatalf("SVG tiny: %v", err)
	}
	depsCovMustContain(t, string(tinySVG), `viewBox="0 0 960 520"`)
	depsCovMustContain(t, string(tinySVG), "The graph contains 1 nodes and 0 edges.")

	if _, err := graph.SVG(GraphView("bogus")); err == nil || err.Error() != `unknown dependency graph view "bogus" (want repos, dependencies, or selections)` {
		t.Fatalf("SVG(bogus) error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// bump_report.go
// ---------------------------------------------------------------------------

func depsCovReportingBumpReport() BumpReport {
	checkedAt := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return BumpReport{
		SchemaVersion:          2,
		Operation:              "deps-bump-go-acme-provider-v0-2-0",
		Status:                 "awaiting_release",
		Phase:                  BumpPhaseAwaitingRelease,
		Progress:               BumpProgress{Wave: 2, RepositoriesTotal: 4, RepositoriesCompleted: 3, LastRepository: "acme/last"},
		Ecosystem:              EcosystemGo,
		SeedEvents:             []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit", CheckedAt: checkedAt}},
		GitHubDir:              "/github",
		BaseRef:                "main",
		ValidationMode:         ValidationModeFast,
		Parallel:               4,
		ParallelExplicit:       true,
		RegistryLookupsSkipped: true,
		FetchCacheEnabled:      true,
		ExcludedRepositories:   []string{"acme/excluded"},
		HeldRepositories: []HeldRepository{
			{Repository: "acme/held-with-pr", PR: "https://github.com/acme/held-with-pr/pull/1", Reason: "owner decides"},
			{Repository: "acme/held-without-pr", Reason: "owner decides"},
		},
		Scopes: []string{"example.com/*", "acme/*"},
		ScopeResolutions: []LatestScopeResolution{
			{Dependency: "example.com/provider", Repository: "acme/provider", Version: "v0.2.0", Reason: "published | newer\nrelease"},
			{Dependency: "example.com/silent", Repository: "acme/silent", Reason: "no published version"},
		},
		DiscoverySkips:         []GraphDiscoverySkip{{Repository: "acme/website", Reason: "no go.mod"}},
		DefaultBranchFallbacks: []GraphDefaultBranchFallback{{Repository: "acme/legacy", Ref: "master"}},
		ManifestWarnings:       []GraphManifestWarning{{Repository: "acme/tpl", Manifest: "templates/go.mod", Reason: "unparseable"}},
		AmbiguousModules:       []GraphAmbiguousModuleWarning{{Module: "example.com/dup", Repository: "acme/one", Manifest: "go.mod", Duplicates: []string{"acme/two", "acme/three"}, Reason: "stale clone"}},
		Waves: []BumpWaveReport{{
			Index:          1,
			Status:         "awaiting_release",
			ValidationMode: ValidationModeFull,
			Events:         []ReleaseEvent{{Dependency: "example.com/provider", Version: "v0.2.0", Source: "explicit"}},
			Refreshes: []ReleaseEventRefresh{{
				Dependency: "example.com/provider", Before: "v0.1.0", After: "v0.2.0", CheckedAt: checkedAt, Reason: "newer | release",
			}},
			DeferredRepositories: []string{"acme/sink", "acme/sink-two"},
			HeldRepositories:     []HeldRepository{{Repository: "acme/held", PR: "https://github.com/acme/held/pull/9"}},
			Repositories: []RepositoryReport{
				{
					Repository: "acme/app", Ref: "main", Status: "updated", Reason: "applied | exactly",
					WorktreeDir: "/work/app", ChangedFiles: []string{"go.mod", "go.sum"},
					Commit: "abc", PR: "https://github.com/acme/app/pull/1", Merged: true,
					Decisions: []Decision{{
						Dependency: "example.com/provider", Ecosystem: EcosystemGo, File: "go.mod",
						AfterVersion: "v0.2.0", Action: "updated", Reason: "exact target",
					}},
					DependencyDeltas: []DependencyDelta{{
						SourcePR: "https://github.com/acme/app/pull/1", SourceHead: "head", Consumer: "acme/app",
						Ecosystem: EcosystemGo, Package: "example.com/provider", Manifest: "go.mod", Selector: "require:example.com/provider",
						Before: "v0.1.0", RequestedAfter: "v0.2.0", CandidateAfter: "v0.2.0",
						Lockfile: "go.sum", LockfileSelector: "h1", LockfileVersion: "v0.2.0", Reviewed: true,
					}},
				},
				{
					Repository: "acme/no-pr", Ref: "main", Status: "planned", Reason: "nothing to do",
					ChangedFiles: []string{"go.mod"},
				},
				{
					Repository: "acme/delta-only", Ref: "main", Status: "planned", Reason: "nothing to do",
					DependencyDeltas: []DependencyDelta{{
						SourcePR: "https://github.com/acme/delta-only/pull/3", SourceHead: "head", Consumer: "acme/delta-only",
						Ecosystem: EcosystemGo, Package: "example.com/provider", Manifest: "go.mod", Selector: "require:example.com/provider",
						Before: "v0.1.0", RequestedAfter: "v0.2.0", CandidateAfter: "v0.2.0",
					}},
				},
			},
			Releases: []ReleaseObservation{
				{
					Module: "example.com/app", Repository: "acme/app", Before: "v0.1.0", After: "v0.2.0",
					ExpectedRequirements: map[string]string{"example.com/provider": "v0.2.0", "example.com/other": "v1.0.0"},
					Status:               "published", Source: "registry", Reason: "observed | published",
				},
				{
					Module: "example.com/silent", Repository: "acme/silent",
					Status: "awaiting_release", Source: "registry", Reason: "no version observed",
				},
			},
		}},
	}
}

func TestDepsCovReportingBumpReportMarkdown(t *testing.T) {
	t.Parallel()
	report := depsCovReportingBumpReport()
	markdown := report.Markdown()
	for _, expected := range []string{
		"# WB dependency bump waves\n\n",
		"- Operation: `deps-bump-go-acme-provider-v0-2-0`\n",
		"- Ecosystem: `go`\n",
		"- Status: `awaiting_release`\n",
		"- Phase: `awaiting_release`\n",
		"- Progress: wave `2`, repositories `3/4`; last completed `acme/last`\n",
		"- Base ref: `main`\n",
		"- Validation mode: `fast`\n",
		"- Parallelism: `4`\n",
		"- Registry carrier and stale-event lookups: `skipped` (no-registry plan policy)\n",
		"- Waves: `1`\n\n",
		"## Seed release events\n\n- `example.com/provider@v0.2.0` — `explicit`\n",
		"\n## Excluded repositories\n\n",
		"Removed by `--exclude` before any discovery ran.",
		"- `acme/excluded`\n",
		"\n## Held pull requests\n\n",
		"- `acme/held-with-pr` — https://github.com/acme/held-with-pr/pull/1\n",
		"- `acme/held-without-pr` — —\n",
		"\n## Derived scopes\n\n",
		"Seed events above were derived by `--latest` from the registry for `example.com/*`, `acme/*`.",
		"| `example.com/provider` | `acme/provider` | `v0.2.0` | published \\| newer release |\n",
		"| `example.com/silent` | `acme/silent` | `—` | no published version |\n",
		"\n## Skipped discovery failures\n\n",
		"- `acme/website` — no go.mod\n",
		"\n## Default branch fallbacks\n\n",
		"- `acme/legacy` — base: `master` (default-branch fallback)\n",
		"\n## Manifest warnings\n\n",
		"- `acme/tpl` (`templates/go.mod`) — unparseable\n",
		"\n## Ambiguous module resolutions\n\n",
		"- `example.com/dup` — kept `acme/one` (`go.mod`); duplicate(s): `acme/two`, `acme/three` — stale clone\n",
		"\n## Wave 1 — `awaiting_release`\n\n",
		"Validation mode: `full`\n\nEvents:\n\n",
		"- `example.com/provider@v0.2.0` — `explicit`\n",
		"\n### Stale-event registry checks\n\n",
		"| `example.com/provider` | `v0.1.0` | `v0.2.0` | `2026-07-26T12:00:00Z` | newer \\| release |\n",
		"\n### Deferred to coalesce releases\n\n",
		"No worktree or CI run was started for these later provider-path repositories: `acme/sink`, `acme/sink-two`.\n",
		"\nWaves after this one are waiting on held pull requests:\n\n",
		"- `acme/held` — https://github.com/acme/held/pull/9\n",
		"| `acme/app` | `updated` | applied \\| exactly | `2` | `abc` | [PR](https://github.com/acme/app/pull/1) | `true` |\n",
		"| `acme/no-pr` | `planned` | nothing to do | `1` | `` |  | `false` |\n",
		"| `acme/delta-only` | `planned` | nothing to do | `0` | `` |  | `false` |\n",
		"\n### Release evidence\n\n",
		"| `example.com/app` | `acme/app` | `v0.1.0` | `v0.2.0` | `example.com/other@v1.0.0`<br>`example.com/provider@v0.2.0` | `published` | `registry` | observed \\| published |\n",
		"| `example.com/silent` | `acme/silent` | `` | `` | — | `awaiting_release` | `registry` | no version observed |\n",
		"\n### acme/app decisions\n\n",
		"Inspect the detailed patch with `git -C '/work/app' diff origin/main`.\n\n",
		"- `example.com/provider` in `go.mod`: `unknown` → `v0.2.0` (`updated`) — exact target\n",
		"\n#### acme/app exact dependency PR deltas\n\n",
		"| https://github.com/acme/app/pull/1 | `head` | `go.mod:require:example.com/provider` | `v0.1.0` | `v0.2.0` | `v0.2.0` | `go.sum` | `h1` | `v0.2.0` | true |\n",
		"\n### acme/delta-only decisions\n\n",
		"\n#### acme/delta-only exact dependency PR deltas\n\n",
		"| https://github.com/acme/delta-only/pull/3 | `head` | `go.mod:require:example.com/provider` | `v0.1.0` | `v0.2.0` | `v0.2.0` | `` | `` | `` | false |\n",
	} {
		depsCovMustContain(t, markdown, expected)
	}
	depsCovMustNotContain(t, markdown, "### acme/no-pr decisions")
	depsCovMustNotContain(t, markdown, "Inspect the detailed patch with `git -C ''")
}

func TestDepsCovReportingBumpReportMarkdownOptionalArms(t *testing.T) {
	t.Parallel()
	sparse := BumpReport{
		Operation: "deps-bump-npm-acme-widget-1-0-0", Ecosystem: EcosystemNPM, Status: "planned",
		BaseRef: "main", Parallel: 1, Progress: BumpProgress{Wave: 1, RepositoriesTotal: 1},
	}
	markdown := sparse.Markdown()
	depsCovMustContain(t, markdown, "- Progress: wave `1`, repositories `0/1`\n")
	depsCovMustNotContain(t, markdown, "; last completed")
	depsCovMustNotContain(t, markdown, "- Phase:")
	depsCovMustNotContain(t, markdown, "- Validation mode:")
	depsCovMustNotContain(t, markdown, "Registry carrier and stale-event lookups")
	depsCovMustNotContain(t, markdown, "## Excluded repositories")
	depsCovMustNotContain(t, markdown, "## Held pull requests")
	depsCovMustNotContain(t, markdown, "## Derived scopes")
	depsCovMustNotContain(t, markdown, "## Skipped discovery failures")
	depsCovMustNotContain(t, markdown, "## Default branch fallbacks")
	depsCovMustNotContain(t, markdown, "## Manifest warnings")
	depsCovMustNotContain(t, markdown, "## Ambiguous module resolutions")
	depsCovMustNotContain(t, markdown, "- Waves: `1`")

	noProgress := sparse
	noProgress.Progress = BumpProgress{}
	depsCovMustNotContain(t, noProgress.Markdown(), "- Progress:")
}

func TestDepsCovReportingExpectedRequirementsMarkdown(t *testing.T) {
	t.Parallel()
	if got := expectedRequirementsMarkdown(nil); got != "—" {
		t.Fatalf("expectedRequirementsMarkdown(nil) = %q, want an em dash", got)
	}
	if got := expectedRequirementsMarkdown(map[string]string{}); got != "—" {
		t.Fatalf("expectedRequirementsMarkdown(empty) = %q, want an em dash", got)
	}
	single := expectedRequirementsMarkdown(map[string]string{"example.com/provider": "v0.2.0"})
	if single != "`example.com/provider@v0.2.0`" {
		t.Fatalf("single = %q", single)
	}
	many := expectedRequirementsMarkdown(map[string]string{
		"example.com/provider": "v0.2.0",
		"example.com/other":    "v1.0.0",
		"example.com/aaa":      "",
	})
	if many != "`example.com/aaa@`<br>`example.com/other@v1.0.0`<br>`example.com/provider@v0.2.0`" {
		t.Fatalf("many = %q, want a deterministic sorted join", many)
	}
}

func TestDepsCovReportingBumpReportJSONAndYAML(t *testing.T) {
	t.Parallel()
	report := depsCovReportingBumpReport()
	raw, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	document := depsCovJSONDocument(t, raw)
	for _, key := range []string{
		"schema_version", "operation", "status", "phase", "progress", "ecosystem", "seed_events",
		"base_ref", "validation_mode", "parallel", "parallel_explicit", "registry_lookups_skipped",
		"fetch_cache_enabled", "excluded_repositories", "held_repositories", "scopes",
		"scope_resolutions", "discovery_skips", "default_branch_fallbacks", "manifest_warnings",
		"ambiguous_modules", "waves",
	} {
		if _, ok := document[key]; !ok {
			t.Fatalf("JSON is missing key %q: %s", key, raw)
		}
	}
	for _, leaked := range []string{"Operation", "SchemaVersion", "Waves", "RegistryLookupsSkipped"} {
		if _, ok := document[leaked]; ok {
			t.Fatalf("JSON leaked the Go field name %q: %s", leaked, raw)
		}
	}
	if document["operation"] != "deps-bump-go-acme-provider-v0-2-0" || document["registry_lookups_skipped"] != true || document["fetch_cache_enabled"] != true {
		t.Fatalf("JSON scalars = %v", document)
	}
	if _, ok := document["verification"]; ok {
		t.Fatalf("empty verification list was emitted despite omitempty: %s", raw)
	}
	progress := depsCovJSONObject(t, document, "progress")
	if progress["wave"] != float64(2) || progress["last_repository"] != "acme/last" {
		t.Fatalf("progress = %+v", progress)
	}
	scopes := depsCovJSONArray(t, document, "scope_resolutions")
	silent, ok := scopes[1].(map[string]any)
	if !ok || silent["dependency"] != "example.com/silent" {
		t.Fatalf("scope resolutions = %+v", scopes)
	}
	if _, ok := silent["version"]; ok {
		t.Fatalf("empty scope version was emitted despite omitempty: %+v", silent)
	}
	waves := depsCovJSONArray(t, document, "waves")
	if len(waves) != 1 {
		t.Fatalf("waves = %+v", waves)
	}
	wave, ok := waves[0].(map[string]any)
	if !ok || wave["validation_mode"] != "full" {
		t.Fatalf("wave = %+v", waves[0])
	}
	releases := depsCovJSONArray(t, wave, "releases")
	first, ok := releases[0].(map[string]any)
	if !ok {
		t.Fatalf("release = %+v", releases[0])
	}
	expected := depsCovJSONObject(t, first, "expected_requirements")
	if expected["example.com/provider"] != "v0.2.0" || expected["example.com/other"] != "v1.0.0" {
		t.Fatalf("expected_requirements = %+v", expected)
	}
	second, ok := releases[1].(map[string]any)
	if !ok {
		t.Fatalf("release = %+v", releases[1])
	}
	if _, ok := second["expected_requirements"]; ok {
		t.Fatalf("empty expected_requirements was emitted despite omitempty: %+v", second)
	}

	yamlRaw, err := report.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	var loaded BumpReport
	if err := yaml.Unmarshal(yamlRaw, &loaded); err != nil {
		t.Fatalf("YAML round trip: %v\n%s", err, yamlRaw)
	}
	if loaded.Operation != report.Operation || loaded.Phase != report.Phase || loaded.RegistryLookupsSkipped != report.RegistryLookupsSkipped ||
		loaded.FetchCacheEnabled != report.FetchCacheEnabled || len(loaded.Waves) != 1 || len(loaded.ScopeResolutions) != 2 ||
		loaded.Progress != report.Progress || !reflect.DeepEqual(loaded.Scopes, report.Scopes) {
		t.Fatalf("loaded bump report = %+v", loaded)
	}
	if !loaded.Waves[0].Refreshes[0].CheckedAt.Equal(report.Waves[0].Refreshes[0].CheckedAt) {
		t.Fatalf("refresh timestamp = %s", loaded.Waves[0].Refreshes[0].CheckedAt)
	}
	if got := loaded.Waves[0].Releases[0].ExpectedRequirements; !reflect.DeepEqual(got, report.Waves[0].Releases[0].ExpectedRequirements) {
		t.Fatalf("expected requirements = %+v", got)
	}
}

func TestDepsCovReportingWriteBumpReports(t *testing.T) {
	t.Parallel()
	report := depsCovReportingBumpReport()
	directory := filepath.Join(t.TempDir(), "nested", "bump")
	if err := WriteBumpReports(directory, report); err != nil {
		t.Fatalf("WriteBumpReports: %v", err)
	}
	if names := depsCovDirNames(t, directory); !reflect.DeepEqual(names, []string{"deps-bump.md", "deps-bump.yaml"}) {
		t.Fatalf("bump report directory = %v, want exactly the Markdown and YAML artifacts", names)
	}
	markdownPath := filepath.Join(directory, "deps-bump.md")
	if got, want := string(depsCovReadFile(t, markdownPath)), report.Markdown(); got != want {
		t.Fatalf("deps-bump.md differs from BumpReport.Markdown():\n%s", got)
	}
	wantYAML, err := report.YAML()
	if err != nil {
		t.Fatalf("BumpReport.YAML: %v", err)
	}
	if got := string(depsCovReadFile(t, filepath.Join(directory, "deps-bump.yaml"))); got != string(wantYAML) {
		t.Fatalf("deps-bump.yaml differs from BumpReport.YAML():\n%s", got)
	}
	info, err := os.Stat(markdownPath)
	if err != nil {
		t.Fatalf("stat deps-bump.md: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("deps-bump.md mode = %v, want 0644", info.Mode().Perm())
	}

	file := filepath.Join(t.TempDir(), "plain-file")
	depsCovWriteFile(t, file, "not a directory")
	if err := WriteBumpReports(filepath.Join(file, "bump"), report); err == nil {
		t.Fatal("a bump report directory nested under a file must fail")
	}

	markdownBlocked := t.TempDir()
	depsCovMkdir(t, filepath.Join(markdownBlocked, "deps-bump.md"))
	if err := WriteBumpReports(markdownBlocked, report); err == nil || !strings.Contains(err.Error(), "deps-bump.md") {
		t.Fatalf("error = %v, want the Markdown write refusal naming deps-bump.md", err)
	}
	if _, statErr := os.Stat(filepath.Join(markdownBlocked, "deps-bump.yaml")); statErr == nil {
		t.Fatal("deps-bump.yaml must not be written after the Markdown write failed")
	}

	yamlBlocked := t.TempDir()
	depsCovMkdir(t, filepath.Join(yamlBlocked, "deps-bump.yaml"))
	if err := WriteBumpReports(yamlBlocked, report); err == nil || !strings.Contains(err.Error(), "deps-bump.yaml") {
		t.Fatalf("error = %v, want the YAML write refusal naming deps-bump.yaml", err)
	}
}

func TestDepsCovReportingLoadBumpReport(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	report := depsCovReportingBumpReport()
	if err := WriteBumpReports(directory, report); err != nil {
		t.Fatalf("WriteBumpReports: %v", err)
	}
	loaded, err := LoadBumpReport(directory)
	if err != nil {
		t.Fatalf("LoadBumpReport: %v", err)
	}
	if loaded.Operation != report.Operation || loaded.Phase != report.Phase || len(loaded.Waves) != 1 || loaded.RegistryLookupsSkipped != report.RegistryLookupsSkipped {
		t.Fatalf("loaded report = %+v", loaded)
	}
	if !loaded.Waves[0].Refreshes[0].CheckedAt.Equal(report.Waves[0].Refreshes[0].CheckedAt) {
		t.Fatalf("refresh timestamp = %s", loaded.Waves[0].Refreshes[0].CheckedAt)
	}

	if _, err := LoadBumpReport(t.TempDir()); err == nil || !os.IsNotExist(err) {
		t.Fatalf("loading a directory with no report must report a missing file, got %v", err)
	}

	corrupt := t.TempDir()
	depsCovWriteFile(t, filepath.Join(corrupt, "deps-bump.yaml"), "schema_version: not-an-integer\n")
	if _, err := LoadBumpReport(corrupt); err == nil {
		t.Fatal("an unparseable persisted report must be refused")
	}
}
