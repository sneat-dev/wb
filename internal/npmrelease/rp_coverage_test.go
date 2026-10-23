package npmrelease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rpCovCommandRunner is the recording command seam used by the coverage tests.
// It can answer from a scripted step list or from a callback, and it records
// the exact argv of every invocation so command construction is assertable.
type rpCovCommandRunner struct {
	calls []string
	dirs  []string
	steps []CommandResult
	fn    func(dir string, args []string) CommandResult
}

func (r *rpCovCommandRunner) Run(_ context.Context, dir string, args ...string) CommandResult {
	r.calls = append(r.calls, strings.Join(args, " "))
	r.dirs = append(r.dirs, dir)
	if r.fn != nil {
		return r.fn(dir, args)
	}
	if len(r.steps) == 0 {
		return CommandResult{Code: 2, Err: errors.New("unexpected command")}
	}
	result := r.steps[0]
	r.steps = r.steps[1:]
	return result
}

// rpCovContextRunner records the context it was handed so tests can prove the
// per-command timeout budget is applied (or deliberately skipped).
type rpCovContextRunner struct {
	sawContext context.Context
	seen       int
}

func (r *rpCovContextRunner) Run(ctx context.Context, _ string, _ ...string) CommandResult {
	r.sawContext = ctx
	r.seen++
	return CommandResult{}
}

