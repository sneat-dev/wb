package sessionlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSlCovHarnessSelectionAndNormalization(t *testing.T) {
	t.Parallel()
	if err := ValidateHarnessSelection(RuntimeCodex, ""); err != nil {
		t.Fatalf("ValidateHarnessSelection(inherit) = %v", err)
	}
	if err := ValidateHarnessSelection(RuntimeCodex, "codex"); err != nil {
		t.Fatalf("ValidateHarnessSelection(codex) = %v", err)
	}
	if err := ValidateHarnessSelection(RuntimeCodex, "claude code"); err != nil {
		t.Fatalf("ValidateHarnessSelection(claude code) = %v", err)
	}
	if err := ValidateHarnessSelection(RuntimeCodex, "shell"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ValidateHarnessSelection(shell) = %v, want refusal", err)
	}
	if got := NormalizeModel("  opus  "); got != "opus" {
		t.Fatalf("NormalizeModel = %q", got)
	}
	if got := NormalizeModel("\t"); got != "" {
		t.Fatalf("NormalizeModel(blank) = %q", got)
	}
	if runtime, err := NormalizeRuntime("claude-code", "codex"); err != nil || runtime != RuntimeCodex {
		t.Fatalf("requested codex = %q %v", runtime, err)
	}
	if runtime, err := NormalizeRuntime("claude-code", "  "); err != nil || runtime != RuntimeClaudeCode {
		t.Fatalf("inherit source = %q %v", runtime, err)
	}
	if runtime, err := NormalizeRuntime(" Codex ", "claude code"); err != nil || runtime != RuntimeClaudeCode {
		t.Fatalf("spoken claude = %q %v", runtime, err)
	}
}

func TestSlCovCleanAbsoluteExecutableRejectsEveryNonExecutableShape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executable := slCovExecutable(t, root, "run")
	plain := filepath.Join(root, "plain")
	slCovWrite(t, plain, 0o644, "x")
	directory := filepath.Join(root, "dir")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "relative", path: "relative/run"},
		{name: "unclean", path: root + "/./run"},
		{name: "missing", path: filepath.Join(root, "absent")},
		{name: "directory", path: directory},
		{name: "not executable", path: plain},
		{name: "valid", path: executable, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := cleanAbsoluteExecutable(test.path)
			if test.want {
				if err != nil || got != test.path {
					t.Fatalf("cleanAbsoluteExecutable = %q %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("cleanAbsoluteExecutable(%q) unexpectedly accepted", test.path)
			}
		})
	}
}

func TestSlCovValidatePlanExecutablesNamesTheInvalidSide(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wb := slCovExecutable(t, root, "wb")
	harness := slCovExecutable(t, root, "harness")
	plan := slCovPlan("handoff-123")
	plan.WBExecutable, plan.HarnessExecutable = wb, harness
	if err := validatePlanExecutables(plan); err != nil {
		t.Fatalf("valid plan = %v", err)
	}
	plan.WBExecutable = filepath.Join(root, "absent")
	if err := validatePlanExecutables(plan); err == nil || !strings.Contains(err.Error(), "WB executable") {
		t.Fatalf("invalid WB executable = %v", err)
	}
	plan.WBExecutable = wb
	plan.HarnessExecutable = filepath.Join(root, "absent")
	if err := validatePlanExecutables(plan); err == nil || !strings.Contains(err.Error(), "harness executable") {
		t.Fatalf("invalid harness executable = %v", err)
	}
}

func TestSlCovProveProcessDeadClassifiesEveryProbeOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		probe func(int) error
		want  string
	}{
		{name: "dead", probe: func(int) error { return syscall.ESRCH }},
		{name: "live", probe: func(int) error { return nil }, want: "PID is live"},
		{name: "permission ambiguous", probe: func(int) error { return syscall.EPERM }, want: "permission-ambiguous"},
		{name: "unexpected ambiguous", probe: func(int) error { return syscall.EINVAL }, want: "probe is ambiguous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := proveProcessDead(dependencies{processStatus: test.probe}, 4242)
			if test.want == "" {
				if err != nil {
					t.Fatalf("proveProcessDead = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("proveProcessDead = %v, want %q", err, test.want)
			}
		})
	}
	// A nil probe falls back to the platform processStatus and reports a live
	// PID for this very process.
	err := proveProcessDead(dependencies{}, os.Getpid())
	if err == nil || !strings.Contains(err.Error(), "PID is live") {
		t.Fatalf("fallback probe = %v, want live PID", err)
	}
}

func TestSlCovProcessStatusObservesALiveProcess(t *testing.T) {
	t.Parallel()
	if err := processStatus(os.Getpid()); err != nil {
		t.Fatalf("processStatus(self) = %v", err)
	}
}

