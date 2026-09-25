# Review: task-9 PR-8 (cov-t9-pr8 @ 8168db980a1371dee9e43806a127a3e5d573f4ff), misc atomic writers -> internal/filewrite

Verdict: CHANGES REQUESTED. There are 2 blockers.

Base: origin/cov/integration 1cc1dc19. Branch: f6fe10d2, plus merge 8168db98 whose parents are f6fe10d2 and 1cc1dc19.
Diff: 43 files, +2475/-219.
Coverage was run on each package's full tests under `umask 022` (see (f)). internal/filewrite is at 100.0%.

## Blockers

### B1: The lifecyclehooks migration drops the Windows no-op directory sync
- **Files:** internal/lifecyclehooks/queue.go:494-499 (writeJSONAtomic), :528-539 (quarantineFile), and gc.go:195-200 (rewriteReceiptRecords).
- **What the original did:** all three sites called `syncDirectory(dir)` or `syncQueueDirectories(...)`. That function is platform-split:
  - syncdir_unix.go opens the directory and calls Sync;
  - syncdir_windows.go is `func syncDirectory(string) error { return nil }`, and has been since ee863d8c introduced the feature.
- **What PR-8 does:** it inlines `os.Open(dir)` + `filewrite.SyncDir(directory, inj)`. That is an unconditional `(*os.File).Sync()`. On Windows it becomes FlushFileBuffers on a directory handle opened GENERIC_READ (syscall_windows.go:375-376, fd_fsync_windows.go). Windows rejects that; this is the reason the shim exists.
- **Impact:** on Windows, writeJSONAtomic, rewriteReceiptRecords and quarantineFile now return an error after they have already published their file. That is a behaviour change with a nil Injector, not a refactor.
- **Why CI misses it:** the Windows job only runs `go test ./internal/lifecyclehooks -run '^TestWindowsTrust'` (go-ci.yml:473-475). `GOOS=windows go build ./internal/...` still passes.
- **Fix:** give the shim an Injector-taking twin and call it from all three sites:
  - syncdir_unix.go: `syncDirectoryInjected(path, inj)` does open + `filewrite.SyncDir` + close;
  - syncdir_windows.go: `func syncDirectoryInjected(string, *filewrite.Injector) error { return nil }`;
  - `syncDirectory(path)` becomes `syncDirectoryInjected(path, nil)`.
- **Precedent:** PR-7 kept session's equivalent Windows shim, and that was correct.
- **Related:** repositoryevents has the same inlining (N4). It has no Windows shim, so it is not a blocker there.

### B2: New or moved statements are left uncovered (founder decision 1)
The per-package coverprofiles were mapped against every added line; three blocks come out uncovered.

1. **internal/lifecyclehooks/queue.go:529-531**, the new `os.Open(directory)` failure branch in quarantineFile's inlined loop.
   - It is new code; the old code's equivalent failure lived in syncQueueDirectories and was covered by coverage_queue_test.go:884.
   - It goes away if B1 is fixed by routing through the shim, because the shim's open failure is already tested.
2. **internal/locallink/execports.go:405-406**, the `os.Remove(stage)` failure branch that was moved into writeLinkPendingMarker.
   - It was uncovered on base too (base execports.go:414, count 0). But it is now moved code, and it is newly cheap to reach.
   - A StepOpenOrCreate Injector with a Hook that drops a file into `stage` makes os.Remove(stage) fail with ENOTEMPTY.
3. **internal/wbconfig/remote.go:73-75**, the `encoder.Close()` failure branch whose `_ = filewrite.Close(...)` changed.
   - It was uncovered on base (remote.go:63-65, count 0).
   - An overlay probe (Skip 0..5 at StepWrite) shows yaml.v3 makes exactly 2 writes, both inside Encode, and none in Close. So this branch cannot be reached through the Writer seam.
   - **Cheapest fix:** revert that single line to `_ = temporary.Close()`, which is behaviour-identical with nil. The statement is then unchanged rather than changed-and-uncovered.
   - **Alternative:** have the coordinator explicitly accept it as a pre-existing unreachable branch.

