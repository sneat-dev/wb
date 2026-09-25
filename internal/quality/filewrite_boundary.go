// This file backs TestNoInlineWriteSequencesOutsideFilewrite (spec/plans/
// coverage-to-100 task-9): the mechanical check that a temp-file
// write -> sync -> chmod -> close -> publish (rename or link) sequence is
// implemented in exactly one place, internal/filewrite, and nowhere else.
//
// The check is call-based, not a co-occurrence heuristic: a function is a
// violation if its body directly calls one of a fixed set of publish or
// create primitives:
//
//   - publish, always banned: os.Rename, os.Link, syscall.Rename,
//     syscall.Renameat, syscall.Link, the fd-relative unix.Rename/
//     Renameat/Renameat2/RenameatxNp/Link/Linkat family, the repo-local
//     renameNoReplace helper, or a package-level alias of os.Link/os.Rename.
//   - create, always banned regardless of whether the same function goes
//     on to write content: os.CreateTemp, ioutil.TempFile, os.Create. A
//     round-2 review of task-9 PR-1 found these three reserve or open a
//     file unconditionally -- unlike os.OpenFile/unix.Openat, they cannot
//     be used for a lock-only open, so gating them behind a content-write
//     check let a real write escape through a package-level seam function
//     (internal/nodeidentity/nodeidentity.go:writeNodeIDTempFile).
//   - create, banned only when the same function also writes content:
//     os.OpenFile or unix.Openat carrying an O_CREAT-family flag. This
//     gate stays narrow because both are legitimately used for a
//     lock-file open (O_CREAT|O_EXCL followed only by Fchmod/Flock, never
//     a content write) that internal/filewrite has nothing to replace.
//   - os.WriteFile alone is not banned; it is only a violation together
//     with one of the publish primitives above in the same function,
//     since a bare os.WriteFile is an ordinary overwrite, not a temp-file
//     publish.
//
// This replaced an earlier create-and-rename-pair heuristic that missed
// every os.*-based site (the dominant shape in this repository), every
// renameNoReplace call and every package-level rename/link alias, and
// that could never match os.OpenFile at all because it looked for the
// identifier O_CREAT while the os package spells its flag O_CREATE.
//
// A violation's key is "relative/path.go:FuncName", or
// "relative/path.go:ReceiverType.FuncName" for a method -- the receiver
// type disambiguates two methods that share a name on different receivers
// in the same file (internal/streams/events.go has both
// (*FileEventLog).Append, a real O_APPEND write, and (DiscardEvents).Append,
// a no-op stub that is never flagged).
//
// Two named allow-lists carry the sites this detector finds but task-9's PR
// series has not folded into internal/filewrite yet:
//
//   - PendingMigrationExemptions: real write-then-publish (or write-once,
//     or create-only-scratch) sequences still implemented inline, each
//     naming the PR that will migrate it. The last PR in the series empties
//     this map, turning the guard below into a zero-exception gate. Per a
//     round-2 review decision, this includes every ephemeral/scratch
//     CreateTemp site (a name reserved and freed, or a file written and
//     used once by a single subprocess, never durably read back): the plan
//     the file's own package doc for internal/filewrite states the
//     Verifies goal as "zero direct temp-file write/sync/chmod/close/
//     rename sequences outside the new package", with no carve-out for
//     scratch files, so these are pending a future filewrite.CreateScratch
//     helper rather than permanently exempt.
//   - NotAFileWritePublishExemptions: sites this detector's necessarily
//     conservative rules flag, but that are not a temp-file write-and-
//     publish sequence at all -- a queue-state move, an archive/quarantine
//     move, a directory move, an append-only log write, or the
//     renameNoReplace primitive's own OS-specific implementation. These
//     never migrate to internal/filewrite, because there is nothing here
//     for it to replace. This list holds only move-only renames,
//     append-only log writes, and the renameNoReplace OS wrappers -- never
//     a create-temp-file site, since every one of those is either a real
//     write (pending migration) or a scratch file (also pending, per the
//     policy above).
//
// TestEveryFilewriteBoundaryExemptionMatchesALiveViolation asserts every
// entry in both maps still names a real, currently-detected site, so a
// stale or padded entry is caught immediately and the allow-lists can only
// shrink as sites genuinely migrate.
// TestNotAFileWritePublishExemptionsNeverAlsoCreateAndWriteContent asserts
// no NotAFileWritePublishExemptions entry -- exempted for a rename/move/
// append reason -- also independently creates a file via a gated
// OpenFile/Openat-with-create-flag call, an os.WriteFile/ioutil.WriteFile
// call, or an unconditionally-banned create primitive, and writes content
// to it; a round-2 review found internal/locallink/execports.go's Link
// function misclassified this way (exempted as "renames ... into place",
// when it also does two real O_CREATE|O_EXCL content writes before that
// rename), and a round-3 review found the same true of
// internal/lifecyclehooks/queue.go's Dispatcher.quarantineFile (renames
// the quarantined file, then os.WriteFile's a ".reason.txt" sidecar of
// its own).
// TestNotAFileWritePublishExemptionsAreRenameOnlyOrAppendOnlyNeverBoth
// asserts every remaining entry is exactly one of the list's two
// legitimate shapes -- a pure rename/move (calls a publish primitive,
// writes no content of its own) or a pure append-only log (opens
// O_APPEND and writes, calls no publish primitive) -- never neither and
// never both; a function doing both at once (rename plus an independent
// append-write) would otherwise slip past the create-and-write check
// above, since that check only looks for a *gated* create primitive and
// O_APPEND opens are deliberately excluded from it.
package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PendingMigrationExemptions lists "relative/path.go:FuncName" (or
// "relative/path.go:ReceiverType.FuncName" for a method) sites that
// implement a real temp-file write-then-publish sequence, write-once
// sequence, or scratch/name-reservation create inline, not yet routed
// through internal/filewrite. Every entry names the task-9 PR that will
// migrate it; that PR removes the entry in the same commit it lands.
var PendingMigrationExemptions = map[string]string{
	// internal/execfile predates task-9's filewrite consolidation (added by
	// task-21/#739) and is exactly PR-8's own shape: a path-based
	// CreateTemp+chmod+Rename publish with no sync call. It slots into the
	// existing PR-8 misc-atomic-writers batch alongside the other 23
	// entries below, not a separate series. PR-8's migration of this site
	// must keep syscall.ForkLock.RLock held for the temp fd's whole open
	// lifetime (see execfile.go's WriteExecutableFile doc comment for why:
	// golang/go#22315) -- internal/filewrite has no such option today, so
	// PR-8 must either add one or keep the RLock in the caller.
	"internal/execfile/execfile.go:WriteExecutableFile": "PR-8: misc-atomic-writers -- path-based CreateTemp+chmod+Rename, no sync; migration must keep syscall.ForkLock.RLock held across the temp file's open lifetime",

	// Category A: os.CreateTemp/os.OpenFile/os.WriteFile + os.Rename, all
	// in the same function (spec/plans/coverage-to-100 task-9 PR-1 review,
	// B1 inventory items 1-47).
	"internal/archiveprune/untracked.go:overwriteArchiveCleanReceipt": "PR-8: misc-atomic-writers -- CreateTemp+chmod+sync+Rename",
	"internal/checkoutmarker/checkoutmarker.go:writeFileAtomically":   "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/deps/github_actions.go:writeAtomic":                     "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/discover/local_index.go:writeLocalIndex":                "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/fleetsync/receipt.go:overwriteRemovalReceipt":           "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/githubobserver/observer.go:writeCacheEntry":             "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/landinglane/landinglane.go:writeRecord":                 "PR-8: misc-atomic-writers -- WriteFile-to-temp+Rename",
	"internal/layout/migrate.go:writeManifest":                        "PR-8: misc-atomic-writers -- WriteFile-to-temp+Rename",
	"internal/lifecyclehooks/gc.go:rewriteReceiptRecords":             "PR-8: misc-atomic-writers -- CreateTemp+chmod+sync+Rename",
	"internal/lifecyclehooks/queue.go:writeJSONAtomic":                "PR-8: misc-atomic-writers -- CreateTemp+chmod+sync+Rename",
	"internal/migrate/engine.go:Apply":                                "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/npmrelease/release.go:writeAtomic":                      "PR-8: misc-atomic-writers -- CreateTemp+chmod+Rename",
	"internal/repositoryevents/queue.go:Queue.persist":                "PR-8: misc-atomic-writers -- CreateTemp-based publish",
	"internal/repositoryevents/receiver.go:CursorStore.saveState":     "PR-8: misc-atomic-writers -- CreateTemp-based publish",
	"internal/runqueue/visibility.go:atomicWriteFile":                 "PR-8: misc-atomic-writers -- CreateTemp-based publish",
	"internal/streams/store.go:Store.writeAtomically":                 "PR-8: misc-atomic-writers -- CreateTemp-based publish",
	"internal/waitregistry/registry.go:Register":                      "PR-8: misc-atomic-writers -- WriteFile-to-temp+Rename",
	"internal/wbconfig/peers.go:SetPeersUpstream":                     "PR-8: misc-atomic-writers -- CreateTemp-based publish",
	"internal/wbconfig/remote.go:SetRemoteHub":                        "PR-8: misc-atomic-writers -- CreateTemp-based publish",

	// Category C: create-exclusive, write, sync, no publish -- the
	// write-once-immutable shape (review items 57-63, plus writeOneTimeToken
	// and MarkParked found while regenerating this inventory against the
	// call-based detector). PR-1 migrated this shape for sessionpark only.
	"internal/locallink/execports.go:ExecNode.Link":            "PR-8: misc-atomic-writers -- two OpenFile O_CREATE|O_EXCL write-once marker/backup writes ahead of a rename; not rename-only, unlike ExecNode.Unlink",
	"internal/locallink/execports.go:copyBuiltPackageContents": "PR-8: misc-atomic-writers -- OpenFile O_EXCL write-once (copy via io.Copy)",

	// Category D (round 2): create-only scratch/name-reservation temp
	// files -- created, immediately closed (some also removed) and never
	// written to. Not detected before this round, since os.CreateTemp was
	// previously gated by the same content-write check os.OpenFile/
	// unix.Openat still use; the round-2 review made os.CreateTemp
	// unconditional, which now catches these. Per the coordinator's
	// scratch-file policy (spec/plans/coverage-to-100 task-9 round-2
	// review), these are pending migration to a new filewrite.CreateScratch
	// helper, not permanently exempt as a non-write-publish site.
	"cmd/wb/coverage_ratchet.go:runChangedCoverage":          "PR-9: scratch-helper (filewrite.CreateScratch) -- reserves a unique coverage-profile path, closes and reuses it, never writes",
	"cmd/wb/fleet_default_branch.go:defaultBranchReportPath": "PR-9: scratch-helper (filewrite.CreateScratch) -- reserves a unique report path then frees it via os.Remove, never writes",
	"internal/locallink/execports.go:ExecGit.ContentHash":    "PR-9: scratch-helper (filewrite.CreateScratch) -- reserves a name for git plumbing output, closes and removes it, never writes",
	"internal/pathguard/pathguard.go:OSProbe":                "PR-9: scratch-helper (filewrite.CreateScratch) -- writability probe: create, close, remove, never writes",
	"internal/quality/coverage.go:coverageProfilePath":       "PR-9: scratch-helper (filewrite.CreateScratch) -- reserves a unique coverage-profile path, closes it, never writes",
	"internal/quality/verify.go:runShardedVerification":      "PR-9: scratch-helper (filewrite.CreateScratch) -- reserves a unique verify-coverage-profile path, closes it, never writes",

	// Category E (round 2): ephemeral scratch temp files moved here from
	// NotAFileWritePublishExemptions per the coordinator's round-2 policy
	// override -- created, written, used as one subprocess's input (gh api,
	// git diff/push, a hook template, an HTTP download), and removed in the
	// same function or its immediate caller, never durably read back. The
	// task-9 plan's Verifies goal is "zero direct temp-file write/sync/
	// chmod/close/rename sequences outside the new package" with no
	// carve-out for scratch files, so these are pending a
	// filewrite.CreateScratch helper rather than permanently exempt.
	"cmd/wb/fleet_merge_policy.go:applyClassicProtectionWithoutLinearHistory":                           "PR-9: scratch-helper (filewrite.CreateScratch) -- ephemeral temp file passed as gh api --input, removed by defer",
	"cmd/wb/fleet_merge_policy.go:applySharedRuleset":                                                   "PR-9: scratch-helper (filewrite.CreateScratch) -- ephemeral temp file passed as gh api --input, removed by defer",
	"internal/hooks/run.go:runTemplate":                                                                 "PR-9: scratch-helper (filewrite.CreateScratch) -- ephemeral temp script for one subprocess execution, removed by defer",
	"internal/orchestrate/worktree_merge.go:PrepareWorktreeMergeRevert":                                 "PR-9: scratch-helper (filewrite.CreateScratch) -- writes an ephemeral git-diff patch temp file, removed by defer",
	"internal/orchestrate/worktree_merge.go:runWorktreeMergePrePushGate":                                "PR-9: scratch-helper (filewrite.CreateScratch) -- writes an ephemeral pre-push gate input temp file, removed by defer",
	"internal/orchestrate/worktree_merge.go:writeWorktreeMergePrompt":                                   "PR-9: scratch-helper (filewrite.CreateScratch) -- writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer",
	"internal/orchestrate/worktree_merge_conflict_replacement.go:writeConflictCandidateRefreshPrompt":   "PR-9: scratch-helper (filewrite.CreateScratch) -- writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer",
	"internal/orchestrate/worktree_merge_published_forward_repair.go:writePublishedForwardRepairPrompt": "PR-9: scratch-helper (filewrite.CreateScratch) -- writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer",
	"internal/orchestrate/worktree_merge_seal.go:writeValidationFailureSealPrompt":                      "PR-9: scratch-helper (filewrite.CreateScratch) -- writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer",
	"cmd/wb/deps_policy.go:fetchPolicy":                                                                 "PR-9: scratch-helper (filewrite.CreateScratch) -- writes one HTTP download to a temp file returned to the caller for one-shot use",

	// Category F (round 3): a function that renames or moves one file but
	// also independently creates-and-writes (Dispatcher.quarantineFile) or
	// appends content of its own (ExecGit.ExcludePath) is not rename-only,
	// and moving a git exclude file is not a durable log either -- neither
	// belongs on the permanent list (round-3 review, B2/N1).
	"internal/lifecyclehooks/queue.go:Dispatcher.quarantineFile": "PR-8: misc-atomic-writers -- renames the quarantined file, then os.WriteFile's a \".reason.txt\" sidecar of its own -- not rename-only",
	"internal/locallink/execports.go:ExecGit.ExcludePath":        "PR-8: misc-atomic-writers -- O_APPEND write to a git exclude file; not a durable log, so not permanent-list append-only",
}

// NotAFileWritePublishExemptions lists "relative/path.go:FuncName" (or
// "relative/path.go:ReceiverType.FuncName" for a method) sites this
// detector's conservative call-based rules flag, but that are not a
// temp-file write-and-publish sequence: a queue-state move, an archive or
// quarantine move, a directory or lock move, an append-only log write, or
// the renameNoReplace primitive's own OS-specific implementation (which
// internal/filewrite does not yet own -- see LinkNoReplace's doc comment).
// None of these migrates to internal/filewrite, because there is no write
// sequence here for it to replace. Per a round-2 review decision, this list
// holds only move-only renames, append-only logs, and the renameNoReplace
// OS wrappers -- a create-temp-file site never belongs here, even a
// scratch one; see PendingMigrationExemptions' Category D/E.
// TestNotAFileWritePublishExemptionsNeverAlsoCreateAndWriteContent enforces
// that no entry below also independently creates and writes a file.
var NotAFileWritePublishExemptions = map[string]string{
	"internal/lifecyclehooks/queue.go:Dispatcher.recoverRunning":               "renames a queue job's state directory back to pending on recovery; not a file write",
	"internal/lifecyclehooks/queue.go:Dispatcher.claimBatch":                   "renames a queue job's state directory to claim it; not a file write",
	"internal/streams/store.go:Store.archiveLocked":                            "renames a stream's directory into an archive location; not a file write",
	"internal/locallink/execports.go:ExecNode.Unlink":                          "renames an existing backup directory back into place; not a temp-file write",
	"cmd/wb/daemon_file_bridge.go:daemonFileBridgeServer.quarantine":           "renames a request file into a quarantine directory; not a write publish",
	"internal/worktrees/worktrees.go:moveExpectedDirectoryNoReplaceAuthorized": "moves a worktree directory after an identity check; not a file write",
	"internal/worktrees/worktrees.go:moveExpectedLockNoReplace":                "moves a lock file after an identity check, without writing new content; not a file write",
	"internal/hooks/manager.go:moveExpectedManagedHookNoReplace":               "moves a managed hook after an identity check, without writing new content; not a file write",

	// The renameNoReplace primitive's own per-OS implementation: a thin
	// wrapper around Renameat2/RenameatxNp, with no write of its own.
	// internal/filewrite.LinkNoReplace and RenameNoReplace are the seam
	// callers of renameNoReplace use for fault injection; the primitive
	// itself stays where it is until the PR that folds it in (see N3 in
	// the task-9 PR-1 review).
	"internal/hooks/rename_noreplace_darwin.go:renameNoReplace": "OS-specific renameNoReplace syscall wrapper, not a write sequence",
	"internal/hooks/rename_noreplace_linux.go:renameNoReplace":  "OS-specific renameNoReplace syscall wrapper, not a write sequence",

	// Append-only log writes: an O_APPEND descriptor with a flock (or a
	// bare append), never a temp name, never a rename or link. There is no
	// create/write/publish sequence here for internal/filewrite to replace.
	"internal/agentguard/gh.go:recordGhPrMergeOverride": "O_APPEND log write, not a create/publish sequence",
	"internal/hooks/metrics.go:AppendEvents":            "O_APPEND log write, not a create/publish sequence",
	"internal/runlog/runlog.go:Append":                  "O_APPEND log write (flock-guarded), not a create/publish sequence",
	"internal/streams/events.go:FileEventLog.Append":    "O_APPEND log write (flock-guarded), not a create/publish sequence",
	"internal/lifecyclehooks/queue.go:appendReceipt":    "O_APPEND log write (flock-guarded), not a create/publish sequence",
}

// InlineWriteSequenceViolation names one function outside
// internal/filewrite whose body directly calls a publish or create
// primitive spec/plans/coverage-to-100 task-9 consolidates into
// internal/filewrite, and is not exempted by either allow-list passed to
// FindInlineWriteSequences.
type InlineWriteSequenceViolation struct {
	// File is the path relative to root, slash-separated.
	File string
	// Func is the offending function's name, or "ReceiverType.Name" for a
	// method.
	Func string
	// Line is the function declaration's line number, for a human
	// reading the failure to jump straight to it.
	Line int
}

func (v InlineWriteSequenceViolation) String() string {
	return fmt.Sprintf("%s:%d: func %s calls a create/write/publish primitive directly, outside internal/filewrite", v.File, v.Line, v.Func)
}

// filewriteBoundaryExcludedDirs are module-relative, slash-separated
// directories FindInlineWriteSequences never scans: internal/filewrite
// itself (the seam these calls belong in) and internal/unixcompat (the
// cross-platform syscall shim internal/filewrite and everything else is
// itself built on -- it is the primitive layer, not a second
// implementation of anything internal/filewrite provides).
var filewriteBoundaryExcludedDirs = []string{
	"internal/filewrite",
	"internal/unixcompat",
}

// FindInlineWriteSequences walks root (a module root, typically
// ParallelGuardModuleRoot's result) and reports every non-test Go function
// outside internal/filewrite and internal/unixcompat whose body directly
// calls a publish primitive (os.Rename, os.Link, syscall.Rename,
// syscall.Renameat, syscall.Link, the fd-relative unix.Rename/Renameat/
// Renameat2/RenameatxNp/Link/Linkat family, the repo-local renameNoReplace
// helper, or a package-level alias of os.Link/os.Rename), a create
// primitive banned unconditionally (os.CreateTemp, ioutil.TempFile,
// os.Create), a create primitive banned only alongside a content write
// (os.OpenFile or unix.Openat carrying an O_CREAT-family flag), or
// os.WriteFile together with one of the publish primitives above in the
// same function -- skipping any file:func listed in pendingMigration or
// notAFileWritePublish. Callers pass PendingMigrationExemptions and
// NotAFileWritePublishExemptions for the real guard; a test may pass its
// own maps (or nil) to exercise the detector without touching package
// state, which keeps every test in this file parallel-safe.
func FindInlineWriteSequences(root string, pendingMigration, notAFileWritePublish map[string]string) ([]InlineWriteSequenceViolation, error) {
	fset := token.NewFileSet()
	var violations []InlineWriteSequenceViolation
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" || (strings.HasPrefix(base, ".") && base != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := relSlashFor(root, path)
		for _, excluded := range filewriteBoundaryExcludedDirs {
			if rel == excluded || strings.HasPrefix(rel, excluded+"/") {
				return nil
			}
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		aliases := fileRenameLinkAliases(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !functionHasInlineWriteSequence(fn.Body, aliases) {
				continue
			}
			funcName := qualifiedFuncName(fn)
			key := rel + ":" + funcName
			if _, exempt := pendingMigration[key]; exempt {
				continue
			}
			if _, exempt := notAFileWritePublish[key]; exempt {
				continue
			}
			violations = append(violations, InlineWriteSequenceViolation{
				File: rel,
				Func: funcName,
				Line: fset.Position(fn.Pos()).Line,
			})
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Func < violations[j].Func
	})
	return violations, nil
}

// qualifiedFuncName returns fn's name, prefixed with its receiver's type
// name and a "." when fn is a method -- e.g. "FileEventLog.Append" -- so
// two methods with the same name on different receivers in the same file
// never collide under a single "file:func" exemption key.
func qualifiedFuncName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		if t := receiverTypeName(fn.Recv.List[0].Type); t != "" {
			return t + "." + fn.Name.Name
		}
	}
	return fn.Name.Name
}