func TestRPCovIsSHAAcceptsOnlyFortyHexCharacters(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		want  bool
	}{
		{releaseHead, true},
		{strings.ToUpper(releaseHead), true},
		{"abc", false},
		{strings.Repeat("g", 40), false},
		{releaseHead[:39] + "z", false},
		{"", false},
	} {
		if got := isSHA(test.value); got != test.want {
			t.Errorf("isSHA(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestRPCovNpmVersionValidRejectsEmptyAndNonSemver(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		version string
		want    bool
	}{
		{"", false},
		{"0.1.0", true},
		{"v0.1.0", true},
		{"not-a-version", false},
	} {
		if got := npmVersionValid(test.version); got != test.want {
			t.Errorf("npmVersionValid(%q) = %t, want %t", test.version, got, test.want)
		}
	}
}

func TestRPCovCloneInputsAndFingerprintTreatEmptyInputsAsAbsent(t *testing.T) {
	t.Parallel()
	if cloneInputs(nil) != nil || cloneInputs(map[string]string{}) != nil {
		t.Fatal("empty workflow inputs must clone to nil, not an allocated empty map")
	}
	source := map[string]string{"package": "runtime"}
	cloned := cloneInputs(source)
	if cloned["package"] != "runtime" {
		t.Fatalf("cloneInputs = %+v", cloned)
	}
	cloned["package"] = "mutated"
	if source["package"] != "runtime" {
		t.Fatal("cloneInputs handed back the caller's own map")
	}
	if workflowInputFingerprint(nil) != "" {
		t.Fatal("absent inputs must fingerprint as the empty string")
	}
	if workflowInputFingerprint(map[string]string{"a": "1"}) == "" {
		t.Fatal("present inputs must fingerprint to a non-empty digest")
	}
}

func TestRPCovNowUsesTheInjectedClockAndFallsBackToWallClock(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := now(Options{Now: func() time.Time { return fixed }}); !got.Equal(fixed) {
		t.Fatalf("now(injected) = %s, want %s", got, fixed)
	}
	wall := now(Options{})
	if wall.Location() != time.UTC {
		t.Fatalf("now() location = %s, want UTC", wall.Location())
	}
	if delta := time.Since(wall); delta > time.Minute || delta < -time.Minute {
		t.Fatalf("now() = %s, want approximately the current time", wall)
	}
}

func TestRPCovCommandErrorFallsBackToExitCodeWithoutOutput(t *testing.T) {
	t.Parallel()
	err := commandError("dispatch release workflow", CommandResult{Code: 7})
	if err == nil || err.Error() != "dispatch release workflow: exit code 7" {
		t.Fatalf("commandError = %v, want the exit-code fallback", err)
	}
}

func TestRPCovUseSharedGitHubObserverSelectsTheSharedTransport(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		runner CommandRunner
		want   bool
	}{
		{"absent", nil, true},
		{"value", OSCommandRunner{}, true},
		{"pointer", &OSCommandRunner{}, true},
		{"custom", &rpCovCommandRunner{}, false},
	} {
		if got := useSharedGitHubObserver(test.runner); got != test.want {
			t.Errorf("%s: useSharedGitHubObserver = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestRPCovRunExternalAppliesTimeoutOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	withoutTimeout := &rpCovContextRunner{}
	// The nil context is exactly what this test asserts is defaulted.
	//nolint:staticcheck // SA1012: passing nil is the behaviour under test.
	runExternal(nil, Options{Runner: withoutTimeout}, "gh", "api")
	if withoutTimeout.sawContext == nil {
		t.Fatal("runExternal passed a nil context through to the runner")
	}
	if _, ok := withoutTimeout.sawContext.Deadline(); ok {
		t.Fatal("runExternal imposed a deadline without a configured timeout")
	}

	withTimeout := &rpCovContextRunner{}
	runExternal(context.Background(), Options{Runner: withTimeout, Timeout: time.Minute}, "gh", "api")
	if _, ok := withTimeout.sawContext.Deadline(); !ok {
		t.Fatal("runExternal did not impose the configured per-command timeout")
	}
}

func TestRPCovValidateOptionsRejectsConflictingAndMalformedSelections(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options Options
		want    string
	}{
		{"apply and dry-run", Options{Apply: true, DryRun: true}, "--apply and --dry-run cannot be used together"},
		{"resume without apply", Options{Resume: true}, "--resume requires --apply"},
		{"negative timeout", Options{Timeout: -time.Second}, "timeout and poll interval must not be negative"},
		{"negative poll interval", Options{PollInterval: -time.Second}, "timeout and poll interval must not be negative"},
		{"scheme-less registry", Options{Registry: "registry.example"}, "invalid npm registry URL"},
		{"host-less registry", Options{Registry: "https://"}, "invalid npm registry URL"},
	} {
		err := ValidateOptions(test.options)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: ValidateOptions error = %v, want %q", test.name, err, test.want)
		}
	}
	if err := ValidateOptions(Options{Apply: true, Registry: "http://registry.example"}); err != nil {
		t.Fatalf("valid plain-http registry rejected: %v", err)
	}
}

func TestRPCovValidateReleaseRejectsUnsafeIdentifiers(t *testing.T) {
	t.Parallel()
	mutate := func(change func(*Release)) Release {
		release := testRelease()
		change(&release)
		return release
	}
	for _, test := range []struct {
		name    string
		release Release
		want    string
	}{
		{"unqualified repository", mutate(func(r *Release) { r.Repository = "assetus" }), "invalid GitHub repository"},
		{"empty workflow", mutate(func(r *Release) { r.Workflow = "" }), "invalid release workflow"},
		{"parent-escaping workflow", mutate(func(r *Release) { r.Workflow = "../publish.yml" }), "invalid release workflow"},
		{"absolute workflow", mutate(func(r *Release) { r.Workflow = "/publish.yml" }), "invalid release workflow"},
		{"non-yaml workflow", mutate(func(r *Release) { r.Workflow = "publish.txt" }), "must end in .yml or .yaml"},
		{"invalid package", mutate(func(r *Release) { r.Package = "@sneat" }), "invalid npm package"},
		{"v-prefixed version", mutate(func(r *Release) { r.Version = "v0.1.0" }), "invalid npm release version"},
		{"whitespace ref", mutate(func(r *Release) { r.Ref = "ma in" }), "release ref must be a non-empty ref without whitespace"},
		{"empty ref", mutate(func(r *Release) { r.Ref = " " }), "release ref must be a non-empty ref without whitespace"},
		{"colon input name", mutate(func(r *Release) { r.Inputs = map[string]string{"package:name": "x"} }), "invalid workflow input name"},
		{"equals input name", mutate(func(r *Release) { r.Inputs = map[string]string{"package=name": "x"} }), "invalid workflow input name"},
		{"blank input name", mutate(func(r *Release) { r.Inputs = map[string]string{"  ": "x"} }), "invalid workflow input name"},
	} {
		err := ValidateRelease(test.release)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: ValidateRelease error = %v, want %q", test.name, err, test.want)
		}
	}
	if err := ValidateRelease(testRelease()); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}
}