## (a) Writer.ReadFrom
- **Correct.** The injection check runs first; on failure it returns `(0, err)` and the real ReadFrom never runs, which the test asserts via empty file content. It then delegates to `file.ReadFrom(r)` unchanged.
- **Covered:** TestWriterReadFromCopiesFromAnOsFileSource and TestWriterReadFromHonoursAnInjectedFailure. Mutant p1 (drop the check) is killed.
- **copyBuiltPackageContents** (*os.File -> *os.File): the original io.Copy went File.WriteTo -> genericWriteTo -> dst.ReadFrom(fileWithoutWriteTo) -> copy_file_range. Now writer.ReadFrom -> file.ReadFrom(fileWithoutWriteTo) is also accepted by copyFileRange (zero_copy_linux.go:102). **Byte-identical.**
- **orchestrate tar path from PR-4/PR-6** (tar.Reader has no WriteTo): it now goes writer.ReadFrom -> file.ReadFrom -> genericReadFrom, the same 32 KiB loop. That is identical to the original pre-task-9 `io.Copy(file, reader)`.

## (b) OpenAppend
- It uses `os.O_APPEND|os.O_CREATE|os.O_WRONLY` with the caller's 0o644. That is the same flag set and mode as the original ExcludePath `os.OpenFile`.
- `WriteString(line)` becomes `Write([]byte(line))`, which is the same syscall.
- Mutant p9 (drop O_APPEND) kills TestOpenAppendAppendsToAnExistingFile. Mutant p5 (site uses CreateOrTruncatePath) is killed by TestLgCovExcludePathFailurePaths.

## (c) locallink extraction and the exemption
- **Ordering and wrapping are preserved.** writeLinkPendingMarker and writeLinkSymlinkBackup keep the exact error texts, the cleanup order (Close, then remove marker, then remove stage) and the call positions:
  - the marker write stays before the deferred restore is registered;
  - the backup write stays before `os.Remove(target)`.
- **The exemption is legitimate.** An overlay that removes the new `ExecNode.linkInjected` entry makes TestNoInlineWriteSequencesOutsideFilewrite flag execports.go:448. The function's remaining primitives are:
  - MkdirAll, Mkdir, Remove, Symlink;
  - `os.Rename(target, directoryBackup)`, a directory move with the same shape as the existing `ExecNode.Unlink` entry.
  - There is no content write left.
- A whole-function permanent exemption would hide a future write added to linkInjected. That risk is the same as for every NotAFileWrite entry, so it is not raised here.

## (d) The PR-10 deferral of execfile.WriteExecutableFile
**Acceptable as scoping, but half the stated reason is weak.**
- **ForkLock is not a real obstacle.** filewrite's primitives never fork, so the caller can keep `syscall.ForkLock.RLock()` around CreateTemp, Write, ChmodFile and Close exactly as today.
- **The seam redesign is the real reason.** The `tempExecutableFile` interface plus the createTemp/rename package vars back fake-based tests that would have to be rewritten as Injector tables. PR-7 did exactly that for nodeidentity in about 100 lines, so this is an effort argument, not a blocker.
- **One genuine subtlety:** an Injector Hook would run inside the RLock, so a Hook that execs would deadlock.
- **Condition:** "PR-10" appears nowhere in spec/plans/coverage-to-100. It must be recorded (plan or task board), so task-9's Verifies clause ("zero direct ... outside the new package") is not declared met while this entry remains. The exemption text should also say that the RLock can simply stay in the caller.

## (e) Merge resolution
- `git merge-tree --write-tree f6fe10d2 1cc1dc19` shows conflicts only in filewrite.go and filewrite_boundary.go.
- I diffed the conflicted auto-merge tree fd0118fe against 8168db98:
  - **filewrite.go:** HEAD's Writer doc is kept. That is correct, because ReadFrom now exists and the integration-side text says it does not.
  - **filewrite_boundary.go:** both conflict sides are dropped. The PR-7 keys were already deleted upstream, and the PR-8 keys are deleted by this branch. That is correct.
- There are no other differences from the auto-merge. (`git show --remerge-diff` on git 2.43 printed the first-parent diff for this merge; I used merge-tree instead.)
- **Exemption deletions:** 19 Category A + 2 Category C + 2 Category E keys = the 23 sites, plus the execfile entry reworded to PR-10.

