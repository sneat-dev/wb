// Package worktreeretire owns the immutable private archive and its remote proof.
package worktreeretire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/gitcli"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// Receipt contains only the identities required to publish and verify one
// retirement archive. The caller retains the durable retirement transaction.
type Receipt struct {
	Task, Repository, Branch, Preserve, SourceSHA, RetiredRef                    string
	Worktree, Canonical, ArchiveRef, ArchiveSHA, ClaimID, EffortID, RunID, Phase string
}

type Claim struct {
	ClaimID, Repository, Branch, PromptArchive, PromptDigest string
	BaseSHA                                                  string
}

type Terminal struct {
	ClaimID, FinalCommit, ReportPath string
}

type Manifest struct {
	Version    int               `json:"version"`
	Repository string            `json:"repository"`
	Branch     string            `json:"branch"`
	Preserve   string            `json:"preserve,omitempty"`
	SourceSHA  string            `json:"source_sha"`
	RetiredRef string            `json:"retired_ref"`
	ClaimID    string            `json:"claim_id"`
	BaseSHA    string            `json:"base_sha,omitempty"`
	Files      map[string]string `json:"files"`
}

// Ports are read-only authority projections plus Git operations. A caller's
// Git runner remains injectable and the leaf does not depend on facade types.
type Ports struct {
	ReadClaim      func(home, worktree string) (Claim, error)
	ReadTerminal   func(home, worktree string) (*Terminal, error)
	ReportFileName func(task, repository string) (string, error)
	RemoteSHA      func(context.Context, string, string, string) (string, error)
	Git            func(context.Context, string, ...string) (string, error)
	GitBytes       func(context.Context, string, ...string) ([]byte, error)
	GitObjectSHA   func(context.Context, string, string) (string, error)
	WriteManifest  func(string, []byte, os.FileMode) error
}

// CaptureFile opens the complete source path without following symlinks.
func CaptureFile(source, destination string) (string, error) {
	return CaptureFileInjected(source, destination, nil)
}

func CaptureFileInjected(source, destination string, inj *filewrite.Injector) (string, error) {
	return captureFileWithOps(source, destination, inj, captureOps{stat: (*os.File).Stat, copy: io.Copy})
}

type captureOps struct {
	stat func(*os.File) (os.FileInfo, error)
	copy func(io.Writer, io.Reader) (int64, error)
}

func captureFileWithOps(source, destination string, inj *filewrite.Injector, ops captureOps) (string, error) {
	parent, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(filepath.Dir(source), false)
	if err != nil {
		return "", err
	}
	defer func() { _ = parent.Close() }()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(source), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	input := os.NewFile(uintptr(fd), source)
	defer func() { _ = input.Close() }()
	info, err := ops.stat(input)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("retirement archive refuses nonregular file %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", err
	}
	output, err := filewrite.CreateExclusivePath(destination, 0o600, inj)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := ops.copy(io.MultiWriter(output, hash), input)
	closeErr := filewrite.Close(output, destination, inj)
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func CaptureTree(source, destination string, include func(string) bool, hashes map[string]string, prefix string) error {
	root, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(source, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, _ := filepath.Rel(source, path) // WalkDir yields only descendants.
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retirement archive refuses symlink %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !include(filepath.ToSlash(relative)) {
			return nil
		}
		archivePath := filepath.ToSlash(filepath.Join(prefix, relative))
		digest, err := CaptureFile(path, filepath.Join(destination, relative))
		if err != nil {
			return err
		}
		hashes[archivePath] = digest
		return nil
	})
}