// receiverTypeName extracts the bare type name from a method receiver's
// type expression, unwrapping a pointer receiver (*T) and a generic
// receiver's instantiation (T[P]) to their base identifier. It returns ""
// for a shape it does not recognise, in which case qualifiedFuncName falls
// back to the bare function name.
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return ""
	}
}

// relSlashFor renders path as a slash-separated path relative to root,
// falling back to path itself when it cannot be made relative (mixed
// absolute/relative inputs, which never occurs through
// FindInlineWriteSequences's own filepath.Walk but is exercised directly
// by a unit test) -- the same fallback packageDirFor uses.
func relSlashFor(root, path string) string {
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}
	return filepath.ToSlash(rel)
}

// fileRenameLinkAliases scans file's package-level var and const
// declarations for a single-value assignment of exactly os.Link or
// os.Rename (the "var linkX = os.Link" shape this repository's
// internal/orchestrate package uses repeatedly) and returns the set of
// declared names that alias one of them, so functionHasInlineWriteSequence
// can recognise a call to the alias as a publish call.
func fileRenameLinkAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.VAR && gen.Tok != token.CONST) {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != len(value.Values) {
				continue
			}
			for i, name := range value.Names {
				sel, ok := value.Values[i].(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "os" {
					continue
				}
				if sel.Sel.Name == "Link" || sel.Sel.Name == "Rename" {
					aliases[name.Name] = true
				}
			}
		}
	}
	return aliases
}

