package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionReceiveTaskPathRequiresExactRepositorySuffixAndOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-transfer", "github.com", "acme", "app")
	if got, ok := sessionReceiveTaskPath(path, "session-transfer", "github.com/acme/app"); !ok || got != filepath.Dir(filepath.Dir(filepath.Dir(path))) {
		t.Fatalf("exact task path = %q, %v", got, ok)
	}
	for _, tc := range []struct{ operation, relative string }{
		{"session-other", "github.com/acme/app"},
		{"session-transfer", "github.com/acme/other"},
		{"session-transfer", "acme/app"},
	} {
		if got, ok := sessionReceiveTaskPath(path, tc.operation, tc.relative); ok || got != "" {
			t.Fatalf("accepted operation=%q relative=%q: %q", tc.operation, tc.relative, got)
		}
	}
}

func TestLoadOriginalPromptRejectsUnsafeArchiveAndPreservesJournalPriority(t *testing.T) {
	claim := workLogClaim{PromptArchive: "../outside"}
	journal := []PromptRecord{{Name: "0000-start.md", SHA256: "digest", Body: "from journal"}}
	got, err := loadOriginalPrompt("", claim, journal)
	if err != nil || got.Source != "journal" || got.Body != "from journal" {
		t.Fatalf("journal original = %#v, %v", got, err)
	}
	for _, name := range []string{"../outside", ".", "..", "subdir/archive"} {
		claim.PromptArchive = name
		if _, err := loadOriginalPrompt(t.TempDir(), claim, nil); err == nil || !strings.Contains(err.Error(), "unsafe prompt archive") {
			t.Fatalf("archive %q: %v", name, err)
		}
	}
	claim.PromptArchive = ""
	if _, err := loadOriginalPrompt(t.TempDir(), claim, nil); err == nil {
		t.Fatal("missing private archive was accepted")
	}
}

func TestParsePromptFileRejectsMalformedFrontmatter(t *testing.T) {
	for _, content := range []string{"plain prompt", "---\nsource: human\n"} {
		if _, _, err := parsePromptFile([]byte(content)); err == nil {
			t.Fatalf("accepted malformed prompt %q", content)
		}
	}
	header, body, err := parsePromptFile([]byte("---\nsource: human_declared\nseq: 0\n---\n\nkeep this exact body\n"))
	if err != nil || header.Source != PromptSourceHuman || body != "keep this exact body\n" {
		t.Fatalf("parsed = %#v, %q, %v", header, body, err)
	}
}

func TestListPromptRecordsRejectsOrdinalMismatchAndGap(t *testing.T) {
	root := t.TempDir()
	if records, err := listPromptRecords(root, true); err != nil || len(records) != 0 {
		t.Fatalf("missing journal = %#v, %v", records, err)
	}
	dir := filepath.Join(root, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, seq int) {
		t.Helper()
		content := "---\nsource: human_declared\nseq: " + string(rune('0'+seq)) + "\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("0000-start.md", 1)
	if _, err := listPromptRecords(root, true); err == nil || !strings.Contains(err.Error(), "ordinal") {
		t.Fatalf("ordinal mismatch: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "0000-start.md")); err != nil {
		t.Fatal(err)
	}
	write("0001-next.md", 1)
	if _, err := listPromptRecords(root, true); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("sequence gap: %v", err)
	}
}

func TestCopyDirCopiesNestedContentsAndReportsMissingSource(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "source"), filepath.Join(root, "archive")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "body.txt"), []byte("private body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "nested", "body.txt"))
	if err != nil || string(got) != "private body" {
		t.Fatalf("copy = %q, %v", got, err)
	}
	if err := copyDir(filepath.Join(root, "missing"), filepath.Join(root, "other")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source = %v", err)
	}
}