func PublishArchive(ctx context.Context, home, remote string, result *Receipt, ports Ports) error {
	claim, err := ports.ReadClaim(home, result.Worktree)
	if err != nil {
		return err
	}
	if claim.ClaimID != result.ClaimID || claim.Repository != result.Repository || claim.Branch != result.Branch {
		return fmt.Errorf("retirement archive claim identity changed")
	}
	working, err := os.MkdirTemp("", "wb-retirement-archive-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(working) }()
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"config", "user.name", "WB Retirement"}, {"config", "user.email", "wb-retirement@localhost"}} {
		if _, err := ports.Git(ctx, working, args...); err != nil {
			return err
		}
	}
	hashes := map[string]string{}
	if digest, err := CaptureFile(filepath.Join(result.Worktree, ".worktree.md"), filepath.Join(working, "worktree", ".worktree.md")); err != nil {
		return fmt.Errorf("capture worktree metadata: %w", err)
	} else {
		hashes["worktree/.worktree.md"] = digest
	}
	if err := CaptureTree(filepath.Join(result.Worktree, ".wb", "local"), filepath.Join(working, "worktree", ".wb", "local"), func(string) bool { return true }, hashes, "worktree/.wb/local"); err != nil {
		return fmt.Errorf("capture local Work Log: %w", err)
	}
	if digest, err := CaptureFile(filepath.Join(result.Worktree, ".wb-worklog", "recovery.json"), filepath.Join(working, "worktree", ".wb-worklog", "recovery.json")); err != nil {
		return fmt.Errorf("capture Work Log projection: %w", err)
	} else {
		hashes["worktree/.wb-worklog/recovery.json"] = digest
	}
	run := filepath.Join(home, "worklogs", result.EffortID, "runs", result.RunID)
	reportName, err := ports.ReportFileName(result.Task, result.Repository)
	if err != nil {
		return err
	}
	if err := CaptureTree(run, filepath.Join(working, "worklog", "run"), func(path string) bool { return ArchiveIncludesRunPath(result.ClaimID, reportName, path) }, hashes, "worklog/run"); err != nil {
		return fmt.Errorf("capture private Work Log run: %w", err)
	}
	for _, mandatory := range []string{"worklog/run/run.json", "worklog/run/claims/" + result.ClaimID + ".json", "worklog/run/terminals/" + result.ClaimID + ".json"} {
		if hashes[mandatory] == "" {
			return fmt.Errorf("missing mandatory Work Log file %s", mandatory)
		}
	}
	if claim.PromptArchive != "" && (filepath.Base(claim.PromptArchive) != claim.PromptArchive || hashes["worklog/run/"+claim.PromptArchive] != claim.PromptDigest) {
		return fmt.Errorf("private prompt archive does not match immutable claim digest")
	}
	terminal, err := ports.ReadTerminal(home, result.Worktree)
	if err != nil {
		return err
	}
	if terminal == nil || terminal.ClaimID != result.ClaimID || terminal.FinalCommit != result.SourceSHA {
		return fmt.Errorf("retirement terminal does not bind exact source commit")
	}
	if terminal.ReportPath != "" && (filepath.Clean(terminal.ReportPath) != filepath.Join(run, "reports", reportName) || hashes["worklog/run/reports/"+reportName] == "") {
		return fmt.Errorf("missing referenced private Work Log report")
	}
	if err := CaptureTree(filepath.Join(home, "worklogs", result.EffortID, "outbox"), filepath.Join(working, "worklog", "outbox"), func(path string) bool { return strings.HasPrefix(path, result.RunID+"-"+result.ClaimID+"-") }, hashes, "worklog/outbox"); err != nil {
		return fmt.Errorf("capture Work Log outbox: %w", err)
	}
	preserve := result.Preserve
	if preserve == "" {
		preserve = "branch"
	}
	ref := "refs/heads/" + result.ArchiveRef
	existing, err := ports.RemoteSHA(ctx, result.Canonical, remote, ref)
	if err != nil {
		return err
	}
	if existing != "" && claim.BaseSHA != "" {
		legacy, err := legacyHistoryArchive(ctx, working, remote, ref, existing, ports)
		if err != nil {
			return err
		}
		if legacy {
			// An older preservation already backed up the base through its
			// source history. Continue that exact immutable archive only when
			// the original ancestry proof still succeeds; divergence never
			// inherits this compatibility exception.
			if _, err := ports.Git(ctx, result.Worktree, "merge-base", "--is-ancestor", claim.BaseSHA, result.SourceSHA); err != nil {
				return fmt.Errorf("legacy retirement archive cannot preserve a diverged base: %w", err)
			}
			claim.BaseSHA = ""
		}
	}
	if claim.BaseSHA != "" {
		if err := archiveSourceHistory(ctx, working, remote, existing, *result, claim, hashes, ports); err != nil {
			return err
		}
	}
	manifest := Manifest{Version: 1, Repository: result.Repository, Branch: result.Branch, Preserve: preserve, SourceSHA: result.SourceSHA, RetiredRef: result.RetiredRef, ClaimID: result.ClaimID, BaseSHA: claim.BaseSHA, Files: hashes}
	// Manifest has a fixed schema of strings, integers, and map[string]string.
	body, _ := json.MarshalIndent(manifest, "", "  ")
	writeManifest := ports.WriteManifest
	if writeManifest == nil {
		writeManifest = os.WriteFile
	}
	if err := writeManifest(filepath.Join(working, "retirement.json"), append(body, '\n'), 0o600); err != nil {
		return err
	}
	if existing != "" {
		if err := VerifyArchive(ctx, working, remote, ref, existing, manifest, ports); err != nil {
			return err
		}
		result.ArchiveSHA, result.Phase = existing, "archive_published"
		return nil
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "Retire " + result.Repository + " " + result.Branch + " at " + result.SourceSHA}} {
		if _, err := ports.Git(ctx, working, args...); err != nil {
			return err
		}
	}
	sha, err := ports.Git(ctx, working, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := ports.Git(ctx, working, "push", "--force-with-lease="+ref+":", remote, "HEAD:"+ref); err != nil {
		return fmt.Errorf("publish private Work Log archive: %w", err)
	}
	observed, err := ports.RemoteSHA(ctx, result.Canonical, remote, ref)
	if err != nil || observed != sha {
		return fmt.Errorf("private archive ref verification failed: %w", err)
	}
	if err := VerifyArchive(ctx, working, remote, ref, sha, manifest, ports); err != nil {
		return err
	}
	result.ArchiveSHA, result.Phase = sha, "archive_published"
	return nil
}