func TestRPCovEventsForRequiresExactPublishedRegistryEvidence(t *testing.T) {
	t.Parallel()
	checkedAt := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	published := Receipt{Release: testRelease(), Status: StatusPublished, RegistryVersion: "0.1.0", RegistryCheckedAt: checkedAt}
	report := Report{Status: StatusPublished, Releases: []Receipt{published}}
	events, err := EventsFor(report)
	if err != nil || len(events) != 1 || events[0].Dependency != "@sneat/extension-assetus" || events[0].CheckedAt != checkedAt {
		t.Fatalf("EventsFor = %+v, err=%v", events, err)
	}
	for _, broken := range []Receipt{
		{Release: testRelease(), Status: StatusRunning, RegistryVersion: "0.1.0", RegistryCheckedAt: checkedAt},
		{Release: testRelease(), Status: StatusPublished, RegistryVersion: "0.1.1", RegistryCheckedAt: checkedAt},
		{Release: testRelease(), Status: StatusPublished, RegistryVersion: "0.1.0"},
	} {
		if _, err := EventsFor(Report{Status: StatusPublished, Releases: []Receipt{broken}}); err == nil ||
			!strings.Contains(err.Error(), "lacks exact registry evidence") {
			t.Errorf("EventsFor(%+v) error = %v, want missing-evidence refusal", broken, err)
		}
	}
}

func TestRPCovReportExistsRecognizesEitherArtifact(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if exists, err := ReportExists(directory); err != nil || exists {
		t.Fatalf("ReportExists(empty) = %t, %v", exists, err)
	}
	for _, name := range []string{"npm-publish.yaml", "npm-publish.json"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		exists, err := ReportExists(dir)
		if err != nil || !exists {
			t.Fatalf("ReportExists with only %s = %t, %v", name, exists, err)
		}
	}
	// A path component that is a regular file makes Stat fail with something
	// other than NotExist, which must be reported rather than read as absent.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportExists(filepath.Join(blocker, "nested")); err == nil {
		t.Fatal("ReportExists treated an unreadable path as an absent report")
	}
}