// inlineWriteSequenceClassification is the shared result of scanning one
// function body for the primitives this file bans, split into the two
// independent signals FindInlineWriteSequences and
// functionCreatesAndWritesContentIgnoringPublish each need: whether the
// body calls a publish primitive (always a violation on its own), and
// whether it independently creates a file via a gated create primitive
// and writes content to it (a violation only via this second signal, and
// the signal TestNotAFileWritePublishExemptionsNeverAlsoCreateAndWriteContent
// checks in isolation from any publish call in the same function).
type inlineWriteSequenceClassification struct {
	publishBanned bool
	alwaysBanned  bool
	createBanned  bool
	// createExclOrTruncBanned is like createBanned, but excludes an
	// os.OpenFile/unix.Openat call whose flags also mention O_APPEND (an
	// ever-growing append log, never a temp-file publish target) and is
	// also set whenever alwaysBanned is (os.CreateTemp/ioutil.TempFile/
	// os.Create can never append). Only
	// functionCreatesAndWritesContentIgnoringPublish uses this: it must
	// not mistake a legitimate O_APPEND log write (which also happens to
	// create the file on first use) for the write-once/write-then-publish
	// shape the round-2 review's B1 finding was about.
	createExclOrTruncBanned bool
	hasContentWrite         bool
	// appendFlagSeen reports whether any os.OpenFile/unix.Openat call in
	// the function mentions an O_APPEND-family flag, regardless of
	// whether a create flag or a content write is also present. Only
	// functionRenameOnlyOrAppendOnly uses this, to classify a
	// NotAFileWritePublishExemptions entry as append-only.
	appendFlagSeen bool
}