## (f) lifecyclehooks "receipt parent must not be writable by group or other users"
- **Cause:** the VM umask, combined with a test-hermeticity bug. This PR should not handle it.
- **Evidence:**
  - The VM's umask is 002.
  - `t.TempDir()` creates its per-test subdirectory with `os.Mkdir(dir, 0o777)`, so under umask 002 it is 0775.
  - ensureTrustedParent's validatePrivateDirectory correctly rejects group-writable parents (trust_unix.go:31).
- **Repro:** the same `-run 'Receipt|GC|Gc|Quarantine|Enqueue|Dispatch'` set gives 10+ FAILs under umask 002 and passes under `(umask 022; ...)`.
- **Recommendation:** worth an issue. hkCovEnv, testDispatcher and friends should `os.Chmod(root, 0o700)`, or set up the receipt parent explicitly, so the tests do not depend on the runner's umask. CI's umask 022 masks the bug.

## Stash hygiene
`git stash list` is empty (the stash is shared across all worktrees of the clone). Nothing was left behind.

## Other checks
- **Behaviour at the other 20 sites is identical with a nil Injector:**
  - CreateTemp -> os.CreateTemp; ChmodFile -> File.Chmod; ChmodPath -> os.Chmod;
  - WriteFile -> os.WriteFile; Rename -> os.Rename; CreateExclusivePath -> the same O_WRONLY|O_CREATE|O_EXCL;
  - WriteString -> Write([]byte).
  - The order of chmod/write/sync/close/rename and the dir sync is unchanged, and every error-wrap text is unchanged.
- **Tests:** fault tests assert errors.Is on a PR-8 sentinel plus no leftover temp. Chmod-preset Hook tests exist wherever CreateTemp's 0600 equals the final mode. Sites that take a mode parameter test a non-0600 mode (0644 or 0640).
- **Quality guards** (TestNoInlineWriteSequencesOutsideFilewrite, TestEveryFilewriteBoundaryExemption*, TestNoDirectExecWriteFileOutsideTestenv, TestParallelBaseline*): green.
- **Hygiene:** gofmt is clean on the changed files and go vet is clean. There are no forbidden patterns: no coverage:ignore, no build tags, no sleeps or retries, and no nolint added.
- **Mutation overlays** (scratchpad/mut8/): p1, p5, p6, p8, p9 and p10 are killed. p3, p4 and p7 survive; see N1-N3.

## Notes (non-blocking)
- **N1 locallink (p3):** dropping `os.Remove(stage)` on a pending-marker write or close failure survives. TestLinkInjectedHonoursPendingMarkerFailures never asserts that `stage` is gone. Add that assertion.
- **N2 locallink (p4):** dropping `os.Remove(symlinkBackup)` on a backup write failure survives at the Link level, because Link's deferred restore removes the backup anyway. Call writeLinkSymlinkBackup directly in a table to pin the helper's own cleanup.
- **N3 lifecyclehooks quarantine (p7):** dropping the second directory (quarantineDir) from the sync loop survives. The StepDirSync row fails on the first directory. A Name-keyed or Skip:1 row would pin both.
- **N4 repositoryevents queue.go:520-525 and receiver.go:231-236:**
  - The inlined open+SyncDir drops the original `errors.Join(syncErr, closeErr)` close error. That is practically unreachable, but it is not byte-identical in error semantics.
  - receiver.go:245 `syncDirectory` is now referenced only by zz_sdcov_receiver_test.go, which makes it dead production code kept alive by a test.
  - Suggested fix: the same shape as B1, `syncDirectoryInjected(path, inj)` keeping errors.Join, with `syncDirectory` delegating to it.
- **N5 lifecyclehooks quarantine:** the source fixture is written at 0600, so TestQuarantineFileInjectedMovesChmodsAndRecordsReason cannot tell the ChmodPath(0600) from a missing one. The StepChmod row still catches a deleted call. Writing the fixture at 0644 would pin it.
- **N6 filewrite:** no test pins that Writer implements io.ReaderFrom. Mutant evidence: deleting ReadFrom keeps both ReadFrom tests green, because they fall back through Write. A one-line `if _, ok := Writer(f, "f", nil).(io.ReaderFrom); !ok` check would pin the fast-path contract.
- **N7 Writer.ReadFrom changes injection granularity for io.Copy callers without a WriterTo source** (the orchestrate tar path): StepWrite now fires once per io.Copy instead of once per 32 KiB chunk. No existing test depends on per-chunk Skip counts (they all pass), but the Writer doc should say so.