// Keep the original base as an advertised recovery ref even when it is not
// reachable from the source branch. The archive commit itself remains a
// metadata-only root; these histories travel in its verified bundle blob.
// A pre-history-bundle archive is accepted only by the ancestry-valid
// compatibility path above. The ordinary full archive verification still runs.
func legacyHistoryArchive(ctx context.Context, working, remote, ref, sha string, ports Ports) (bool, error) {
	if err := fetchHistoryArchive(ctx, working, remote, ref, sha, ports); err != nil {
		return false, err
	}
	body, err := ports.GitBytes(ctx, working, "show", "FETCH_HEAD:retirement.json")
	if err != nil {
		return false, err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return false, err
	}
	return manifest.BaseSHA == "" && manifest.Files["history.bundle"] == "", nil
}

func fetchHistoryArchive(ctx context.Context, working, remote, ref, sha string, ports Ports) error {
	if _, err := ports.Git(ctx, working, "fetch", "--no-tags", remote, ref); err != nil {
		return err
	}
	head, err := ports.Git(ctx, working, "rev-parse", "FETCH_HEAD")
	if err != nil || head != sha {
		return fmt.Errorf("private archive commit changed while recovering history: %w", err)
	}
	return nil
}

func archiveSourceHistory(ctx context.Context, working, remote, existing string, result Receipt, claim Claim, hashes map[string]string, ports Ports) error {
	bundle := filepath.Join(working, ".git", "source-history.bundle")
	if existing != "" {
		// Pack bytes are not canonical. Reuse the published backup on retry,
		// proving its actual refs and object graph instead of regenerating it.
		if err := fetchHistoryArchive(ctx, working, remote, "refs/heads/"+result.ArchiveRef, existing, ports); err != nil {
			return err
		}
		body, err := ports.GitBytes(ctx, working, "show", "FETCH_HEAD:history.bundle")
		if err != nil {
			return err
		}
		if err := os.WriteFile(bundle, body, 0o600); err != nil {
			return err
		}
	} else {
		for _, args := range [][]string{
			{"fetch", "--no-tags", result.Worktree, result.SourceSHA + ":refs/heads/preserved-source", claim.BaseSHA + ":refs/heads/preserved-base"},
			{"bundle", "create", bundle, "refs/heads/preserved-source", "refs/heads/preserved-base"},
		} {
			if _, err := ports.Git(ctx, working, args...); err != nil {
				return fmt.Errorf("preserve source and claimed-base histories: %w", err)
			}
		}
	}
	if err := validateSourceHistory(ctx, bundle, result.SourceSHA, claim.BaseSHA, ports); err != nil {
		return err
	}
	digest, err := CaptureFile(bundle, filepath.Join(working, "history.bundle"))
	if err != nil {
		return fmt.Errorf("capture source and claimed-base histories: %w", err)
	}
	hashes["history.bundle"] = digest
	return nil
}