func TestSlCovParseAttemptIDBoundsEveryField(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want uint64
		ok   bool
	}{
		{name: "valid", in: "000001-0000000000000000000000000000000a", want: 1, ok: true},
		{name: "large", in: "123456-0123456789abcdef0123456789abcdef", want: 123456, ok: true},
		{name: "too few parts", in: "000001"},
		{name: "too many parts", in: "000001-0000000000000000000000000000000a-extra"},
		{name: "short index", in: "0001-0000000000000000000000000000000a"},
		{name: "short entropy", in: "000001-000000000000000000000000000000"},
		{name: "non numeric index", in: "00000x-0000000000000000000000000000000a"},
		{name: "zero index", in: "000000-0000000000000000000000000000000a"},
		{name: "non hex entropy", in: "000001-0000000000000000000000000000000z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			index, err := parseAttemptID(test.in)
			if !test.ok {
				if err == nil {
					t.Fatalf("parseAttemptID(%q) unexpectedly accepted", test.in)
				}
				return
			}
			if err != nil || index != test.want {
				t.Fatalf("parseAttemptID(%q) = %d %v", test.in, index, err)
			}
		})
	}
}

func TestSlCovLaunchJSONEncodingRejectsUnsupportedAndNonStrictInput(t *testing.T) {
	t.Parallel()
	if _, err := encodeLaunchJSON(make(chan int)); err == nil {
		t.Fatal("encodeLaunchJSON accepted a channel")
	}
	var plan launchPlan
	if err := decodeLaunchJSON([]byte(`{"schema_version":1,"unknown_field":true}`), &plan); err == nil {
		t.Fatal("decodeLaunchJSON accepted an unknown field")
	}
	if err := decodeLaunchJSON([]byte(`{`), &plan); err == nil {
		t.Fatal("decodeLaunchJSON accepted malformed JSON")
	}
	if err := decodeLaunchJSON([]byte("{\"schema_version\":1}\n{}\n"), &plan); err == nil {
		t.Fatal("decodeLaunchJSON accepted trailing JSON")
	}
	raw, err := encodeLaunchJSON(plan)
	if err != nil || !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("encodeLaunchJSON = %q %v", raw, err)
	}
}

func TestSlCovReadPrivateArtifactAtEnforcesPrivateRegularFileShape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	slCovWrite(t, filepath.Join(root, "good"), 0o600, "private")
	slCovWrite(t, filepath.Join(root, "wide"), 0o644, "private")
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "good"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	slCovWrite(t, filepath.Join(root, "big"), 0o600, strings.Repeat("x", 32))

	if raw, err := readPrivateArtifactAt(directory, "good", 64); err != nil || string(raw) != "private" {
		t.Fatalf("read good = %q %v", raw, err)
	}
	if err := os.Link(filepath.Join(root, "good"), filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"absent", "wide", "subdir", "link", "hardlink", "big"} {
		if _, err := readPrivateArtifactAt(directory, name, 16); err == nil {
			t.Fatalf("readPrivateArtifactAt(%q) unexpectedly accepted", name)
		}
	}
}

func TestSlCovValidatePrivateLaunchFileRejectsClosedDescriptorAndBadMode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	closedPath := filepath.Join(root, "closed")
	slCovWrite(t, closedPath, 0o600, "x")
	closed, err := os.Open(closedPath)
	if err != nil {
		t.Fatal(err)
	}
	closedFD := int(closed.Fd())
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateLaunchFile(closedFD, "closed", 0); err == nil {
		t.Fatal("validatePrivateLaunchFile accepted a closed descriptor")
	}
	wide := filepath.Join(root, "wide")
	slCovWrite(t, wide, 0o644, "x")
	file, err := os.Open(wide)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := validatePrivateLaunchFile(int(file.Fd()), "wide", 0); err == nil {
		t.Fatal("validatePrivateLaunchFile accepted mode 0644")
	}
	good := filepath.Join(root, "good")
	slCovWrite(t, good, 0o600, "")
	goodFile, err := os.Open(good)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = goodFile.Close() }()
	if err := validatePrivateLaunchFile(int(goodFile.Fd()), "good", 0); err != nil {
		t.Fatalf("validatePrivateLaunchFile(good) = %v", err)
	}
	if _, err := fileForFD(-1, "negative"); err == nil {
		t.Fatal("fileForFD accepted a negative descriptor")
	}
}

func TestSlCovDefaultPrivateLauncherDependenciesAreWired(t *testing.T) {
	t.Parallel()
	deps := defaultPrivateLauncherDependencies()
	if deps.pid == nil || deps.register == nil || deps.sleep == nil || deps.exec == nil ||
		deps.verifyPinned == nil || deps.now == nil || deps.wbExecutable == nil {
		t.Fatalf("default dependencies incomplete: %#v", deps)
	}
	if deps.pid() != os.Getpid() {
		t.Fatalf("default pid = %d", deps.pid())
	}
	if _, err := deps.wbExecutable(); err != nil {
		t.Fatalf("default wbExecutable = %v", err)
	}
	if deps.now().IsZero() {
		t.Fatal("default now is zero")
	}
}