func TestRPCovLoadReportRejectsBrokenArtifacts(t *testing.T) {
	t.Parallel()
	if _, err := LoadReport(t.TempDir()); err == nil {
		t.Fatal("LoadReport accepted a directory with no report")
	}

	valid := t.TempDir()
	if err := WriteReport(valid, plannedReport([]Release{testRelease()})); err != nil {
		t.Fatal(err)
	}

	// YAML that is not a mapping cannot decode.
	brokenYAML := t.TempDir()
	if err := WriteReport(brokenYAML, plannedReport([]Release{testRelease()})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenYAML, "npm-publish.yaml"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(brokenYAML); err == nil {
		t.Fatal("LoadReport accepted an undecodable YAML report")
	}

	// Canonical YAML with no matching JSON generation.
	noJSON := t.TempDir()
	if err := WriteReport(noJSON, plannedReport([]Release{testRelease()})); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(noJSON, "npm-publish.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(noJSON); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("LoadReport error = %v, want the incomplete-generation refusal", err)
	}

	// JSON that exists but cannot be read as a report: here it is a directory.
	unreadableJSON := t.TempDir()
	if err := WriteReport(unreadableJSON, plannedReport([]Release{testRelease()})); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(unreadableJSON, "npm-publish.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(unreadableJSON, "npm-publish.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(unreadableJSON); err == nil {
		t.Fatal("LoadReport accepted a JSON path it cannot read")
	}

	// JSON that decodes as YAML but is not a report.
	undecodableJSON := t.TempDir()
	if err := WriteReport(undecodableJSON, plannedReport([]Release{testRelease()})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(undecodableJSON, "npm-publish.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(undecodableJSON); err == nil || !strings.Contains(err.Error(), "decode npm publication JSON report") {
		t.Fatalf("LoadReport error = %v, want the JSON decode refusal", err)
	}
}

func TestRPCovWriteReportReportsUnusableDirectoriesAndAtomicReplacementFailures(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(filepath.Join(blocker, "reports"), plannedReport([]Release{testRelease()})); err == nil {
		t.Fatal("WriteReport created a report directory under a regular file")
	}

	// The JSON target is a non-empty directory, so the atomic rename cannot
	// publish it; WriteReport must fail rather than leave a half-written pair.
	directory := t.TempDir()
	occupied := filepath.Join(directory, "npm-publish.json")
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(directory, plannedReport([]Release{testRelease()})); err == nil {
		t.Fatal("WriteReport reported success after the atomic replacement failed")
	}
	if _, err := os.Stat(filepath.Join(directory, "npm-publish.yaml")); !os.IsNotExist(err) {
		t.Fatal("YAML advanced even though the JSON generation never published")
	}
}

func TestRPCovWriteAtomicPublishesAndCleansUpTemporaryFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "value.txt")
	if err := writeAtomic(target, []byte("hello"), 0o640); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "hello" {
		t.Fatalf("published contents = %q, err=%v", contents, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("published mode = %04o, want 0640", info.Mode().Perm())
	}

	// A failed rename must leave no temporary file behind: the defer has to
	// remove the staged copy when the atomic publish does not happen.
	occupied := filepath.Join(directory, "occupied")
	if err := os.MkdirAll(filepath.Join(occupied, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(occupied, []byte("x"), 0o600); err == nil {
		t.Fatal("writeAtomic replaced a non-empty directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".npm-publish-") {
			t.Fatalf("failed writeAtomic left a staged temporary file: %s", entry.Name())
		}
	}
	if err := writeAtomic(filepath.Join(directory, "missing", "value.txt"), []byte("x"), 0o600); err == nil {
		t.Fatal("writeAtomic staged a file in a directory that does not exist")
	}
}

func TestRPCovValidatePreviousRejectsEveryMismatchedResumeReport(t *testing.T) {
	t.Parallel()
	normalized, err := Normalize([]Release{testRelease()}, "main")
	if err != nil {
		t.Fatal(err)
	}
	valid := plannedReport(normalized)

	wrongSchema := valid
	wrongSchema.SchemaVersion = SchemaVersion + 1
	if _, err := validatePrevious(wrongSchema, normalized); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("schema mismatch error = %v", err)
	}

	wrongOperation := valid
	wrongOperation.Operation = "npm-publish-something-else"
	if _, err := validatePrevious(wrongOperation, normalized); err == nil || !strings.Contains(err.Error(), "does not match the requested") {
		t.Fatalf("operation mismatch error = %v", err)
	}

	wrongCount := valid
	wrongCount.Releases = nil
	if _, err := validatePrevious(wrongCount, normalized); err == nil || !strings.Contains(err.Error(), "release count does not match") {
		t.Fatalf("release-count mismatch error = %v", err)
	}

	other := testRelease()
	other.Package = "@sneat/extension-other"
	otherNormalized, err := Normalize([]Release{testRelease(), other}, "main")
	if err != nil {
		t.Fatal(err)
	}
	duplicated := plannedReport(otherNormalized)
	duplicated.Releases[1] = duplicated.Releases[0]
	if _, err := validatePrevious(duplicated, otherNormalized); err == nil || !strings.Contains(err.Error(), "duplicate release tuple") {
		t.Fatalf("duplicate tuple error = %v", err)
	}

	incomplete := plannedReport(normalized)
	incomplete.Releases[0].DispatchAt = time.Now().UTC()
	if _, err := validatePrevious(incomplete, normalized); err == nil || !strings.Contains(err.Error(), "lacks its durable pre-dispatch baseline") {
		t.Fatalf("missing-baseline error = %v", err)
	}

	ordered, err := validatePrevious(valid, normalized)
	if err != nil || len(ordered) != 1 || ordered[0].Package != testRelease().Package {
		t.Fatalf("valid resume report = %+v, err=%v", ordered, err)
	}
}

func TestRPCovRunRejectsInvalidOptionCombinationsAndMissingResumeReport(t *testing.T) {
	t.Parallel()
	if _, err := Run(context.Background(), []Release{testRelease()}, Options{Apply: true, DryRun: true}); err == nil ||
		!strings.Contains(err.Error(), "--apply and --dry-run") {
		t.Fatalf("conflicting-option error = %v", err)
	}
	if _, err := Run(context.Background(), []Release{testRelease()}, Options{Apply: true, Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "requires a persisted npm publication report") {
		t.Fatalf("missing-previous error = %v", err)
	}
	stop := errors.New("persistence unavailable")
	if _, err := Run(context.Background(), []Release{testRelease()}, Options{
		Apply: true, Persist: func(Report) error { return stop },
	}); !errors.Is(err, stop) {
		t.Fatalf("pre-apply persistence error = %v, want %v", err, stop)
	}
}

func TestRPCovProcessReceiptRefusesIncompletePersistedDispatchState(t *testing.T) {
	t.Parallel()
	receipt := &Receipt{
		Release:    testRelease(),
		Status:     StatusDispatchUnknown,
		DispatchAt: time.Now().UTC(),
	}
	persistCalls := 0
	err := processReceipt(context.Background(), receipt, Options{Runner: &rpCovCommandRunner{}}, func() error {
		persistCalls++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "lacks an exact head or pre-dispatch baseline") {
		t.Fatalf("error = %v, want the ambiguous-dispatch refusal", err)
	}
	if persistCalls != 0 {
		t.Fatalf("persist calls = %d, want none for a refused dispatch state", persistCalls)
	}
	if receipt.Reason == "" {
		t.Fatal("refusal did not explain itself on the receipt")
	}
}

func TestRPCovProcessReceiptRefusesARunWhoseHeadDiffersFromTheDispatchedHead(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Truncate(time.Second)
	otherHead := "fedcba9876543210fedcba9876543210fedcba98"
	receipt := &Receipt{
		Release: testRelease(), Status: StatusRunning,
		HeadSHA: releaseHead, DispatchAt: created, DispatchBaselineAt: created,
		RunID: "123", RunHeadSHA: otherHead,
	}
	err := processReceipt(context.Background(), receipt, Options{Runner: &rpCovCommandRunner{}}, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "does not match dispatched head") {
		t.Fatalf("error = %v, want the head-mismatch refusal", err)
	}
	if receipt.Status != StatusFailed {
		t.Fatalf("receipt status = %q, want failed", receipt.Status)
	}
}

func TestRPCovProcessReceiptReportsPersistFailureAfterLocatingTheRun(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Truncate(time.Second)
	run := workflowRunFixture("123", "queued", "", created.Add(time.Second))
	runner := &rpCovCommandRunner{steps: []CommandResult{{Output: workflowRunList(run)}}}
	receipt := &Receipt{
		Release: testRelease(), Status: StatusAwaitingRun,
		HeadSHA: releaseHead, DispatchAt: created, DispatchBaselineAt: created,
	}
	persistErr := errors.New("disk full")
	err := processReceipt(context.Background(), receipt, Options{Runner: runner}, func() error { return persistErr })
	if !errors.Is(err, persistErr) {
		t.Fatalf("error = %v, want the persistence failure", err)
	}
	if receipt.RunID != "123" {
		t.Fatalf("located run was not recorded before the persistence failure: %+v", receipt)
	}
}

func TestRPCovProcessReceiptReportsPersistFailureWhenDispatchFails(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Truncate(time.Second)
	dispatchErr := errors.New("workflow dispatch rejected")
	runner := &rpCovCommandRunner{steps: []CommandResult{
		{Output: releaseHead},
		{Output: `[]`},
		{Code: 1, Err: dispatchErr, Output: "boom"},
	}}
	receipt := &Receipt{Release: testRelease(), Status: StatusPlanned}
	persistCalls := 0
	err := processReceipt(context.Background(), receipt, Options{Runner: runner, Now: func() time.Time { return created }}, func() error {
		persistCalls++
		if persistCalls >= 2 {
			return errors.New("disk full")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "persist dispatch failure state") {
		t.Fatalf("error = %v, want the joined persistence failure", err)
	}
	if !strings.Contains(err.Error(), "dispatch release workflow: boom") {
		t.Fatalf("error = %v, want the dispatch failure retained", err)
	}
	if receipt.Status != StatusDispatchFailed {
		t.Fatalf("receipt status = %q, want dispatch_failed", receipt.Status)
	}
}

func TestRPCovLocateRunRefusesMissingIdentityAndStaleUnbaselinedRuns(t *testing.T) {
	t.Parallel()
	if _, err := locateRun(context.Background(), Receipt{}, Options{Runner: &rpCovCommandRunner{}}); err == nil ||
		!strings.Contains(err.Error(), "without the persisted head, baseline, and dispatch timestamp") {
		t.Fatalf("missing-identity error = %v", err)
	}

	created := time.Now().UTC().Truncate(time.Second)
	stale := workflowRunFixture("123", "queued", "", created.Add(-10*time.Minute))
	runner := &rpCovCommandRunner{steps: []CommandResult{{Output: workflowRunList(stale)}}}
	receipt := Receipt{Release: testRelease(), HeadSHA: releaseHead, DispatchAt: created, DispatchBaselineAt: created}
	if _, err := locateRun(context.Background(), receipt, Options{Runner: runner}); err == nil ||
		!strings.Contains(err.Error(), "predates dispatch") {
		t.Fatalf("stale-run error = %v, want the delayed-visibility refusal", err)
	}

	listErr := errors.New("gh run list failed")
	failing := &rpCovCommandRunner{steps: []CommandResult{{Code: 1, Err: listErr, Output: "nope"}}}
	if _, err := locateRun(context.Background(), receipt, Options{Runner: failing}); err == nil ||
		!strings.Contains(err.Error(), "locate dispatched workflow run") {
		t.Fatalf("list error = %v", err)
	}
}

func TestRPCovLocateRunPollsOnTheDefaultIntervalAndHonoursCancellation(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Truncate(time.Second)
	runner := &rpCovCommandRunner{steps: []CommandResult{
		{Output: `[]`},
		{Output: `[]`},
	}}
	receipt := Receipt{Release: testRelease(), HeadSHA: releaseHead, DispatchAt: created, DispatchBaselineAt: created}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := locateRun(ctx, receipt, Options{Runner: runner, PollInterval: 0})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the caller's cancellation", err)
	}
	if len(runner.calls) < 1 {
		t.Fatalf("locateRun never polled before observing the default interval: %v", runner.calls)
	}
}

func TestRPCovWaitRunRejectsMalformedAndMismatchedObservations(t *testing.T) {
	t.Parallel()
	receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
	noPersist := func() error { return nil }

	garbled := &rpCovCommandRunner{steps: []CommandResult{{Output: "{"}}}
	if err := waitRun(context.Background(), receipt, Options{Runner: garbled}, noPersist); err == nil ||
		!strings.Contains(err.Error(), "decode workflow run") {
		t.Fatalf("decode error = %v", err)
	}

	foreign := &rpCovCommandRunner{steps: []CommandResult{{Output: workflowRunFixture("999", "completed", "success", time.Now())}}}
	if err := waitRun(context.Background(), receipt, Options{Runner: foreign}, noPersist); err == nil ||
		!strings.Contains(err.Error(), "no longer identifies exact workflow_dispatch run") {
		t.Fatalf("identity error = %v", err)
	}

	extraEvent := &rpCovCommandRunner{steps: []CommandResult{{Output: strings.Replace(
		workflowRunFixture("123", "completed", "success", time.Now()), `"event":"workflow_dispatch"`, `"event":"push"`, 1)}}}
	if err := waitRun(context.Background(), receipt, Options{Runner: extraEvent}, noPersist); err == nil ||
		!strings.Contains(err.Error(), "no longer identifies exact workflow_dispatch run") {
		t.Fatalf("event error = %v", err)
	}
}

func TestRPCovWaitRunClassifiesConclusionsTimeoutsAndPersistFailures(t *testing.T) {
	t.Parallel()
	base := func(status, conclusion string) *rpCovCommandRunner {
		return &rpCovCommandRunner{steps: []CommandResult{
			{Output: workflowRunFixture("123", status, conclusion, time.Now().UTC())},
		}}
	}
	noPersist := func() error { return nil }

	receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
	if err := waitRun(context.Background(), receipt, Options{Runner: base("completed", "neutral")}, noPersist); err == nil ||
		!strings.Contains(err.Error(), "completed without a successful conclusion") {
		t.Fatalf("unexpected-conclusion error = %v", err)
	}
	if receipt.Status != StatusFailed {
		t.Fatalf("receipt status = %q, want failed", receipt.Status)
	}

	timedOut := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
	if err := waitRun(context.Background(), timedOut, Options{Runner: base("in_progress", ""), Timeout: time.Nanosecond}, noPersist); err == nil ||
		!strings.Contains(err.Error(), "did not complete before timeout") {
		t.Fatalf("timeout error = %v", err)
	}
	if timedOut.Status != StatusAwaitingRun {
		t.Fatalf("timed-out status = %q, want awaiting_run", timedOut.Status)
	}

	persistErr := errors.New("disk full")
	for _, test := range []struct {
		name       string
		status     string
		conclusion string
		failOn     int
	}{
		{"success", "completed", "success", 1},
		{"failure", "completed", "failure", 1},
		{"unexpected conclusion", "completed", "neutral", 1},
		{"waiting", "in_progress", "", 1},
	} {
		calls := 0
		persist := func() error {
			calls++
			if calls >= test.failOn {
				return persistErr
			}
			return nil
		}
		receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
		if err := waitRun(context.Background(), receipt, Options{Runner: base(test.status, test.conclusion)}, persist); !errors.Is(err, persistErr) {
			t.Errorf("%s: error = %v, want the persistence failure", test.name, err)
		}
	}
}

func TestRPCovWaitRunPollsOnTheDefaultIntervalAndHonoursCancellation(t *testing.T) {
	t.Parallel()
	runner := &rpCovCommandRunner{steps: []CommandResult{
		{Output: workflowRunFixture("123", "in_progress", "", time.Now().UTC())},
		{Output: workflowRunFixture("123", "in_progress", "", time.Now().UTC())},
	}}
	receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := waitRun(ctx, receipt, Options{Runner: runner, PollInterval: 0}, func() error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the caller's cancellation", err)
	}
	if len(runner.calls) < 1 {
		t.Fatalf("waitRun never polled before observing the default interval: %v", runner.calls)
	}
}

func TestRPCovWaitRunReportsCommandFailureAndObservesProgress(t *testing.T) {
	t.Parallel()
	receipt := &Receipt{Release: testRelease(), Status: StatusAwaitingRun, RunID: "123", HeadSHA: releaseHead}
	failing := &rpCovCommandRunner{steps: []CommandResult{{Code: 1, Err: errors.New("gh exhausted"), Output: "429 too many requests"}}}
	err := waitRun(context.Background(), receipt, Options{Runner: failing}, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "observe workflow run 123") {
		t.Fatalf("error = %v, want the observation failure", err)
	}
}

func TestRPCovListExactWorkflowRunsRejectsDecodingDuplicatesAndSkippedRuns(t *testing.T) {
	t.Parallel()
	receipt := Receipt{Release: testRelease(), HeadSHA: releaseHead}

	garbled := &rpCovCommandRunner{steps: []CommandResult{{Output: "{"}}}
	if _, err := listExactWorkflowRuns(context.Background(), receipt, Options{Runner: garbled}); err == nil ||
		!strings.Contains(err.Error(), "decode GitHub workflow run list") {
		t.Fatalf("decode error = %v", err)
	}

	failing := &rpCovCommandRunner{steps: []CommandResult{{Code: 1, Err: errors.New("boom")}}}
	if _, err := listExactWorkflowRuns(context.Background(), receipt, Options{Runner: failing}); err == nil ||
		!strings.Contains(err.Error(), "list exact workflow runs") {
		t.Fatalf("command error = %v", err)
	}

	good := workflowRunFixture("5", "queued", "", time.Now().UTC())
	duplicate := &rpCovCommandRunner{steps: []CommandResult{{Output: workflowRunList(good, good)}}}
	if _, err := listExactWorkflowRuns(context.Background(), receipt, Options{Runner: duplicate}); err == nil ||
		!strings.Contains(err.Error(), "duplicate workflow run ID 5") {
		t.Fatalf("duplicate error = %v", err)
	}

	// Runs with no ID, a different head, or a different event are omitted from
	// the baseline rather than being treated as candidates.
	none := `{"databaseId":0,"headSha":"` + releaseHead + `","event":"workflow_dispatch"}`
	otherHead := `{"databaseId":7,"headSha":"` + strings.Repeat("a", 40) + `","event":"workflow_dispatch"}`
	pushEvent := `{"databaseId":8,"headSha":"` + releaseHead + `","event":"push"}`
	skipping := &rpCovCommandRunner{steps: []CommandResult{{Output: workflowRunList(none, otherHead, pushEvent, good)}}}
	runs, err := listExactWorkflowRuns(context.Background(), receipt, Options{Runner: skipping})
	if err != nil || len(runs) != 1 || workflowRunID(runs[0]) != "5" {
		t.Fatalf("filtered runs = %+v, err=%v", runs, err)
	}
}