func TestProjectionReadersAndRemovalKeepLegacySeparate(t *testing.T) {
	root := t.TempDir()
	if _, err := readWorkLogProjectionForReadOnlyClaim(root); !errors.Is(err, errWorkLogProjectionNotFound) {
		t.Fatalf("missing projection = %v", err)
	}
	if err := removeWorkLogProjection(root); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyWorkLogProjection(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, workLogProjectionDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, workLogProjectionDirectory, workLogProjectionName), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogProjectionForReadOnlyClaim(root); err == nil || errors.Is(err, errWorkLogProjectionNotFound) {
		t.Fatalf("corrupt current projection = %v", err)
	}
	if err := removeWorkLogProjection(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, workLogProjectionDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("projection directory remains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, legacyWorkLogProjectionName), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyWorkLogProjection(root); err == nil {
		t.Fatal("legacy reader accepted corrupt projection")
	}
	if _, err := readWorkLogProjectionForReadOnlyClaim(root); err == nil || errors.Is(err, errWorkLogProjectionNotFound) {
		t.Fatalf("corrupt legacy projection = %v", err)
	}
	if err := removeLegacyWorkLogProjection(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyWorkLogProjectionName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy projection remains: %v", err)
	}
}

func TestCloneParentRelativePreservesLegacyAndHostedAddresses(t *testing.T) {
	if got := cloneParentRelative("", "acme"); got != "acme" {
		t.Fatalf("legacy parent = %q", got)
	}
	if got := cloneParentRelative("github.com", "acme"); got != "github.com/acme" {
		t.Fatalf("hosted parent = %q", got)
	}
}

func TestInterruptedReceiveStageParsingAndAmbiguity(t *testing.T) {
	root := t.TempDir()
	stage := ".wb-stage-0123456789abcdef0123456789abcdef"
	if !validInterruptedSessionStageName(stage) {
		t.Fatal("valid stage refused")
	}
	for _, invalid := range []string{".wb-stage-short", ".wb-stage-0123456789abcdef0123456789abcdeg", "other-0123456789abcdef0123456789abcdef"} {
		if validInterruptedSessionStageName(invalid) {
			t.Fatalf("accepted %q", invalid)
		}
	}
	checkout := filepath.Join(root, stage, "checkout")
	if got, ok := exactInterruptedSessionStage(root, checkout); !ok || got != stage {
		t.Fatalf("exact stage = %q, %v", got, ok)
	}
	for _, path := range []string{filepath.Join(root, "other", "checkout"), filepath.Join(root, stage, "nested", "checkout")} {
		if got, ok := exactInterruptedSessionStage(root, path); ok || got != "" {
			t.Fatalf("accepted path %q", path)
		}
	}
	operation, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	if err := requireOnlyInterruptedSessionStage(operation, stage); err == nil {
		t.Fatal("accepted missing stage")
	}
	if err := os.Mkdir(filepath.Join(root, stage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "unrelated"), 0o700); err != nil {
		t.Fatal(err)
	}
	names, err := interruptedSessionStageNames(operation)
	if err != nil || len(names) != 1 || names[0] != stage {
		t.Fatalf("stage names = %#v, %v", names, err)
	}
	if err := requireOnlyInterruptedSessionStage(operation, stage); err != nil {
		t.Fatal(err)
	}
	if err := requireOnlyInterruptedSessionStage(operation, "another"); err == nil {
		t.Fatal("accepted mismatched stage")
	}
	if err := os.Mkdir(filepath.Join(root, ".wb-stage-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := requireOnlyInterruptedSessionStage(operation, stage); err == nil {
		t.Fatal("accepted multiple stages")
	}
}

func TestFinalizeReportPrivateRoundTripAndValidation(t *testing.T) {
	if _, err := finalizeReportFileName("bad/task", "acme/app"); err == nil {
		t.Fatal("accepted unsafe task")
	}
	if _, err := finalizeReportFileName("task", "bad-repository"); err == nil {
		t.Fatal("accepted unsafe repository")
	}
	name, err := finalizeReportFileName(" task ", "acme/app")
	if err != nil || name != "task--acme--app.md" {
		t.Fatalf("report name = %q, %v", name, err)
	}
	home := t.TempDir()
	if _, err := writeWorkLogFinalizeReport(home, "effort", "run", "task", "acme/app", make([]byte, MaxFinalizeReportBytes+1)); err == nil {
		t.Fatal("accepted oversized report")
	}
	path, err := writeWorkLogFinalizeReport(home, "effort", "run", "task", "acme/app", []byte("first report"))
	if err != nil {
		t.Fatal(err)
	}
	if body, err := readWorkLogFinalizeReportBody(path); err != nil || body != "first report" {
		t.Fatalf("read = %q, %v", body, err)
	}
	second, err := writeWorkLogFinalizeReport(home, "effort", "run", "task", "acme/app", []byte("revised report"))
	if err != nil || second != path {
		t.Fatalf("retry path = %q, %v", second, err)
	}
	if body, err := readWorkLogFinalizeReportBody(path); err != nil || body != "revised report" {
		t.Fatalf("revised read = %q, %v", body, err)
	}
	if _, err := readWorkLogFinalizeReportBody(filepath.Join(home, "missing.md")); err == nil {
		t.Fatal("missing report read")
	}
}

func TestPrivateWorkLogDirectoriesValidateIdentityAndCreation(t *testing.T) {
	home := t.TempDir()
	if _, _, err := openWorkLogRun(home, "bad/effort", "run", true); err == nil {
		t.Fatal("accepted unsafe effort")
	}
	if _, _, err := openWorkLogRun(home, "effort", "bad/run", true); err == nil {
		t.Fatal("accepted unsafe run")
	}
	if _, _, err := openWorkLogRun(home, "effort", "run", false); err == nil {
		t.Fatal("opened absent run")
	}
	run, path, err := openWorkLogRun(home, "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "worklogs", "effort", "runs", "run") {
		t.Fatalf("run path = %q", path)
	}
	if _, err := openPrivateChild(run, "bad/child", true); err == nil {
		t.Fatal("accepted unsafe child")
	}
	child, err := openPrivateChild(run, "reports", true)
	if err != nil {
		t.Fatal(err)
	}
	child.Close()
	run.Close()
	if _, err := openWorkLogOutbox(home, "bad/effort", true); err == nil {
		t.Fatal("accepted unsafe outbox effort")
	}
	outbox, err := openWorkLogOutbox(home, "effort", true)
	if err != nil {
		t.Fatal(err)
	}
	outbox.Close()
	if _, err := os.Stat(filepath.Join(home, "worklogs", "effort", "outbox")); err != nil {
		t.Fatal(err)
	}
}

func TestRemovedTerminalWorkLogEntryPointsFailClosed(t *testing.T) {
	if err := ValidateRemovedTerminalWorkLogs(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("empty validation = %v", err)
	}
	if _, err := ReadRemovedTerminalWorkLogClaimBase(t.TempDir(), TerminalWorkLogExpectation{}); err == nil {
		t.Fatal("read accepted absent identity")
	}
}

func TestOptionalClaimFenceRequiresAnActiveClaimWhenRequested(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := withOptionalClaimFence(fixture.projectsRoot, fixture.canonical, true); err == nil || !strings.Contains(err.Error(), "active Work Log claim required") {
		t.Fatalf("required claim = %v", err)
	}
	fence, err := withOptionalClaimFence(fixture.projectsRoot, fixture.canonical, false)
	if err != nil || fence.home == "" || fence.unlock != nil {
		t.Fatalf("optional claim = %#v, %v", fence, err)
	}
}