// Header verification alone does not check a bundle's pack payload. Import
// into an empty object database and fsck both recovery roots before deletion
// can be authorized, even when all objects still exist in the source clone.
func validateSourceHistory(ctx context.Context, bundle, source, base string, ports Ports) error {
	restored, err := os.MkdirTemp("", "wb-retirement-history-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(restored) }()
	heads, err := ports.Git(ctx, restored, "bundle", "list-heads", bundle)
	if err != nil {
		return err
	}
	fields := strings.Fields(heads)
	refs := map[string]string{}
	if len(fields) == 4 {
		refs[fields[1]], refs[fields[3]] = fields[0], fields[2]
	}
	if len(refs) != 2 || refs["refs/heads/preserved-source"] != source || refs["refs/heads/preserved-base"] != base {
		return fmt.Errorf("history bundle does not advertise the exact source and claimed base")
	}
	for _, args := range [][]string{
		{"init", "--bare", "--initial-branch=main"},
		{"fetch", "--no-tags", bundle, "refs/heads/preserved-source:refs/heads/preserved-source", "refs/heads/preserved-base:refs/heads/preserved-base"},
		{"fsck", "--full", "refs/heads/preserved-source", "refs/heads/preserved-base"},
	} {
		if _, err := ports.Git(ctx, restored, args...); err != nil {
			return fmt.Errorf("restore source and claimed-base histories: %w", err)
		}
	}
	return nil
}

func ArchiveIncludesRunPath(claimID, reportName, path string) bool {
	if path == "run.json" || path == "original-prompt.json" || strings.HasPrefix(path, "original-prompt.") {
		return true
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return true
	}
	if parts[0] == "claims" || parts[0] == "terminals" || parts[0] == "cleanups" {
		return parts[1] == claimID+".json"
	}
	if parts[0] == "corrections" || parts[0] == "dirty-discard" {
		return parts[1] == claimID
	}
	if parts[0] == "reports" {
		return parts[1] == reportName
	}
	return false
}

func VerifyArchive(ctx context.Context, working, remote, ref, expectedSHA string, expected Manifest, ports Ports) error {
	if _, err := ports.Git(ctx, working, "fetch", "--no-tags", remote, ref); err != nil {
		return err
	}
	sha, err := ports.Git(ctx, working, "rev-parse", "FETCH_HEAD")
	if err != nil || sha != expectedSHA {
		return fmt.Errorf("private archive commit changed: %w", err)
	}
	sizeText, err := ports.Git(ctx, working, "cat-file", "-s", "FETCH_HEAD:retirement.json")
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size > 16<<20 {
		return fmt.Errorf("private archive manifest exceeds verification limit")
	}
	body, err := ports.GitBytes(ctx, working, "show", "FETCH_HEAD:retirement.json")
	if err != nil {
		return err
	}
	var actual Manifest
	if err := json.Unmarshal(body, &actual); err != nil {
		return err
	}
	paths, err := ValidateArchiveManifest(expected, actual)
	if err != nil {
		return err
	}
	tree, err := ports.Git(ctx, working, "ls-tree", "-r", "-z", "--name-only", "FETCH_HEAD")
	if err != nil {
		return err
	}
	var listed []string
	for _, path := range strings.Split(tree, "\x00") {
		if path != "" {
			listed = append(listed, path)
		}
	}
	if err := ValidateArchiveTree(expected.Files, paths, listed); err != nil {
		return err
	}
	for _, path := range paths {
		value, err := ports.GitObjectSHA(ctx, working, "FETCH_HEAD:"+path)
		if err != nil {
			return fmt.Errorf("private archive file missing %s: %w", path, err)
		}
		if value != expected.Files[path] {
			return fmt.Errorf("private archive file digest mismatch %s", path)
		}
	}
	return nil
}

func ValidateArchiveManifest(expected, actual Manifest) ([]string, error) {
	if actual.Version != expected.Version || actual.Repository != expected.Repository || actual.Branch != expected.Branch || ArchiveManifestPreserve(actual) != ArchiveManifestPreserve(expected) || actual.SourceSHA != expected.SourceSHA || actual.RetiredRef != expected.RetiredRef || actual.ClaimID != expected.ClaimID || actual.BaseSHA != expected.BaseSHA || len(actual.Files) != len(expected.Files) {
		return nil, fmt.Errorf("private archive manifest identity mismatch")
	}
	paths := make([]string, 0, len(expected.Files))
	for path, hash := range expected.Files {
		if actual.Files[path] != hash {
			return nil, fmt.Errorf("private archive manifest file mismatch %s", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func ValidateArchiveTree(files map[string]string, paths, listed []string) error {
	if len(listed) != len(paths)+1 {
		return fmt.Errorf("private archive has unlisted files")
	}
	for _, path := range listed {
		if path != "retirement.json" && files[path] == "" {
			return fmt.Errorf("private archive has unlisted file %s", path)
		}
	}
	return nil
}

func ArchiveManifestPreserve(manifest Manifest) string {
	if manifest.Preserve == "" {
		return "branch"
	}
	return manifest.Preserve
}

func GitObjectSHA(ctx context.Context, directory, object string) (string, error) {
	return gitcli.SHA256Object(ctx, directory, object)
}

func GitBytes(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return gitcli.ReadObjectBytes(ctx, directory, args...)
}
