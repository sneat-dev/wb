// This file backs TestNoInlineWriteSequencesOutsideFilewrite (spec/plans/
// coverage-to-100 task-9): the mechanical check that a temp-file
// write -> sync -> chmod -> close -> publish (rename or link) sequence is
// implemented in exactly one place, internal/filewrite, and nowhere else.
//
// The check is call-based, not a co-occurrence heuristic: a function is a
// violation if its body directly calls one of a fixed set of publish or
// create primitives (os.Rename, os.Link, syscall.Rename, the fd-relative
// unix.Renameat/Renameat2/RenameatxNp/Linkat family, the repo-local
// renameNoReplace helper or a package-level alias of os.Link/os.Rename,
// os.CreateTemp, ioutil.TempFile, or os.OpenFile/unix.Openat carrying an
// O_CREAT-family flag) -- with one narrower exception: a bare os.WriteFile
// call is only a violation when the same function also calls one of the
// publish primitives above, since os.WriteFile alone is an ordinary
// overwrite, not a temp-file publish. This replaces an earlier
// create-and-rename-pair heuristic that missed every os.*-based site (the
// dominant shape in this repository), every renameNoReplace call and every
// package-level rename/link alias, and that could never match os.OpenFile
// at all because it looked for the identifier O_CREAT while the os package
// spells its flag O_CREATE.
//
// Two named allow-lists carry the sites this detector finds but task-9's PR
// series has not folded into internal/filewrite yet:
//
//   - PendingMigrationExemptions: real write-then-publish (or write-once)
//     sequences still implemented inline, each naming the PR that will
//     migrate it. The last PR in the series empties this map, turning the
//     guard below into a zero-exception gate.
//   - NotAFileWritePublishExemptions: sites this detector's necessarily
//     conservative rules flag, but that are not a temp-file write-and-
//     publish sequence at all -- a queue-state move, an archive/quarantine
//     move, a directory move, or the renameNoReplace primitive's own OS-
//     specific implementation. These never migrate to internal/filewrite,
//     because there is nothing here for it to replace.
//
// TestEveryFilewriteBoundaryExemptionMatchesALiveViolation asserts every
// entry in both maps still names a real, currently-detected site, so a
// stale or padded entry is caught immediately and the allow-lists can only
// shrink as sites genuinely migrate.
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