// classifyInlineWriteSequence walks body once and reports every signal
// functionHasInlineWriteSequence and functionCreatesAndWritesContentIgnoringPublish
// need. aliases is the file's package-level os.Link/os.Rename alias set,
// from fileRenameLinkAliases (pass nil when publish-alias detection is not
// needed, e.g. from a caller that only wants the create-and-write signal).
func classifyInlineWriteSequence(body *ast.BlockStmt, aliases map[string]bool) inlineWriteSequenceClassification {
	var c inlineWriteSequenceClassification
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			pkg, ok := fn.X.(*ast.Ident)
			pkgName := ""
			if ok {
				pkgName = pkg.Name
			}
			switch {
			case pkgName == "os" && (fn.Sel.Name == "Rename" || fn.Sel.Name == "Link"):
				c.publishBanned = true
			case pkgName == "syscall" && (fn.Sel.Name == "Rename" || fn.Sel.Name == "Renameat" || fn.Sel.Name == "Link"):
				c.publishBanned = true
			case pkgName == "unix" && inlineWriteSequencePublishNames[fn.Sel.Name]:
				c.publishBanned = true
			case pkgName == "os" && fn.Sel.Name == "CreateTemp":
				c.alwaysBanned = true
				c.createExclOrTruncBanned = true
			case pkgName == "ioutil" && fn.Sel.Name == "TempFile":
				c.alwaysBanned = true
				c.createExclOrTruncBanned = true
			case pkgName == "os" && fn.Sel.Name == "Create":
				c.alwaysBanned = true
				c.createExclOrTruncBanned = true
			case pkgName == "os" && fn.Sel.Name == "WriteFile":
				c.hasContentWrite = true
				// os.WriteFile creates-or-truncates the named file and
				// writes its content in one call -- it is a
				// create-and-write in its own right, not only when paired
				// with a publish call. createExclOrTruncBanned (not
				// createBanned) is deliberately the only field this sets:
				// functionHasInlineWriteSequence's own bare-WriteFile
				// carve-out (an ordinary overwrite is not itself a
				// violation) stays intact, but
				// functionCreatesAndWritesContentIgnoringPublish must
				// still catch a function exempted for a rename/move
				// reason that separately creates-and-writes a *different*
				// file via os.WriteFile (round-3 review: internal/
				// lifecyclehooks/queue.go:Dispatcher.quarantineFile writes
				// a ".reason.txt" sidecar this way, alongside its
				// unrelated os.Rename of the quarantined file itself).
				c.createExclOrTruncBanned = true
			case pkgName == "ioutil" && fn.Sel.Name == "WriteFile":
				c.hasContentWrite = true
				c.createExclOrTruncBanned = true
			case (pkgName == "os" && fn.Sel.Name == "OpenFile") || (pkgName == "unix" && fn.Sel.Name == "Openat"):
				if callArgsMentionAppendFlag(call.Args) {
					c.appendFlagSeen = true
				}
				if callArgsMentionCreateFlag(call.Args) {
					c.createBanned = true
					if !callArgsMentionAppendFlag(call.Args) {
						c.createExclOrTruncBanned = true
					}
				}
			case inlineWriteSequenceContentWriteNames[fn.Sel.Name]:
				c.hasContentWrite = true
			}
		case *ast.Ident:
			if fn.Name == "renameNoReplace" || aliases[fn.Name] {
				c.publishBanned = true
			}
		}
		return true
	})
	return c
}

