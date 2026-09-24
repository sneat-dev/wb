package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func migCovModfileParsed(t *testing.T, contents string) (*modfile.File, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return migCovParseModfile(t, contents), dir
}

func TestMigCovReplaceGoModuleDropsOldReplacementsAndKeepsRelativePrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	goMod := migCovWriteGoMod(t, dir, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ../old\n")
	replacementRoot := filepath.Join(dir, "sub", "dep")
	if err := os.MkdirAll(replacementRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceGoModule(dir, goMod, "example.com/dep", replacementRoot); err != nil {
		t.Fatalf("replaceGoModule() error = %v", err)
	}
	got := mustReadCampaignFile(t, goMod)
	if !strings.Contains(got, "replace example.com/dep => ./sub/dep") {
		t.Fatalf("go.mod replacement = %s", got)
	}
	if strings.Contains(got, "../old") {
		t.Fatalf("stale replacement survived: %s", got)
	}

	// A versioned old requirement is dropped by its full old spelling.
	versioned := t.TempDir()
	versionedMod := migCovWriteGoMod(t, versioned, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep v1.0.0 => ../pinned\n")
	target := filepath.Join(versioned, "dep")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceGoModule(versioned, versionedMod, "example.com/dep", target); err != nil {
		t.Fatalf("replaceGoModule(versioned) error = %v", err)
	}
	if got := mustReadCampaignFile(t, versionedMod); strings.Contains(got, "../pinned") || !strings.Contains(got, "replace example.com/dep => ./dep") {
		t.Fatalf("versioned replacement = %s", got)
	}
}

func TestMigCovReplaceGoModuleReportsInputFailures(t *testing.T) {
	t.Parallel()
	if err := replaceGoModule(t.TempDir(), filepath.Join(t.TempDir(), "go.mod"), "example.com/dep", t.TempDir()); err == nil {
		t.Fatal("replaceGoModule(missing go.mod) succeeded")
	}
	brokenDir := t.TempDir()
	broken := migCovWriteGoMod(t, brokenDir, "this is not a go.mod\n")
	if err := replaceGoModule(brokenDir, broken, "example.com/dep", t.TempDir()); err == nil {
		t.Fatal("replaceGoModule(unparseable go.mod) succeeded")
	}

	// A relative module root cannot be made relative to an absolute one.
	mixedDir := t.TempDir()
	mixed := migCovWriteGoMod(t, mixedDir, "module example.com/app\n\ngo 1.24\n")
	if err := replaceGoModule("relative-module-root", mixed, "example.com/dep", t.TempDir()); err == nil {
		t.Fatal("replaceGoModule(mixed relative/absolute roots) succeeded")
	}
}

func TestMigCovDropCampaignReplaceRemovesOnlyTheNamedReplacement(t *testing.T) {
	t.Parallel()
	contents := "module example.com/app\n\ngo 1.24\n\nrequire (\n\texample.com/dep v1.0.0\n\texample.com/other v1.0.0\n)\n\nreplace example.com/other => ../other\n\nreplace example.com/dep v1.0.0 => ../dep\n"
	parsed, dir := migCovModfileParsed(t, contents)
	if err := dropCampaignReplace(dir, parsed, "example.com/dep"); err != nil {
		t.Fatalf("dropCampaignReplace() error = %v", err)
	}
	got := mustReadCampaignFile(t, filepath.Join(dir, "go.mod"))
	if strings.Contains(got, "example.com/dep") && strings.Contains(got, "=> ../dep") {
		t.Fatalf("campaign replacement survived: %s", got)
	}
	if !strings.Contains(got, "../other") {
		t.Fatalf("unrelated replacement was removed: %s", got)
	}

	if err := dropCampaignReplace(t.TempDir(), parsed, "example.com/dep"); err == nil {
		t.Fatal("dropCampaignReplace() without a go.mod succeeded")
	}
}

func TestMigCovHasCampaignReplaceDistinguishesCampaignFromForeignRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	campaignRoot := filepath.Join(dir, "dep")
	contents := "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ./dep\n"
	parsed, _ := migCovModfileParsed(t, contents)
	ok, err := hasCampaignReplace(dir, parsed, "example.com/dep", campaignRoot)
	if err != nil || !ok {
		t.Fatalf("hasCampaignReplace(campaign) = %v, %v", ok, err)
	}

	foreign := "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ../elsewhere\n"
	parsed, _ = migCovModfileParsed(t, foreign)
	if _, err := hasCampaignReplace(dir, parsed, "example.com/dep", campaignRoot); err == nil ||
		!strings.Contains(err.Error(), "non-campaign replacement") {
		t.Fatalf("hasCampaignReplace(foreign) = %v", err)
	}

	ok, err = hasCampaignReplace(dir, parsed, "example.com/absent", campaignRoot)
	if err != nil || ok {
		t.Fatalf("hasCampaignReplace(absent) = %v, %v", ok, err)
	}
}

func TestMigCovUpdateGoModuleReportsInputFailures(t *testing.T) {
	t.Parallel()
	if _, err := updateGoModule(t.TempDir(), Spec{}, "example.com/app", nil); err == nil {
		t.Fatal("updateGoModule(missing go.mod) succeeded")
	}

	brokenDir := t.TempDir()
	migCovWriteGoMod(t, brokenDir, "this is not a go.mod\n")
	if _, err := updateGoModule(brokenDir, Spec{}, "example.com/app", nil); err == nil {
		t.Fatal("updateGoModule(unparseable go.mod) succeeded")
	}

	editDir := t.TempDir()
	migCovWriteGoMod(t, editDir, "module example.com/app\n\ngo 1.24\n")
	spec := Spec{GoModuleRequires: []GoModuleRequire{{Path: "example.com/bad path", Version: "v1.0.0"}}}
	if _, err := updateGoModule(editDir, spec, "example.com/app", nil); err == nil {
		t.Fatal("updateGoModule(invalid requirement) succeeded")
	}

	replaceDir := t.TempDir()
	migCovWriteGoMod(t, replaceDir, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	if _, err := updateGoModule(replaceDir, Spec{}, "example.com/app", map[string]string{"example.com/dep": "relative-dep"}); err == nil {
		t.Fatal("updateGoModule(mixed relative replacement root) succeeded")
	}
}

func TestMigCovUpdateGoModuleReportsTidyFailure(t *testing.T) {
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	dir := t.TempDir()
	migCovWriteGoMod(t, dir, "module example.com/app\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(dir, "app.go"), "package app\n\nimport _ \"example.com/absent-module\"\n")
	if _, err := updateGoModule(dir, Spec{}, "example.com/app", nil); err == nil {
		t.Fatal("updateGoModule() succeeded while a required module was unresolvable")
	}
}

func TestMigCovFinalizeGoModuleRemovesCampaignReplacements(t *testing.T) {
	t.Parallel()
	if _, err := finalizeGoModule(t.TempDir(), Spec{}, "example.com/app", nil, nil); err == nil {
		t.Fatal("finalizeGoModule(missing go.mod) succeeded")
	}
	broken := t.TempDir()
	migCovWriteGoMod(t, broken, "this is not a go.mod\n")
	if _, err := finalizeGoModule(broken, Spec{}, "example.com/app", nil, nil); err == nil {
		t.Fatal("finalizeGoModule(unparseable go.mod) succeeded")
	}

	// With no campaign replacements the manifest is only audited, not edited.
	plain := t.TempDir()
	migCovWriteGoMod(t, plain, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	update, err := finalizeGoModule(plain, Spec{GoModuleReleases: []GoModuleRelease{{Path: "example.com/dep", Version: "v1.2.0"}}}, "example.com/app", nil, nil)
	if err != nil {
		t.Fatalf("finalizeGoModule(plain) = %v", err)
	}
	if update.Changed {
		t.Fatalf("finalizeGoModule(plain) changed the manifest: %d decisions", len(update.DependencyDecisions))
	}

	// A campaign replacement without a published release is refused.
	parent := t.TempDir()
	appRoot := filepath.Join(parent, "app")
	migCovWriteGoMod(t, appRoot, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	depRoot := filepath.Join(parent, "dep")
	migCovWriteGoMod(t, depRoot, "module example.com/dep\n\ngo 1.24\n")
	_, err = finalizeGoModule(appRoot, Spec{}, "example.com/app", map[string]string{"example.com/dep": depRoot}, nil)
	if err == nil || !strings.Contains(err.Error(), "add go_module_release") {
		t.Fatalf("finalizeGoModule(missing release) = %v", err)
	}

	// A release override drops the worktree replacement before publication.
	overrideRoot := filepath.Join(parent, "app-override")
	migCovWriteGoMod(t, overrideRoot, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	update, err = finalizeGoModule(
		overrideRoot, Spec{}, "example.com/app",
		map[string]string{"example.com/dep": depRoot},
		map[string]string{"example.com/dep": "v1.2.3"},
	)
	if err != nil {
		t.Fatalf("finalizeGoModule(override) = %v", err)
	}
	if !update.Changed {
		t.Fatal("finalizeGoModule(override) reported no manifest change")
	}
	if got := mustReadCampaignFile(t, filepath.Join(overrideRoot, "go.mod")); strings.Contains(got, "../dep") {
		t.Fatalf("campaign replacement survived publication: %s", got)
	}
}

func TestMigCovPreflightPublishedReleasesCoversEveryGate(t *testing.T) {
	t.Parallel()
	if _, err := preflightPublishedReleases(t.TempDir(), Spec{}, "example.com/app", nil, nil); err == nil {
		t.Fatal("preflightPublishedReleases(missing go.mod) succeeded")
	}
	broken := t.TempDir()
	migCovWriteGoMod(t, broken, "this is not a go.mod\n")
	if _, err := preflightPublishedReleases(broken, Spec{}, "example.com/app", nil, nil); err == nil {
		t.Fatal("preflightPublishedReleases(unparseable go.mod) succeeded")
	}

	// A versioned replacement is none of preflight's business.
	pinned := t.TempDir()
	migCovWriteGoMod(t, pinned, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep v1.0.0 => example.com/dep v1.2.0\n")
	bootstrap, err := preflightPublishedReleases(
		pinned,
		Spec{
			GoModuleRequires: []GoModuleRequire{{Path: "example.com/other", Version: "v1.0.0"}},
			GoModuleReleases: []GoModuleRelease{{Path: "example.com/dep", Version: "v1.2.0"}},
		},
		"example.com/app", nil, nil,
	)
	if err != nil || len(bootstrap) != 0 {
		t.Fatalf("preflightPublishedReleases(pinned) = %v, %v", bootstrap, err)
	}

	// A direct campaign dependency with no release blocks unless the cycle
	// bootstrap explicitly allows it.
	appRoot := t.TempDir()
	migCovWriteGoMod(t, appRoot, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n")
	roots := map[string]string{"example.com/dep": t.TempDir()}
	if _, err := preflightPublishedReleases(appRoot, Spec{}, "example.com/app", roots, nil); err == nil ||
		!strings.Contains(err.Error(), "add go_module_release") {
		t.Fatalf("preflightPublishedReleases(missing release) = %v", err)
	}
	bootstrap, err = preflightPublishedReleases(appRoot, Spec{}, "example.com/app", roots, map[string]bool{"example.com/dep": true})
	if err != nil || !bootstrap["example.com/dep"] {
		t.Fatalf("preflightPublishedReleases(cycle bootstrap) = %v, %v", bootstrap, err)
	}

	// A replacement that points somewhere other than the campaign worktree is
	// refused before anything is changed.
	foreign := t.TempDir()
	migCovWriteGoMod(t, foreign, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../not-the-campaign\n")
	_, err = preflightPublishedReleases(foreign, Spec{}, "example.com/app", map[string]string{"example.com/dep": filepath.Join(t.TempDir(), "campaign")}, nil)
	if err == nil || !strings.Contains(err.Error(), "non-campaign replacement") {
		t.Fatalf("preflightPublishedReleases(foreign replacement) = %v", err)
	}
}

func TestMigCovVerifyGoModuleRunsTheConfiguredCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if results := verifyGoModule(dir, VerifyNone); len(results) != 0 {
		t.Fatalf("verifyGoModule(none) = %+v, want no commands", results)
	}
	results := verifyGoModule(dir, VerifyTest)
	if len(results) != 1 || results[0].Command != "go test ./..." {
		t.Fatalf("verifyGoModule(test) = %+v", results)
	}
	if results[0].Passed {
		t.Fatal("verifyGoModule(test) passed outside a Go module")
	}
	if results[0].Detail == "" {
		t.Fatal("verifyGoModule(test) did not record the command output")
	}
}

func TestMigCovOpenCampaignPRReusesOpenPullRequest(t *testing.T) {
	worktree := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	writeCampaignFile(t, filepath.Join(binDir, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1 $2" = "pr list" ]; then
	printf '%s\n' 'https://github.com/acme/example/pull/7'
	exit 0
fi
exit 1
`)
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	url, err := openCampaignPR(&campaignRepository{
		repository: "github.com/acme/example",
		worktree:   worktree,
		branch:     "wb/migrate/example",
		ref:        "main",
	}, Spec{ID: "example", Title: "Example migration"})
	if err != nil {
		t.Fatalf("openCampaignPR() error = %v", err)
	}
	if url != "https://github.com/acme/example/pull/7" {
		t.Fatalf("pull request URL = %q", url)
	}
	if log := mustReadCampaignFile(t, logPath); strings.Contains(log, "pr create") {
		t.Fatalf("a duplicate pull request was created:\n%s", log)
	}
}

func TestMigCovOpenCampaignPRReportsCreationFailures(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	writeCampaignFile(t, filepath.Join(binDir, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1 $2" = "pr list" ]; then
	exit 0
fi
printf '%s\n' "$GH_CREATE_STDERR" >&2
exit "${GH_CREATE_EXIT:-1}"
`)
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := &campaignRepository{repository: "github.com/acme/example", worktree: t.TempDir(), branch: "wb/migrate/example", ref: "main"}
	t.Setenv("GH_CREATE_EXIT", "1")
	t.Setenv("GH_CREATE_STDERR", "HTTP 422: Validation Failed")
	if _, err := openCampaignPR(repo, Spec{ID: "example"}); err == nil {
		t.Fatal("openCampaignPR() ignored a failed gh pr create")
	}

	// An empty URL is not a pull request.
	t.Setenv("GH_CREATE_EXIT", "0")
	t.Setenv("GH_CREATE_STDERR", "")
	if _, err := openCampaignPR(repo, Spec{ID: "example"}); err == nil ||
		!strings.Contains(err.Error(), "returned no pull request URL") {
		t.Fatalf("openCampaignPR(empty output) = %v", err)
	}
}

func TestMigCovRequiredChecksGreenRejectsUnusablePullRequests(t *testing.T) {
	if _, _, err := requiredChecksGreen(t.TempDir(), "not-a-pull-request"); err == nil ||
		!strings.Contains(err.Error(), "has no /pull/ segment") {
		t.Fatalf("requiredChecksGreen(invalid URL) = %v", err)
	}

	binDir := t.TempDir()
	writeCampaignFile(t, filepath.Join(binDir, "gh"), "#!/bin/sh\nprintf '%s\\n' 'HTTP 404: Not Found' >&2\nexit 1\n")
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, _, err := requiredChecksGreen(t.TempDir(), "https://github.com/acme/example/pull/9"); err == nil {
		t.Fatal("requiredChecksGreen() ignored an unreadable pull request")
	}
}

func TestMigCovCampaignReportMarkdownRendersEverySection(t *testing.T) {
	t.Parallel()
	emptyChanges := []string{}
	changedFiles := []string{"a.go", "b.go"}
	changedPass := 3
	report := CampaignReport{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: "example", Title: "Example migration.", Format: MigrationFormatV1},
		Status:        "applied",
		SourceRoot:    "/source",
		GitHubDir:     "/github",
		BaseRef:       "main",
		Verification:  VerifyFull,
		Parallel:      2,
		Repositories: []CampaignRepositoryReport{
			{
				Repository: "github.com/acme/bare", WorktreeDir: "/worktrees/bare", Ref: "main",
				ChangedFiles: &emptyChanges,
				Modules: []CampaignModuleReport{
					{Path: "github.com/acme/bare", Status: "provided", PlanState: "not_applicable"},
					{Path: "github.com/acme/deferred", Status: "planned", PlanState: "deferred"},
				},
			},
			{
				Repository: "github.com/acme/full", WorktreeDir: "/worktrees/full", Ref: "main",
				ChangedFiles: &changedFiles,
				PR:           "https://github.com/acme/full/pull/1",
				Pushed:       true,
				Merged:       true,
				RequiredChecks: []RemoteCheck{
					{Name: "ci", Bucket: "pass"},
				},
				Modules: []CampaignModuleReport{{
					Path: "github.com/acme/full", Status: "applied", PlanState: "complete",
					ChangedFiles:        &changedPass,
					ManifestChanged:     true,
					PublishableManifest: true,
					MigrationReportPath: "/reports/full",
					DependencyDecisions: []GoDependencyDecision{{
						Phase: "publishable", Path: "github.com/acme/dep",
						RequiredAtCheck: false, VersionAtCheck: "v1.0.0",
						RequiredAfter: false, VersionAfter: "v1.0.0",
						VersionAction: "updated", ReplacementAction: "removed", TargetVersion: "v1.2.0",
						Reason: "configured target version applied",
					}},
					Verifications: []VerificationResult{{
						Command: "go test ./...", Passed: false, Detail: "boom",
					}},
				}},
			},
		},
	}
	markdown := report.Markdown()
	for _, want := range []string{
		"# WB hierarchical migration: example",
		"Example migration.",
		"github.com/acme/bare",
		"No files differ from the campaign base ref.",
		"not applicable",
		"unknown (worktree not created)",
		"[PR](https://github.com/acme/full/pull/1)",
		"- [a.go](file://",
		"`git -C '/worktrees/full' diff 'origin/main'`",
		"publishable manifest: `true`",
		"[migration report](file://",
		"Dependency `github.com/acme/dep` (`publishable`)",
		"checked `not required`",
		"after `not required`",
		"target `v1.2.0`",
		"`go test ./...`: `false` — boom",
		"Required GitHub checks",
		"`ci`: `pass`",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestMigCovCampaignReportJSONAndWriteCampaignReports(t *testing.T) {
	t.Parallel()
	report := CampaignReport{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: "example", Format: MigrationFormatV1},
		Status:        "planned",
		Repositories:  []CampaignRepositoryReport{},
	}
	raw, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v\n%s", err, raw)
	}
	if _, ok := decoded["schema_version"]; !ok {
		t.Errorf("JSON lacks schema_version: %s", raw)
	}

	dir := filepath.Join(t.TempDir(), "reports")
	if err := WriteCampaignReports(dir, report); err != nil {
		t.Fatalf("WriteCampaignReports() error = %v", err)
	}
	for _, name := range []string{"campaign.md", "campaign.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if got := mustReadCampaignFile(t, filepath.Join(dir, "campaign.md")); !strings.Contains(got, "example") {
		t.Errorf("campaign.md = %s", got)
	}

	// A file where a directory is expected is refused.
	filePath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCampaignReports(filePath, report); err == nil {
		t.Fatal("WriteCampaignReports() accepted a file as its directory")
	}

	// A directory where campaign.md is expected is refused.
	blockedMarkdown := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blockedMarkdown, "campaign.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteCampaignReports(blockedMarkdown, report); err == nil {
		t.Fatal("WriteCampaignReports() accepted a directory named campaign.md")
	}

	// A directory where campaign.yaml is expected is refused after the
	// Markdown file is written.
	blockedYAML := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blockedYAML, "campaign.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteCampaignReports(blockedYAML, report); err == nil {
		t.Fatal("WriteCampaignReports() accepted a directory named campaign.yaml")
	}
}