// PendingMigrationExemptions lists "relative/path.go:FuncName" sites that
// implement a real temp-file write-then-publish (or write-once) sequence
// inline, not yet routed through internal/filewrite. Every entry names the
// task-9 PR that will migrate it; that PR removes the entry in the same
// commit it lands.
var PendingMigrationExemptions = map[string]string{
	"internal/worktrees/worklog.go:writeBytesImmutableAt":   "cov-t9-cutover: rename-based immutable publish (via renameNoReplace), not yet migrated to internal/filewrite",
	"internal/worktrees/worklog.go:writeBytesAtomicAt":      "cov-t9-cutover: fd-relative rename-based atomic write, not yet migrated to internal/filewrite",
	"internal/worktrees/worklog.go:writeBytesAtomic":        "cov-t9-cutover: path-based twin of writeBytesAtomicAt, not yet migrated to internal/filewrite",
	"internal/sessionlaunch/state.go:publishLaunchArtifact": "cov-t9-cutover: link-based immutable publish, not yet migrated to internal/filewrite",
	"internal/hooks/manager.go:writeExecutableAt":           "cov-t9-cutover: fd-relative create+chmod+write+renameNoReplace publish, not yet migrated to internal/filewrite",

	// Category A: os.CreateTemp/os.OpenFile/os.WriteFile + os.Rename, all
	// in the same function (spec/plans/coverage-to-100 task-9 PR-1 review,
	// B1 inventory items 1-47).
	"cmd/wb/daemon.go:writeLifecycleOwnerPID":                                                                                  "cov-t9-cutover: path-based CreateTemp+chmod+sync+Rename, not yet migrated",
	"cmd/wb/daemon_file_bridge.go:writeDaemonFileEnvelope":                                                                     "cov-t9-cutover: OpenFile+write+sync+Rename, not yet migrated",
	"cmd/wb/daemon_process_darwin.go:startDaemonProcess":                                                                       "cov-t9-cutover: CreateTemp plist+chmod+Rename, not yet migrated",
	"cmd/wb/fleet_default_branch.go:persistDefaultBranchReport":                                                                "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"cmd/wb/hooks_agent.go:writeSettingsAtomically":                                                                            "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"cmd/wb/peers.go:savePeerUpstreamState":                                                                                    "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"cmd/wb/sync_report.go:writeSyncIssuesFile":                                                                                "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/agents/run.go:Save":                                                                                              "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/archiveprune/untracked.go:overwriteArchiveCleanReceipt":                                                          "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/checkoutmarker/checkoutmarker.go:writeFileAtomically":                                                            "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/daemon/lifecycle.go:Save":                                                                                        "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/daemon/service.go:persistRecord":                                                                                 "cov-t9-cutover: OpenFile+sync+Rename, not yet migrated",
	"internal/deps/github_actions.go:writeAtomic":                                                                              "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/discover/local_index.go:writeLocalIndex":                                                                         "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/fleetsync/receipt.go:overwriteRemovalReceipt":                                                                    "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/githubobserver/observer.go:writeCacheEntry":                                                                      "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/hooks/pushtier_prlookup.go:savePRStatusCache":                                                                    "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",
	"internal/landinglane/landinglane.go:writeRecord":                                                                          "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",
	"internal/layout/migrate.go:writeManifest":                                                                                 "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",
	"internal/lifecyclehooks/gc.go:rewriteReceiptRecords":                                                                      "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/lifecyclehooks/queue.go:writeJSONAtomic":                                                                         "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/mergeack/mergeack.go:Persist":                                                                                    "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/migrate/engine.go:Apply":                                                                                         "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/npmrelease/release.go:writeAtomic":                                                                               "cov-t9-cutover: CreateTemp+chmod+Rename, not yet migrated",
	"internal/orchestrate/worktree_merge.go:persistWorktreeMergeReceipt":                                                       "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistPreparedWorktreeMergeRebatch":                                           "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistLandedFailureAcknowledgement":                                           "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistValidationFailureSupersession":                                          "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/orchestrate/worktree_merge_retired_publication.go:persistRetiredPublicationAcknowledgement":                      "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/orchestrate/worktree_merge_stranded.go:persistStrandedLandingAcknowledgement":                                    "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/orchestrate/worktree_merge_unpublished_validation_failure.go:persistUnpublishedValidationFailureAcknowledgement": "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/quality/go_test_shards.go:writeCoverageProfileAtomically":                                                        "cov-t9-cutover: CreateTemp+chmod+sync+Rename, not yet migrated",
	"internal/quality/validation_cache.go:SaveValidationCache":                                                                 "cov-t9-cutover: CreateTemp+sync+Rename, not yet migrated",
	"internal/repositoryevents/queue.go:persist":                                                                               "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/repositoryevents/receiver.go:saveState":                                                                          "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/runqueue/visibility.go:atomicWriteFile":                                                                          "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/streams/store.go:writeAtomically":                                                                                "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/waitregistry/registry.go:Register":                                                                               "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",
	"internal/wbconfig/peers.go:SetPeersUpstream":                                                                              "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/wbconfig/remote.go:SetRemoteHub":                                                                                 "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/worktrees/branches_quarantine.go:writeQuarantineReport":                                                          "cov-t9-cutover: OpenFile+sync+Rename, not yet migrated",
	"internal/worktrees/lifecycle.go:writeCleanupReport":                                                                       "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",
	"internal/worktrees/rename.go:writeRenameReport":                                                                           "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",
	"internal/worktrees/retire.go:writeRetireReport":                                                                           "cov-t9-cutover: CreateTemp-based publish, not yet migrated",
	"internal/worktrees/stage_recovery.go:writeRetiredStageReceipt":                                                            "cov-t9-cutover: WriteFile-to-temp+Rename, not yet migrated",

	// Category B: publish through a package-level os.Link alias, or the
	// write and the publish split across functions (review items 48-56).
	"internal/orchestrate/worktree_merge_ack.go:persistReceiptCollisionAcknowledgement":        "cov-t9-cutover: publishes via package-var alias linkReceiptCollisionAcknowledgement = os.Link, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistConflictCandidateAdvance":               "cov-t9-cutover: publishes via package-var alias linkConflictCandidateAdvance = os.Link, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistLegacyValidationFailureIdentity":        "cov-t9-cutover: publishes via package-var alias linkLegacyValidationFailureIdentity = os.Link, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistLegacyConflictIdentity":                 "cov-t9-cutover: publishes via package-var alias linkLegacyConflictIdentity = os.Link, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistMissingCleanupAcknowledgement":          "cov-t9-cutover: publishes via package-var alias linkMissingCleanupAcknowledgement = os.Link, not yet migrated",
	"internal/orchestrate/worktree_merge_ack.go:persistSelfSupersessionCorrection":             "cov-t9-cutover: publishes via package-var alias linkSelfSupersessionCorrection = os.Link, not yet migrated",
	"internal/orchestrate/worktree_merge_adopt_published.go:persistPublishedCandidateAdoption": "cov-t9-cutover: publishes via package-var alias linkPublishedCandidateAdoption = os.Link, not yet migrated",
	"internal/worktrees/branches_cleanup.go:writeBranchCleanupReport":                          "cov-t9-cutover: writeDurableFile(temporary) then os.Rename, not yet migrated",
	"internal/nodeidentity/nodeidentity.go:publishNodeID":                                      "cov-t9-cutover: publishes via os.Link; its temp-file half writeNodeIDTempFile uses package-var seams fileChmod/fileWriteString/fileSync/fileClose (not independently detected, migrates in the same PR), not yet migrated",

	// Category C: create-exclusive, write, sync, no publish -- the
	// write-once-immutable shape (review items 57-63, plus writeOneTimeToken
	// and MarkParked found while regenerating this inventory against the
	// call-based detector). PR-1 migrated this shape for sessionpark only.
	"cmd/wb/daemon_file_bridge.go:daemonFileBridgeKey":                   "cov-t9-cutover: OpenFile O_CREATE|O_EXCL write-once, not yet migrated",
	"cmd/wb/peers.go:writeOneTimeToken":                                  "cov-t9-cutover: OpenFile O_EXCL write-once (one-time token), not yet migrated",
	"cmd/wb/remote_enroll.go:writePrivateCredential":                     "cov-t9-cutover: chmod+OpenFile O_EXCL write-once, not yet migrated",
	"cmd/wb/verify_receipt.go:writeGraduationReceipt":                    "cov-t9-cutover: OpenFile O_EXCL write-once, not yet migrated",
	"internal/retiredcandidateack/ack.go:Persist":                        "cov-t9-cutover: OpenFile O_EXCL write-once, not yet migrated",
	"internal/session/session.go:MarkParked":                             "cov-t9-cutover: OpenFile O_EXCL write-once (parked lifecycle marker), not yet migrated",
	"internal/session/session.go:MarkResumed":                            "cov-t9-cutover: OpenFile O_EXCL write-once, not yet migrated",
	"internal/worktrees/branches_cleanup.go:writeDurableFile":            "cov-t9-cutover: OpenFile O_EXCL write-once (also used non-temp), not yet migrated",
	"internal/worktrees/branches_cleanup.go:copyFileSHA256":              "cov-t9-cutover: OpenFile O_EXCL write-once (copy via io.Copy), not yet migrated",
	"internal/locallink/execports.go:copyBuiltPackageContents":           "cov-t9-cutover: OpenFile O_EXCL write-once (copy via io.Copy), not yet migrated",
	"internal/worktrees/retire.go:retireCaptureFile":                     "cov-t9-cutover: OpenFile O_EXCL write-once (copy via io.Copy), not yet migrated",
	"internal/orchestrate/worktree_merge.go:extractWorktreeMergeArchive": "cov-t9-cutover: OpenFile O_CREATE|O_TRUNC write via io.Copy for each archive entry, not yet migrated",
}