func TestSlCovRunPrivateLauncherRejectsInvalidArgvWithExitOne(t *testing.T) {
	t.Parallel()
	if code := RunPrivateLauncher(nil); code != 1 {
		t.Fatalf("RunPrivateLauncher(nil) = %d", code)
	}
	if code := RunPrivateLauncher([]string{"only"}); code != 1 {
		t.Fatalf("RunPrivateLauncher(short) = %d", code)
	}
	if code := launcherError(errors.New("injected")); code != 1 {
		t.Fatalf("launcherError = %d", code)
	}
}

func TestSlCovLaunchAccessorsExposeDirectoryIdentity(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	if directory, err := state.directory(""); err != nil || directory != state.launch {
		t.Fatalf("state.directory(\"\") = %v %v", directory, err)
	}
	if directory, err := state.directory(attemptsDirectoryName); err != nil || directory != state.attempts {
		t.Fatalf("state.directory(attempts) = %v %v", directory, err)
	}
	if _, err := state.directory("bogus"); err == nil {
		t.Fatal("state.directory accepted an unknown child")
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attempt.Close() }()
	if directory, err := attempt.directory(""); err != nil || directory != attempt.root {
		t.Fatalf("attempt.directory(\"\") = %v %v", directory, err)
	}
	if directory, err := attempt.directory(readyDirectoryName); err != nil || directory != attempt.ready {
		t.Fatalf("attempt.directory(ready) = %v %v", directory, err)
	}
	if directory, err := attempt.directory(execDirectoryName); err != nil || directory != attempt.exec {
		t.Fatalf("attempt.directory(exec) = %v %v", directory, err)
	}
	if _, err := attempt.directory("bogus"); err == nil {
		t.Fatal("attempt.directory accepted an unknown child")
	}
	if _, err := state.read("bogus", "x"); err == nil {
		t.Fatal("state.read accepted an unknown directory")
	}
	if _, err := state.publish("bogus", "x", []byte("x")); err == nil {
		t.Fatal("state.publish accepted an unknown directory")
	}
	if _, err := attempt.read("bogus", "x"); err == nil {
		t.Fatal("attempt.read accepted an unknown directory")
	}
	if _, err := attempt.publish("bogus", "x", []byte("x")); err == nil {
		t.Fatal("attempt.publish accepted an unknown directory")
	}
}

func TestSlCovPublishLaunchArtifactRejectsOversizedPayload(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	oversized := strings.Repeat("x", maxLaunchArtifactBytes+1)
	if created, err := state.publish("", "plan.json", []byte(oversized)); err == nil || created {
		t.Fatalf("oversized publish = created %t err %v", created, err)
	}
}

func TestSlCovLatestAttemptSignalsAbsentHistory(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	if _, err := latestAttempt(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("latestAttempt on empty history = %v", err)
	}
	if _, err := state.openAttempt("not-an-attempt"); err == nil {
		t.Fatal("openAttempt accepted a malformed attempt ID")
	}
}

func TestSlCovEqualHelpersDetectDivergence(t *testing.T) {
	t.Parallel()
	plan := slCovPlan("handoff-123")
	if !equalLaunchPlan(plan, plan) {
		t.Fatal("equalLaunchPlan rejected identical plans")
	}
	other := plan
	other.Model = "other"
	if equalLaunchPlan(plan, other) {
		t.Fatal("equalLaunchPlan accepted divergent plans")
	}
	ready := launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID, PID: 7}
	if !equalReady(ready, ready) {
		t.Fatal("equalReady rejected identical ready artifacts")
	}
	changed := ready
	changed.PID = 8
	if equalReady(ready, changed) {
		t.Fatal("equalReady accepted divergent ready artifacts")
	}
}

func TestSlCovBoundedTmuxDetailTruncatesAndFlattens(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 4096)
	got := boundedTmuxDetail([]byte("line one\r\nline two\n" + long))
	if len(got) != 1024 {
		t.Fatalf("bounded detail length = %d, want 1024", len(got))
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("bounded detail retained newlines: %q", got[:32])
	}
	if got := boundedTmuxDetail([]byte("  spaced \n")); got != "spaced" {
		t.Fatalf("bounded detail = %q", got)
	}
}

func TestSlCovDigestRoundTripMatchesRequestDigest(t *testing.T) {
	t.Parallel()
	plan := slCovPlan("handoff-123")
	if plan.RequestDigest != slCovDigest("request") {
		t.Fatalf("helper digest = %q", plan.RequestDigest)
	}
	if !slCovDigest("x").Matches([]byte("x")) || slCovDigest("x").Matches([]byte("y")) {
		t.Fatal("digest matching is not exact")
	}
}