// functionHasInlineWriteSequence reports whether body directly calls a
// publish primitive this package always bans (os.Rename, os.Link,
// syscall.Rename/Renameat/Link, the unix.Rename/Renameat/Renameat2/
// RenameatxNp/Link/Linkat family, renameNoReplace, or a package-level
// alias of os.Link/os.Rename), a create primitive this package always
// bans regardless of a content write (os.CreateTemp, ioutil.TempFile,
// os.Create), or creates a file with an O_CREAT-family flag via
// os.OpenFile/unix.Openat AND also writes content to it in the same
// function -- the second half of that last condition is what tells a
// genuine write-then-publish or write-once sequence apart from a plain
// lock-file open (O_CREAT|O_EXCL followed only by Fchmod/Flock, with no
// content ever written), which is not a case this package's write
// primitives have anything to offer. aliases is the file's package-level
// os.Link/os.Rename alias set, from fileRenameLinkAliases.
func functionHasInlineWriteSequence(body *ast.BlockStmt, aliases map[string]bool) bool {
	c := classifyInlineWriteSequence(body, aliases)
	if c.publishBanned || c.alwaysBanned {
		return true
	}
	return c.createBanned && c.hasContentWrite
}

// functionCreatesAndWritesContentIgnoringPublish reports whether body
// creates a file via a gated create primitive (os.OpenFile or
// unix.Openat carrying an O_CREAT-family flag) AND writes content to it,
// regardless of whether the same function also calls a publish primitive.
// This is deliberately narrower than functionHasInlineWriteSequence: it
// exists only so TestNotAFileWritePublishExemptionsNeverAlsoCreateAndWriteContent
// can catch a function exempted in NotAFileWritePublishExemptions for a
// rename/move/append reason that also independently does a real
// create-and-write, which functionHasInlineWriteSequence alone would mask
// behind the publish call (a round-2 review finding:
// internal/locallink/execports.go's Link function was exempted as
// "renames ... into place", but does two O_CREATE|O_EXCL content writes
// of its own before that rename).
func functionCreatesAndWritesContentIgnoringPublish(body *ast.BlockStmt) bool {
	c := classifyInlineWriteSequence(body, nil)
	return c.createExclOrTruncBanned && c.hasContentWrite
}

