package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func wtLifeCovWriteJSON(t *testing.T, path string, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func wtLifeCovTerminalReport(task, repository string, generatedAt time.Time, applied bool) cleanupReport {
	result := CleanupResult{ListResult: ListResult{Task: task, Repository: repository}, Applied: applied}
	if applied {
		result.WorktreeGone = true
		result.BranchDeleted = true
	} else {
		result.Reason = "branch still reachable"
	}
	return cleanupReport{
		GeneratedAt: generatedAt,
		Phase:       "applied",
		Apply:       true,
		Task:        task,
		Results:     []CleanupResult{result},
	}
}

func TestWtLifeCovValidateTerminalCleanupReportsRejectsEveryInconsistency(t *testing.T) {
	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	write := func(t *testing.T, report cleanupReport) string {
		t.Helper()
		return wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "report.json"), report)
	}

	cases := []struct {
		name    string
		paths   func(t *testing.T) []string
		tasks   []string
		wantErr string
	}{
		{
			name:    "empty expected task identity",
			paths:   func(t *testing.T) []string { return nil },
			tasks:   []string{""},
			wantErr: "expected task identities are inconsistent",
		},
		{
			name:    "duplicate expected task",
			paths:   func(t *testing.T) []string { return nil },
			tasks:   []string{"task-one", "task-one"},
			wantErr: "expected task identities are inconsistent",
		},
		{
			name:    "no expected tasks",
			paths:   func(t *testing.T) []string { return nil },
			tasks:   nil,
			wantErr: "no expected tasks",
		},
		{
			name:    "relative report path",
			paths:   func(t *testing.T) []string { return []string{"report.json"} },
			tasks:   []string{"task-one"},
			wantErr: "not one unique absolute path",
		},
		{
			name: "duplicate report path",
			paths: func(t *testing.T) []string {
				path := write(t, wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				return []string{path, path}
			},
			tasks:   []string{"task-one"},
			wantErr: "not one unique absolute path",
		},
		{
			name: "missing report file",
			paths: func(t *testing.T) []string {
				return []string{filepath.Join(t.TempDir(), "absent.json")}
			},
			tasks:   []string{"task-one"},
			wantErr: "stat terminal cleanup report",
		},
		{
			name: "report is a directory",
			paths: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "report.json")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return []string{path}
			},
			tasks:   []string{"task-one"},
			wantErr: "is not a regular file",
		},
		{
			name: "report is a symlink",
			paths: func(t *testing.T) []string {
				target := write(t, wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				link := filepath.Join(t.TempDir(), "report.json")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return []string{link}
			},
			tasks:   []string{"task-one"},
			wantErr: "is not a regular file",
		},
		{
			name: "unparseable report",
			paths: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "report.json")
				if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{path}
			},
			tasks:   []string{"task-one"},
			wantErr: "decode terminal cleanup report",
		},
		{
			name: "trailing json value",
			paths: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "report.json")
				encoded, err := json.Marshal(wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(encoded, []byte(" {}")...), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{path}
			},
			tasks:   []string{"task-one"},
			wantErr: "multiple JSON values",
		},
		{
			name: "unknown field",
			paths: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "report.json")
				if err := os.WriteFile(path, []byte(`{"generated_at":"2026-09-16T10:00:00Z","surprise":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{path}
			},
			tasks:   []string{"task-one"},
			wantErr: "decode terminal cleanup report",
		},
		{
			name: "zero generated at",
			paths: func(t *testing.T) []string {
				report := wtLifeCovTerminalReport("task-one", "acme/app", time.Time{}, true)
				return []string{write(t, report)}
			},
			tasks:   []string{"task-one"},
			wantErr: "non-monotonic generated_at",
		},
		{
			name: "non-monotonic generated at",
			paths: func(t *testing.T) []string {
				first := write(t, wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				second := wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "report.json"), wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				return []string{first, second}
			},
			tasks:   []string{"task-one"},
			wantErr: "non-monotonic generated_at",
		},
		{
			name: "unexpected phase",
			paths: func(t *testing.T) []string {
				report := wtLifeCovTerminalReport("task-one", "acme/app", base, true)
				report.Phase = "planned"
				return []string{write(t, report)}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent applied schema",
		},
		{
			name: "unexpected task",
			paths: func(t *testing.T) []string {
				return []string{write(t, wtLifeCovTerminalReport("task-other", "acme/app", base, true))}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent applied schema",
		},
		{
			name: "multiple results",
			paths: func(t *testing.T) []string {
				report := wtLifeCovTerminalReport("task-one", "acme/app", base, true)
				report.Results = append(report.Results, report.Results[0])
				return []string{write(t, report)}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent applied schema",
		},
		{
			name: "repository mismatch",
			paths: func(t *testing.T) []string {
				return []string{write(t, wtLifeCovTerminalReport("task-one", "other/app", base, true))}
			},
			tasks:   []string{"task-one"},
			wantErr: "does not match receipt task/repository identity",
		},
		{
			name: "incomplete successful evidence",
			paths: func(t *testing.T) []string {
				report := wtLifeCovTerminalReport("task-one", "acme/app", base, true)
				report.Results[0].BranchDeleted = false
				return []string{write(t, report)}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent successful cleanup evidence",
		},
		{
			name: "duplicate successful cleanup",
			paths: func(t *testing.T) []string {
				first := write(t, wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				later := wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "report.json"),
					wtLifeCovTerminalReport("task-one", "acme/app", base.Add(time.Minute), true))
				return []string{first, later}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent successful cleanup evidence",
		},
		{
			name: "failed result looks successful",
			paths: func(t *testing.T) []string {
				report := wtLifeCovTerminalReport("task-one", "acme/app", base, false)
				report.Results[0].WorktreeGone = true
				report.Results[0].BranchDeleted = true
				return []string{write(t, report)}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent failed cleanup evidence",
		},
		{
			name: "failed result without reason",
			paths: func(t *testing.T) []string {
				report := wtLifeCovTerminalReport("task-one", "acme/app", base, false)
				report.Results[0].Reason = " "
				return []string{write(t, report)}
			},
			tasks:   []string{"task-one"},
			wantErr: "inconsistent failed cleanup evidence",
		},
		{
			name: "task never completed",
			paths: func(t *testing.T) []string {
				return []string{write(t, wtLifeCovTerminalReport("task-one", "acme/app", base, false))}
			},
			tasks:   []string{"task-one"},
			wantErr: "do not prove task task-one completed",
		},
		{
			name: "failure after completion",
			paths: func(t *testing.T) []string {
				first := write(t, wtLifeCovTerminalReport("task-one", "acme/app", base, true))
				second := wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "report.json"),
					wtLifeCovTerminalReport("task-one", "acme/app", base.Add(time.Minute), false))
				return []string{first, second}
			},
			tasks:   []string{"task-one"},
			wantErr: "failed after its claimed completion",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTerminalCleanupReports(testCase.paths(t), "acme/app", testCase.tasks)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestWtLifeCovValidateTerminalCleanupReportsAcceptsRetainedHistory(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	failed := wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "failed.json"),
		wtLifeCovTerminalReport("task-one", "acme/app", base, false))
	applied := wtLifeCovWriteJSON(t, filepath.Join(t.TempDir(), "applied.json"),
		wtLifeCovTerminalReport("task-one", "acme/app", base.Add(time.Minute), true))
	if err := ValidateTerminalCleanupReports([]string{failed, applied}, "acme/app", []string{"task-one"}); err != nil {
		t.Fatalf("retained history rejected: %v", err)
	}
}

func TestWtLifeCovRequireJSONEOFClassifiesTrailingContent(t *testing.T) {
	t.Parallel()
	decoder := json.NewDecoder(strings.NewReader("{}"))
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		t.Fatalf("clean document error = %v", err)
	}

	multiple := json.NewDecoder(strings.NewReader("{} {}"))
	if err := multiple.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := requireJSONEOF(multiple); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("multiple values error = %v", err)
	}

	malformed := json.NewDecoder(strings.NewReader("{} {"))
	if err := malformed.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := requireJSONEOF(malformed); err == nil || strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("malformed trailing value error = %v", err)
	}
}

func TestWtLifeCovActiveWorkLogClaimAtPathFindsActiveClaim(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	claimID := strings.Repeat("a", 64)

	if claim, err := activeWorkLogClaimAtPath(home, worktree, nil); err != nil || claim != nil {
		t.Fatalf("missing work log root = %+v, %v", claim, err)
	}

	claims := filepath.Join(home, "worklogs", "task-one", "runs", "run-one", "claims")
	wtLifeCovWriteJSON(t, filepath.Join(claims, claimID+".json"), workLogClaim{
		Version: 1, Task: "task-one", Repository: "acme/app", Worktree: worktree, Lifecycle: "active",
	})
	found, err := activeWorkLogClaimAtPath(home, worktree, nil)
	if err != nil || found == nil || found.Task != "task-one" {
		t.Fatalf("active claim lookup = %+v, %v", found, err)
	}

	filtered, err := activeWorkLogClaimAtPath(home, worktree, map[string]bool{"other-task": true})
	if err != nil || filtered != nil {
		t.Fatalf("task-filtered lookup = %+v, %v", filtered, err)
	}

	otherWorktree, err := activeWorkLogClaimAtPath(home, filepath.Join(t.TempDir(), "elsewhere"), nil)
	if err != nil || otherWorktree != nil {
		t.Fatalf("worktree-mismatched lookup = %+v, %v", otherWorktree, err)
	}
}

func TestWtLifeCovActiveWorkLogClaimAtPathClassifiesCorruption(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	claimID := strings.Repeat("b", 64)

	if err := os.WriteFile(filepath.Join(home, "worklogs"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
		t.Fatal("file worklogs root accepted")
	}
	if err := os.Remove(filepath.Join(home, "worklogs")); err != nil {
		t.Fatal(err)
	}

	// A regular file where an effort directory belongs.
	wtLifeCovWriteJSON(t, filepath.Join(home, "worklogs", "task-one"), "not a directory\n")
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
		t.Fatal("file effort directory accepted")
	}
	if err := os.Remove(filepath.Join(home, "worklogs", "task-one")); err != nil {
		t.Fatal(err)
	}

	// An effort directory without runs/.
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "task-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
		t.Fatal("effort directory without runs accepted")
	}

	// An unsafe run name.
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "task-one", "runs", ".unsafe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil ||
		!strings.Contains(err.Error(), "unsafe Work Log run") {
		t.Fatalf("unsafe run error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(home, "worklogs", "task-one", "runs", ".unsafe")); err != nil {
		t.Fatal(err)
	}

	// A run without claims/.
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "task-one", "runs", "run-one"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
		t.Fatal("run without claims accepted")
	}

	// An unsafe claim entry name.
	claims := filepath.Join(home, "worklogs", "task-one", "runs", "run-one", "claims")
	if err := os.MkdirAll(claims, 0o700); err != nil {
		t.Fatal(err)
	}
	wtLifeCovWriteJSON(t, filepath.Join(claims, "not-a-claim.json"), workLogClaim{Version: 1})
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil ||
		!strings.Contains(err.Error(), "unsafe Work Log claim entry") {
		t.Fatalf("unsafe claim error = %v", err)
	}
	if err := os.Remove(filepath.Join(claims, "not-a-claim.json")); err != nil {
		t.Fatal(err)
	}

	// A claim that cannot be decoded.
	wtLifeCovWriteJSON(t, filepath.Join(claims, claimID+".json"), "not a claim")
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
		t.Fatal("unparseable claim accepted")
	}
}

func wtLifeCovCleanupTaskHandle(t *testing.T, root, taskDir string) *cleanupTaskHandle {
	t.Helper()
	return &cleanupTaskHandle{
		worktreesPath: root,
		taskPath:      taskDir,
		worktrees:     wtLifeCovOpenDirectory(t, root),
		task:          wtLifeCovOpenDirectory(t, taskDir),
	}
}

func TestWtLifeCovPrepareAndArchiveCleanupLifecycleArtifacts(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-one")
	stagePath := filepath.Join(taskDir, ".wb-stage-one")
	if err := os.MkdirAll(stagePath, 0o755); err != nil {
		t.Fatal(err)
	}
	handle := wtLifeCovCleanupTaskHandle(t, root, taskDir)
	defer handle.close()
	artifacts := []LifecycleArtifact{{
		Task: "task-one", WorktreesRoot: root, Path: stagePath,
		Kind: "secure_worktree_stage", State: "staging", Eligible: true,
	}}

	archive, archivePath, handles, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, artifacts)
	if err != nil {
		t.Fatalf("prepare artifacts: %v", err)
	}
	if archive == nil || archivePath == "" || len(handles) != 1 {
		t.Fatalf("prepared archive = %v (%q), handles %d", archive, archivePath, len(handles))
	}
	if err := archiveCleanupLifecycleArtifacts(handle, archive, archivePath, handles, artifacts); err != nil {
		t.Fatalf("archive artifacts: %v", err)
	}
	closeCleanupLifecycleArtifacts(handles)
	if !artifacts[0].Applied || artifacts[0].State != "archived" || artifacts[0].Disposition != "archived_empty_stage" {
		t.Fatalf("archived artifact = %+v", artifacts[0])
	}
	if artifacts[0].ArchivePath != filepath.Join(archivePath, ".wb-stage-one") {
		t.Fatalf("artifact archive path = %q", artifacts[0].ArchivePath)
	}
	if _, err := os.Stat(filepath.Join(archivePath, ".wb-stage-one")); err != nil {
		t.Fatalf("archived stage missing: %v", err)
	}
	if _, err := os.Stat(stagePath); !os.IsNotExist(err) {
		t.Fatalf("stage was not moved out of the task: %v", err)
	}
	_ = archive.Close()
}

func TestWtLifeCovPrepareCleanupLifecycleArtifactsRejectsUnsafeEntries(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-one")
	if err := os.MkdirAll(filepath.Join(taskDir, ".wb-stage-nonempty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, ".wb-stage-nonempty", "blocker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle := wtLifeCovCleanupTaskHandle(t, root, taskDir)
	defer handle.close()

	unrecognized := []LifecycleArtifact{{Path: filepath.Join(taskDir, "random-entry"), Kind: "secure_worktree_stage"}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, unrecognized); err == nil ||
		!strings.Contains(err.Error(), "lost its reserved WB identity") {
		t.Fatalf("unrecognized artifact error = %v", err)
	}

	absent := []LifecycleArtifact{{Path: filepath.Join(taskDir, ".wb-stage-absent"), Kind: "secure_worktree_stage"}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, absent); err == nil ||
		!strings.Contains(err.Error(), "open cleanup lifecycle artifact") {
		t.Fatalf("absent artifact error = %v", err)
	}

	nonEmpty := []LifecycleArtifact{{Path: filepath.Join(taskDir, ".wb-stage-nonempty"), Kind: "secure_worktree_stage"}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, nonEmpty); err == nil ||
		!strings.Contains(err.Error(), "became non-empty") {
		t.Fatalf("non-empty artifact error = %v", err)
	}

	namespace := []LifecycleArtifact{{Path: taskDir, Kind: lifecycleArtifactKindTaskNamespace}}
	archive, archivePath, handles, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, namespace)
	if err != nil || archive != nil || archivePath != "" || handles != nil {
		t.Fatalf("task namespace artifact = %v, %q, %v, %v", archive, archivePath, handles, err)
	}

	homeFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(homeFile, "reports"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(taskDir, ".wb-stage-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	stageTwo := []LifecycleArtifact{{Path: filepath.Join(taskDir, ".wb-stage-two"), Kind: "secure_worktree_stage"}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(homeFile, handle, []int{0}, stageTwo); err == nil ||
		!strings.Contains(err.Error(), "open cleanup lifecycle artifact archive") {
		t.Fatalf("unopenable archive error = %v", err)
	}
}

func TestWtLifeCovArchiveCleanupLifecycleArtifactsReportsFailures(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	taskDir := filepath.Join(root, "task-one")
	stagePath := filepath.Join(taskDir, ".wb-stage-one")
	if err := os.MkdirAll(stagePath, 0o755); err != nil {
		t.Fatal(err)
	}
	handle := wtLifeCovCleanupTaskHandle(t, root, taskDir)
	defer handle.close()
	artifacts := []LifecycleArtifact{{
		Task: "task-one", Path: stagePath, Kind: "secure_worktree_stage", State: "staging",
	}}
	archive, archivePath, handles, err := prepareCleanupLifecycleArtifacts(home, handle, []int{0}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()

	// A concurrent writer that refills the stage before retirement must be
	// surfaced, and the stage must be left where it is.
	if err := os.WriteFile(filepath.Join(stagePath, "residue"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := archiveCleanupLifecycleArtifacts(handle, archive, archivePath, handles, artifacts); err == nil ||
		!strings.Contains(err.Error(), "became non-empty at retirement boundary") {
		t.Fatalf("refilled artifact error = %v", err)
	}
	if err := os.Remove(filepath.Join(stagePath, "residue")); err != nil {
		t.Fatal(err)
	}

	// A task handle whose held task descriptor no longer matches its path must
	// refuse to run.
	drifted := &cleanupTaskHandle{
		worktreesPath: root,
		taskPath:      taskDir + "-elsewhere",
		worktrees:     handle.worktrees,
		task:          handle.task,
	}
	if err := archiveCleanupLifecycleArtifacts(drifted, archive, archivePath, handles, artifacts); err == nil ||
		!strings.Contains(err.Error(), "cleanup task path changed") {
		t.Fatalf("drifted task error = %v", err)
	}

	// A destination entry that already exists must fail the no-replace move.
	occupied := filepath.Join(archivePath, ".wb-stage-one")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := archiveCleanupLifecycleArtifacts(handle, archive, archivePath, handles, artifacts); err == nil ||
		!strings.Contains(err.Error(), "descriptor-safely archive cleanup lifecycle artifact") {
		t.Fatalf("occupied destination error = %v", err)
	}
	closeCleanupLifecycleArtifacts(handles)
}
