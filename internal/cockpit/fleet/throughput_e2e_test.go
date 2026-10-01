//go:build e2e

package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// claimIDOf is a valid claim id (64 hex digits) for n.
func claimIDOf(n int) string {
	sum := sha256.Sum256([]byte{byte(n), byte(n >> 8), byte(n >> 16)})
	return hex.EncodeToString(sum[:])
}

// sealedFile writes a terminal record the way the Work Log keeps it, under
// <home>/worklogs/<task>/runs/<run>/terminals/<claim id>.json, and returns its
// path.
func sealedFile(t *testing.T, home, task, run string, n int, record worktreeclaims.TerminalRecord) string {
	t.Helper()
	dir := filepath.Join(home, "worklogs", task, "runs", run, "terminals")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record.ClaimID = claimIDOf(n)
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, record.ClaimID+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tree lists every path under dir with its mode, size and modification time.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	listed := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		listed[path] = info.Mode().String() + "|" + info.ModTime().String() + "|" + string(rune(info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

// TestE2ELocalTerminalsListAndReadTheRealWorkLogAndWriteNothing proves the
// production source finds the sealed records of a real Work Log directory,
// skips what is not one (a symbolic link, a name that is not a claim id, a
// directory, a record over the size bound), reads a record back into
// worktreeclaims.TerminalRecord, and that listing, reading and collecting
// change nothing under the home, not even a directory's modification time.
func TestE2ELocalTerminalsListAndReadTheRealWorkLogAndWriteNothing(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	home := filepath.Join(root, ".wb")
	now := time.Now().UTC()
	landed := landedTerminal("task-a", now.Add(-5*time.Hour), now.Add(-2*time.Hour))
	landed.Repository, landed.Worktree, landed.Branch = "acme/widgets", "/secret/path/wt", "feature/a"
	path := sealedFile(t, home, "task-a", "run-1", 1, landed)
	sealedFile(t, home, "task-a", "run-2", 2, terminal("task-a", "orphaned", now.Add(-3*time.Hour), now.Add(-time.Hour)))
	sealedFile(t, home, "task-b", "run-1", 3, landedTerminal("task-b", now.Add(-9*time.Hour), now.Add(-time.Hour)))
	terminals := filepath.Dir(path)
	// What is not a terminal record: a name that is not a claim id, a
	// directory with a claim id's name, a symbolic link to a record, a corrupt
	// record and one over the bound.
	if err := os.WriteFile(filepath.Join(terminals, "notes.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(terminals, claimIDOf(10)+".json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(terminals, claimIDOf(11)+".json")); err != nil {
		t.Skipf("symbolic links are not available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(terminals, claimIDOf(12)+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(terminals, claimIDOf(13)+".json")
	if err := os.WriteFile(big, []byte(strings.Repeat(" ", maxTerminalBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symbolic link in place of an effort directory is not followed.
	if err := os.Symlink(filepath.Join(home, "worklogs", "task-b"), filepath.Join(home, "worklogs", "linked")); err != nil {
		t.Fatal(err)
	}
	// A run with no terminals directory is an unsealed run.
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "task-c", "runs", "run-1", "claims"), 0o700); err != nil {
		t.Fatal(err)
	}

	source := LocalTerminals{Homes: []string{home}}
	before := tree(t, home)
	files, truncated, err := source.List(t.Context(), 100)
	if err != nil || truncated {
		t.Fatalf("List = %v, truncated %t", err, truncated)
	}
	if len(files) != 5 {
		t.Fatalf("listed %d files, want the 5 regular claim-id records: %+v", len(files), files)
	}
	if _, cut, _ := source.List(t.Context(), 2); !cut {
		t.Error("a list over its limit did not say so")
	}
	record, err := source.Read(path)
	if err != nil || record.Disposition != "landed" || record.Task != "task-a" || record.Repository != "acme/widgets" || !record.SealedAt.Equal(landed.SealedAt) || !record.RecordedAt.Equal(landed.RecordedAt) {
		t.Errorf("Read = %+v, %v", record, err)
	}
	if _, err := source.Read(big); err == nil {
		t.Error("a record over the bound was read")
	}
	if _, err := source.Read(filepath.Join(terminals, claimIDOf(12)+".json")); err == nil {
		t.Error("a corrupt record was read")
	}
	if _, err := source.Read(terminals); err == nil {
		t.Error("a directory was read as a record")
	}
	if _, err := source.Read(filepath.Join(terminals, "absent.json")); err == nil {
		t.Error("an absent record was read")
	}
	if _, _, err := source.List(cancelled(t), 100); err == nil {
		t.Error("a cancelled list succeeded")
	}

	collector := newThroughputCollector(source, 0, 0, 0, func(string, ...any) {})
	block := collector.collect(t.Context(), now)
	collector.scanned = time.Time{}
	collector.collect(t.Context(), now)
	if block == nil || len(block.Slowest) != 2 || block.Slowest[0].Task != "task-b" || block.Slowest[0].DurationSeconds != 8*3600 {
		t.Errorf("block = %+v, want task-b then task-a", block)
	}
	if after := tree(t, home); len(after) != len(before) {
		t.Errorf("the collector created %d paths", len(after)-len(before))
	} else {
		for path, state := range before {
			if after[path] != state {
				t.Errorf("%s changed", path)
			}
		}
	}
}

func cancelled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

// TestE2ELocalTerminalsWithNoWorkLogHaveNothing covers a home with no Work Log
// and one that is not there.
func TestE2ELocalTerminalsWithNoWorkLogHaveNothing(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	source := LocalTerminals{Homes: []string{filepath.Join(root, "absent")}}
	files, truncated, err := source.List(t.Context(), 10)
	if err != nil || truncated || len(files) != 0 {
		t.Errorf("List = %v, %v, %v", files, truncated, err)
	}
}

// TestE2EThroughputScanCostOn3000Records measures the collector on a real
// directory of 3,000 sealed records (the real fleet has about 2,100 claims): a
// cold scan reads every record, a warm scan within the same pass reads none, and
// a scan after one record changed reads exactly it. The timings are logged.
func TestE2EThroughputScanCostOn3000Records(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	home := filepath.Join(root, ".wb")
	now := time.Now().UTC()
	const records = 3000
	var last string
	for n := range records {
		sealed := now.Add(-time.Duration(n) * 17 * time.Minute)
		last = sealedFile(t, home, "task-"+claimIDOf(n)[:12], "run-1", n, landedTerminal("task-"+claimIDOf(n)[:12], sealed.Add(-time.Duration(n%90)*time.Hour), sealed))
	}
	counting := &countingTerminals{TerminalRecords: LocalTerminals{Homes: []string{home}}}
	collector := newThroughputCollector(counting, 0, 5000, 0, func(string, ...any) {})
	started := time.Now()
	block := collector.collect(t.Context(), now)
	cold := time.Since(started)
	if block == nil || counting.reads != records || block.Capped {
		t.Fatalf("cold: block %+v, reads %d", block, counting.reads)
	}
	collector.scanned = time.Time{}
	started = time.Now()
	collector.collect(t.Context(), now)
	warm := time.Since(started)
	if counting.reads != records {
		t.Fatalf("warm scan read %d records again", counting.reads-records)
	}
	body := []byte("{}")
	if err := os.WriteFile(last, body, 0o600); err != nil {
		t.Fatal(err)
	}
	collector.scanned = time.Time{}
	collector.collect(t.Context(), now)
	if counting.reads != records+1 {
		t.Errorf("a changed record caused %d reads, want 1", counting.reads-records)
	}
	t.Logf("3,000 records: cold scan %v (3,000 reads), warm scan %v (0 reads), %d landed in the window", cold, warm, countLanded(block))
}

func countLanded(block *Throughput) int {
	total := 0
	for _, day := range block.PerDay {
		total += day.Landed
	}
	return total
}

// countingTerminals counts the reads of a source.
type countingTerminals struct {
	TerminalRecords
	reads int
}

func (c *countingTerminals) Read(key string) (worktreeclaims.TerminalRecord, error) {
	c.reads++
	return c.TerminalRecords.Read(key)
}

// TestE2ELocalTerminalsResolveTheirHomes: the configured home and the homes the
// projects root resolves to are read once each.
func TestE2ELocalTerminalsResolveTheirHomes(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	home := filepath.Join(root, ".wb")
	count := 0
	for _, found := range (LocalTerminals{ProjectsRoot: root, Home: home}).homes() {
		if found == home {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the home appears %d times", count)
	}
	if homes := (LocalTerminals{ProjectsRoot: root}).homes(); len(homes) == 0 || homes[0] != home {
		t.Errorf("homes from the projects root = %v, want %s first", homes, home)
	}
}