// functionRenameOnlyOrAppendOnly classifies body into exactly one of two
// shapes NotAFileWritePublishExemptions is allowed to hold (round-3
// review, N2): "rename-only" (calls a publish primitive, and writes no
// content of its own -- the content was already written elsewhere; this
// covers every move/quarantine/archive entry and the renameNoReplace OS
// wrappers, which call nothing else at all) or "append-only" (opens with
// an O_APPEND-family flag and writes content, and never calls a publish
// primitive -- an ever-growing log). ok is false when body is neither (a
// function that writes no content and calls no publish primitive is not
// a candidate for this list at all) or, more importantly, both (a
// function that both renames something and also independently appends
// content of its own -- exactly the shape N2 exists to catch, since
// renameOnly requires no content write of its own and appendOnly
// requires no publish call, so a function doing both satisfies neither
// and ok is false).
func functionRenameOnlyOrAppendOnly(body *ast.BlockStmt) (renameOnly, appendOnly, ok bool) {
	c := classifyInlineWriteSequence(body, nil)
	renameOnly = c.publishBanned && !c.hasContentWrite
	appendOnly = c.appendFlagSeen && c.hasContentWrite && !c.publishBanned
	return renameOnly, appendOnly, renameOnly != appendOnly
}

// walkNonTestFunctionDecls walks root the same way FindInlineWriteSequences
// does (same file/dir skip rules, same excluded directories) and calls
// visit for every non-test top-level function declaration found, with rel
// the file's slash-separated path relative to root. It exists so a second
// (or third) detector predicate over the same repository does not need to
// re-implement FindInlineWriteSequences's own walk and skip rules.
func walkNonTestFunctionDecls(root string, visit func(rel string, fn *ast.FuncDecl)) error {
	fset := token.NewFileSet()
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" || (strings.HasPrefix(base, ".") && base != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := relSlashFor(root, path)
		for _, excluded := range filewriteBoundaryExcludedDirs {
			if rel == excluded || strings.HasPrefix(rel, excluded+"/") {
				return nil
			}
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			visit(rel, fn)
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walk %s: %w", root, walkErr)
	}
	return nil
}