// NotAFileWritePublishExemptions lists "relative/path.go:FuncName" sites
// this detector's conservative call-based rules flag, but that are not a
// temp-file write-and-publish sequence: a queue-state move, an archive or
// quarantine move, a directory or lock move, or the renameNoReplace
// primitive's own OS-specific implementation (which internal/filewrite
// does not yet own -- see LinkNoReplace's doc comment). None of these
// migrates to internal/filewrite, because there is no write sequence here
// for it to replace.
var NotAFileWritePublishExemptions = map[string]string{
	"internal/lifecyclehooks/queue.go:recoverRunning":                          "renames a queue job's state directory back to pending on recovery; not a file write",
	"internal/lifecyclehooks/queue.go:claimBatch":                              "renames a queue job's state directory to claim it; not a file write",
	"internal/lifecyclehooks/queue.go:quarantineFile":                          "renames (moves) a file into a quarantine directory; not a write publish",
	"internal/streams/store.go:archiveLocked":                                  "renames a stream's directory into an archive location; not a file write",
	"internal/locallink/execports.go:Link":                                     "renames an existing package directory into place as part of a link/backup dance; not a temp-file write",
	"internal/locallink/execports.go:Unlink":                                   "renames an existing backup directory back into place; not a temp-file write",
	"cmd/wb/daemon_file_bridge.go:quarantine":                                  "renames a request file into a quarantine directory; not a write publish",
	"internal/worktrees/worktrees.go:moveExpectedDirectoryNoReplaceAuthorized": "moves a worktree directory after an identity check; not a file write",
	"internal/worktrees/worktrees.go:moveExpectedLockNoReplace":                "moves a lock file after an identity check, without writing new content; not a file write",
	"internal/hooks/manager.go:moveExpectedManagedHookNoReplace":               "moves a managed hook after an identity check, without writing new content; not a file write",

	// The renameNoReplace primitive's own per-OS implementation: a thin
	// wrapper around Renameat2/RenameatxNp, with no write of its own.
	// internal/filewrite.LinkNoReplace and RenameNoReplace are the seam
	// callers of renameNoReplace use for fault injection; the primitive
	// itself stays where it is until the PR that folds it in (see N3 in
	// the task-9 PR-1 review).
	"internal/worktrees/rename_noreplace_darwin.go:renameNoReplace": "OS-specific renameNoReplace syscall wrapper, not a write sequence",
	"internal/worktrees/rename_noreplace_linux.go:renameNoReplace":  "OS-specific renameNoReplace syscall wrapper, not a write sequence",
	"internal/hooks/rename_noreplace_darwin.go:renameNoReplace":     "OS-specific renameNoReplace syscall wrapper, not a write sequence",
	"internal/hooks/rename_noreplace_linux.go:renameNoReplace":      "OS-specific renameNoReplace syscall wrapper, not a write sequence",

	// Append-only log writes: an O_APPEND descriptor with a flock (or a
	// bare append), never a temp name, never a rename or link. There is no
	// create/write/publish sequence here for internal/filewrite to replace.
	"internal/agentguard/gh.go:recordGhPrMergeOverride": "O_APPEND log write, not a create/publish sequence",
	"internal/hooks/metrics.go:AppendEvents":            "O_APPEND log write, not a create/publish sequence",
	"internal/runlog/runlog.go:Append":                  "O_APPEND log write (flock-guarded), not a create/publish sequence",
	"internal/streams/events.go:Append":                 "O_APPEND log write (flock-guarded), not a create/publish sequence",
	"internal/lifecyclehooks/queue.go:appendReceipt":    "O_APPEND log write (flock-guarded), not a create/publish sequence",
	"internal/locallink/execports.go:ExcludePath":       "O_APPEND write to a git exclude file, not a create/publish sequence",

	// Ephemeral scratch temp files: created, written, used as one
	// subprocess's input (gh api, git diff/push, a hook template), and
	// removed in the same function or its immediate caller, never
	// durably read back. There is no publish and nothing for
	// internal/filewrite's write-once or write-then-publish primitives to
	// replace.
	"cmd/wb/fleet_merge_policy.go:applyClassicProtectionWithoutLinearHistory":                           "ephemeral temp file passed as gh api --input, removed by defer; no publish",
	"cmd/wb/fleet_merge_policy.go:applySharedRuleset":                                                   "ephemeral temp file passed as gh api --input, removed by defer; no publish",
	"internal/hooks/run.go:runTemplate":                                                                 "ephemeral temp script for one subprocess execution, removed by defer; no publish",
	"internal/orchestrate/worktree_merge.go:PrepareWorktreeMergeRevert":                                 "writes an ephemeral git-diff patch temp file, removed by defer; no publish",
	"internal/orchestrate/worktree_merge.go:runWorktreeMergePrePushGate":                                "writes an ephemeral pre-push gate input temp file, removed by defer; no publish",
	"internal/orchestrate/worktree_merge.go:writeWorktreeMergePrompt":                                   "writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer, no publish",
	"internal/orchestrate/worktree_merge_conflict_replacement.go:writeConflictCandidateRefreshPrompt":   "writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer, no publish",
	"internal/orchestrate/worktree_merge_published_forward_repair.go:writePublishedForwardRepairPrompt": "writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer, no publish",
	"internal/orchestrate/worktree_merge_seal.go:writeValidationFailureSealPrompt":                      "writes an ephemeral prompt temp file for one worktree-create call; caller removes it by defer, no publish",
	"cmd/wb/deps_policy.go:fetchPolicy":                                                                 "writes one HTTP download to a temp file returned to the caller for one-shot use; no publish",
}