// findFunctionsThatCreateAndWriteContent returns the set of "file:func"
// (or "file:ReceiverType.func") keys for which
// functionCreatesAndWritesContentIgnoringPublish is true. It is used only
// by TestNotAFileWritePublishExemptionsNeverAlsoCreateAndWriteContent.
func findFunctionsThatCreateAndWriteContent(root string) (map[string]bool, error) {
	keys := map[string]bool{}
	err := walkNonTestFunctionDecls(root, func(rel string, fn *ast.FuncDecl) {
		if functionCreatesAndWritesContentIgnoringPublish(fn.Body) {
			keys[rel+":"+qualifiedFuncName(fn)] = true
		}
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// findFunctionsNotRenameOnlyOrAppendOnly returns the set of "file:func"
// (or "file:ReceiverType.func") keys for which functionRenameOnlyOrAppendOnly
// reports ok == false -- i.e. every function that is neither a pure
// rename/move nor a pure append-only log, including one that is both at
// once (round-3 review, N2). It is used only by
// TestNotAFileWritePublishExemptionsAreRenameOnlyOrAppendOnlyNeverBoth.
func findFunctionsNotRenameOnlyOrAppendOnly(root string) (map[string]bool, error) {
	keys := map[string]bool{}
	err := walkNonTestFunctionDecls(root, func(rel string, fn *ast.FuncDecl) {
		if _, _, ok := functionRenameOnlyOrAppendOnly(fn.Body); !ok {
			keys[rel+":"+qualifiedFuncName(fn)] = true
		}
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// inlineWriteSequencePublishNames are the unix-package (internal/
// unixcompat) selector names that publish a file by rename or hard link.
var inlineWriteSequencePublishNames = map[string]bool{
	"Rename":      true,
	"Renameat":    true,
	"Renameat2":   true,
	"RenameatxNp": true,
	"Link":        true,
	"Linkat":      true,
}

// inlineWriteSequenceContentWriteNames are method names that indicate a
// function is writing content to a file it opened, distinguishing a real
// write sequence from a plain lock-file open with no content written.
// Copy covers io.Copy(destinationFile, source), the shape
// internal/worktrees/branches_cleanup.go:copyFileSHA256 uses to write a
// verified copy through an O_EXCL descriptor.
var inlineWriteSequenceContentWriteNames = map[string]bool{
	"Write":       true,
	"WriteString": true,
	"WriteAt":     true,
	"Fprint":      true,
	"Fprintf":     true,
	"Fprintln":    true,
	"Encode":      true,
	"Copy":        true,
}

// callArgsMentionCreateFlag reports whether any argument expression
// mentions an identifier or selector whose name contains "O_CREAT" --
// covering both unix.O_CREAT and os.O_CREATE (which contains "O_CREAT" as
// a prefix), unlike an earlier version of this check that compared for
// exact equality with "O_CREAT" and so could never match os.OpenFile's own
// flag spelling.
func callArgsMentionCreateFlag(args []ast.Expr) bool {
	return callArgsMentionFlagSubstring(args, "O_CREAT")
}

// callArgsMentionAppendFlag reports whether any argument expression
// mentions an identifier or selector whose name contains "O_APPEND" --
// covering both unix.O_APPEND and os.O_APPEND. It distinguishes an
// ever-growing append log (O_APPEND, never a temp-file publish target,
// still legitimately exempt in NotAFileWritePublishExemptions) from a
// genuine write-once-then-optionally-publish open (O_EXCL or O_TRUNC),
// for functionCreatesAndWritesContentIgnoringPublish only.
func callArgsMentionAppendFlag(args []ast.Expr) bool {
	return callArgsMentionFlagSubstring(args, "O_APPEND")
}

// callArgsMentionFlagSubstring reports whether any argument expression
// mentions an identifier or selector whose name contains substr.
func callArgsMentionFlagSubstring(args []ast.Expr, substr string) bool {
	found := false
	for _, arg := range args {
		ast.Inspect(arg, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.Ident:
				if strings.Contains(e.Name, substr) {
					found = true
				}
			case *ast.SelectorExpr:
				if strings.Contains(e.Sel.Name, substr) {
					found = true
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