// InlineWriteSequenceViolation names one function outside
// internal/filewrite whose body directly calls a publish or create
// primitive spec/plans/coverage-to-100 task-9 consolidates into
// internal/filewrite, and is not exempted by either allow-list passed to
// FindInlineWriteSequences.
type InlineWriteSequenceViolation struct {
	// File is the path relative to root, slash-separated.
	File string
	// Func is the offending top-level function's name.
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
// calls a publish primitive (os.Rename, os.Link, syscall.Rename, the
// fd-relative unix.Renameat/Renameat2/RenameatxNp/Linkat family, the
// repo-local renameNoReplace helper, or a package-level alias of
// os.Link/os.Rename), a create primitive (os.CreateTemp, ioutil.TempFile,
// or os.OpenFile/unix.Openat with an O_CREAT-family flag), or os.WriteFile
// together with one of the publish primitives above in the same function --
// skipping any file:func listed in pendingMigration or notAFileWritePublish.
// Callers pass PendingMigrationExemptions and NotAFileWritePublishExemptions
// for the real guard; a test may pass its own maps (or nil) to exercise the
// detector without touching package state, which keeps every test in this
// file parallel-safe.
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
			key := rel + ":" + fn.Name.Name
			if _, exempt := pendingMigration[key]; exempt {
				continue
			}
			if _, exempt := notAFileWritePublish[key]; exempt {
				continue
			}
			violations = append(violations, InlineWriteSequenceViolation{
				File: rel,
				Func: fn.Name.Name,
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

// functionHasInlineWriteSequence reports whether body directly calls a
// publish primitive this package always bans (os.Rename, os.Link,
// syscall.Rename, the unix.Renameat/Renameat2/RenameatxNp/Linkat family,
// renameNoReplace, or a package-level alias of os.Link/os.Rename), or
// creates a file with an O_CREAT-family flag (os.CreateTemp,
// ioutil.TempFile, or os.OpenFile/unix.Openat carrying the flag) AND also
// writes content to it in the same function -- the second half of that
// condition is what tells a genuine write-then-publish or write-once
// sequence apart from a plain lock-file open (O_CREAT|O_EXCL followed only
// by Fchmod/Flock, with no content ever written), which is not a case this
// package's write primitives have anything to offer. aliases is the file's
// package-level os.Link/os.Rename alias set, from fileRenameLinkAliases.
func functionHasInlineWriteSequence(body *ast.BlockStmt, aliases map[string]bool) bool {
	publishBanned := false
	createBanned := false
	hasContentWrite := false
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
				publishBanned = true
			case pkgName == "syscall" && fn.Sel.Name == "Rename":
				publishBanned = true
			case pkgName == "unix" && inlineWriteSequencePublishNames[fn.Sel.Name]:
				publishBanned = true
			case pkgName == "os" && fn.Sel.Name == "CreateTemp":
				createBanned = true
			case pkgName == "ioutil" && fn.Sel.Name == "TempFile":
				createBanned = true
			case pkgName == "os" && fn.Sel.Name == "WriteFile":
				hasContentWrite = true
			case (pkgName == "os" && fn.Sel.Name == "OpenFile") || (pkgName == "unix" && fn.Sel.Name == "Openat"):
				if callArgsMentionCreateFlag(call.Args) {
					createBanned = true
				}
			case inlineWriteSequenceContentWriteNames[fn.Sel.Name]:
				hasContentWrite = true
			}
		case *ast.Ident:
			if fn.Name == "renameNoReplace" || aliases[fn.Name] {
				publishBanned = true
			}
		}
		return true
	})
	if publishBanned {
		return true
	}
	return createBanned && hasContentWrite
}

// inlineWriteSequencePublishNames are the unix-package (internal/
// unixcompat) selector names that publish a file by rename or hard link.
var inlineWriteSequencePublishNames = map[string]bool{
	"Renameat":    true,
	"Renameat2":   true,
	"RenameatxNp": true,
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
	found := false
	for _, arg := range args {
		ast.Inspect(arg, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.Ident:
				if strings.Contains(e.Name, "O_CREAT") {
					found = true
				}
			case *ast.SelectorExpr:
				if strings.Contains(e.Sel.Name, "O_CREAT") {
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
