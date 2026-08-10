package worktrees

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

const (
	workLogProjectionDirectory  = ".wb-worklog"
	workLogProjectionName       = "recovery.json"
	workLogProjectionExclude    = "/.wb-worklog/"
	legacyWorkLogProjectionName = ".wb-worklog.json"
)

var errWorkLogProjectionNotFound = errors.New("work-log projection not found")

// WorkLogOptions is transport-neutral. The exact prompt is private local data;
// only opaque IDs and bounded Git evidence enter the projection/outbox.
type WorkLogOptions struct {
	EffortID              string
	RunID                 string
	Initiator             string
	AgentID               string
	AgentRuntime          string
	Model                 string
	OriginalPrompt        string // readable local file, copied to the private archive
	RequireOriginalPrompt bool   // public create/recycle commands require exact local recovery input

	// originalPromptContents is an immutable preflight snapshot. Keeping it in
	// the options passed through one create/recycle call closes the usual
	// stat/read/use race: recordWorkLog never reopens a path whose bytes may
	// have changed after preflight.
	originalPromptContents []byte
	originalPromptDigest   string
}

// workLogProjection is an untrusted pointer. It contains no path, prompt,
// repository, branch, or model data and is never used without loading and
// corroborating the immutable private claim.
type workLogProjection struct {
	Version   int    `json:"version"`
	EffortID  string `json:"effort_id"`
	RunID     string `json:"run_id"`
	ClaimID   string `json:"claim_id"`
	Lifecycle string `json:"lifecycle"`
}

type workLogClaim struct {
	Version       int       `json:"version"`
	EffortID      string    `json:"effort_id"`
	RunID         string    `json:"run_id"`
	ClaimID       string    `json:"claim_id"`
	Task          string    `json:"task"`
	Repository    string    `json:"repository"`
	Worktree      string    `json:"worktree"`
	Branch        string    `json:"branch"`
	Base          string    `json:"base"`
	BaseSHA       string    `json:"base_sha"`
	Lifecycle     string    `json:"lifecycle"`
	RecordedAt    time.Time `json:"recorded_at"`
	Initiator     string    `json:"initiator,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	AgentRuntime  string    `json:"agent_runtime,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptArchive string    `json:"prompt_archive,omitempty"` // run-relative
	PromptDigest  string    `json:"prompt_sha256,omitempty"`
	ParentClaimID string    `json:"parent_claim_id,omitempty"`
	AcquiredVia   string    `json:"acquired_via,omitempty"`
}

type workLogPromptMetadata struct {
	Version         int       `json:"version"`
	SHA256          string    `json:"sha256"`
	SourceReference string    `json:"source_reference"`
	CapturedAt      time.Time `json:"captured_at"`
}

type workLogTerminalRecord struct {
	workLogClaim
	FinalCommit      string    `json:"final_commit"`
	Disposition      string    `json:"worktree_disposition"`
	SealedAt         time.Time `json:"sealed_at"`
	SuccessorClaimID string    `json:"successor_claim_id,omitempty"`
	SuccessorAgentID string    `json:"successor_agent_id,omitempty"`
}

type workLogPublicEvent struct {
	Version     int       `json:"version"`
	Type        string    `json:"type"`
	At          time.Time `json:"at"`
	EffortID    string    `json:"effort_id"`
	RunID       string    `json:"run_id"`
	ClaimID     string    `json:"claim_id"`
	Repository  string    `json:"repository"`
	Branch      string    `json:"branch"`
	Base        string    `json:"base"`
	BaseSHA     string    `json:"base_sha"`
	FinalCommit string    `json:"final_commit,omitempty"`
	Lifecycle   string    `json:"lifecycle"`
	Disposition string    `json:"disposition,omitempty"`
}

type legacyWorkLogClaim struct {
	Version       int       `json:"version"`
	EffortID      string    `json:"effort_id"`
	RunID         string    `json:"run_id"`
	Task          string    `json:"task"`
	Repository    string    `json:"repository"`
	Worktree      string    `json:"worktree"`
	Branch        string    `json:"branch"`
	Base          string    `json:"base"`
	BaseSHA       string    `json:"base_sha"`
	Lifecycle     string    `json:"lifecycle"`
	RecordedAt    time.Time `json:"recorded_at"`
	Initiator     string    `json:"initiator,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	AgentRuntime  string    `json:"agent_runtime,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptArchive string    `json:"prompt_archive,omitempty"`
}

type legacyClaimMigration struct {
	Version             int  `json:"version"`
	RecoveredClaims     int  `json:"recovered_claims"`
	ObservedProjections int  `json:"observed_worktree_projections"`
	LostCardinality     bool `json:"lost_cardinality"`
}

// PrepareWorkLogOptions validates every identifier and snapshots the exact
// private prompt before a caller mutates Git, hooks, or a worktree. It also
// corroborates an existing run's immutable prompt archive so reusing a Run ID
// with different bytes is rejected before worktree creation.
func PrepareWorkLogOptions(projectsRoot, task string, options WorkLogOptions) (WorkLogOptions, error) {
	now := time.Now().UTC()
	effort, run, err := normalizeWorkLogOptions(task, options, now)
	if err != nil {
		return WorkLogOptions{}, err
	}
	options.EffortID = effort
	options.RunID = run
	if err := snapshotOriginalPrompt(&options); err != nil {
		return WorkLogOptions{}, err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return WorkLogOptions{}, err
	}
	if err := corroborateExistingRunPrompt(home, effort, run, options); err != nil {
		return WorkLogOptions{}, err
	}
	return options, nil
}

// PreflightWorkLogOptions remains the pure, path-independent validation used
// by callers that have not resolved a projects root yet. Mutation paths use
// PrepareWorkLogOptions so an existing run is corroborated as well.
func PreflightWorkLogOptions(task string, options WorkLogOptions) error {
	effort, run, err := normalizeWorkLogOptions(task, options, time.Now().UTC())
	if err != nil {
		return err
	}
	options.EffortID, options.RunID = effort, run
	return snapshotOriginalPrompt(&options)
}

func snapshotOriginalPrompt(options *WorkLogOptions) error {
	if len(options.originalPromptContents) != 0 {
		digest := sha256.Sum256(options.originalPromptContents)
		if options.originalPromptDigest != hex.EncodeToString(digest[:]) || strings.TrimSpace(options.OriginalPrompt) == "" {
			return fmt.Errorf("prepared original prompt snapshot is internally inconsistent")
		}
		return nil
	}
	prompt := strings.TrimSpace(options.OriginalPrompt)
	if prompt == "" {
		if options.RequireOriginalPrompt {
			return fmt.Errorf("--original-prompt-file is required so the private Work Log can retain the exact originating request")
		}
		return nil
	}
	absolute, err := filepath.Abs(prompt)
	if err != nil {
		return fmt.Errorf("resolve original prompt %s before mutation: %w", prompt, err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return fmt.Errorf("open original prompt %s before mutation: %w", prompt, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect original prompt %s: %w", prompt, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("original prompt %s must be a regular file", prompt)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read original prompt %s before mutation: %w", prompt, err)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return fmt.Errorf("original prompt %s must not be empty", prompt)
	}
	digest := sha256.Sum256(contents)
	options.OriginalPrompt = absolute
	options.originalPromptContents = append([]byte(nil), contents...)
	options.originalPromptDigest = hex.EncodeToString(digest[:])
	return nil
}

func corroborateExistingRunPrompt(home, effort, run string, options WorkLogOptions) error {
	runDir, _, err := openWorkLogRun(home, effort, run, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing work-log run before mutation: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	archived, promptErr := readBytesAt(runDir, "original-prompt.txt")
	var metadata workLogPromptMetadata
	metadataErr := readJSONAt(runDir, "original-prompt.json", &metadata)
	if promptErr == nil {
		if len(options.originalPromptContents) == 0 {
			return fmt.Errorf("work-log run %s/%s already has an original prompt; provide the same --original-prompt-file", effort, run)
		}
		if !bytes.Equal(archived, options.originalPromptContents) {
			return fmt.Errorf("work-log run %s/%s is already bound to different original prompt bytes", effort, run)
		}
		digest := sha256.Sum256(archived)
		want := hex.EncodeToString(digest[:])
		if metadataErr == nil && (metadata.Version != 1 || metadata.SHA256 != want) {
			return fmt.Errorf("work-log run %s/%s prompt metadata does not match its immutable archive", effort, run)
		}
		if metadataErr != nil && !errors.Is(metadataErr, os.ErrNotExist) {
			return fmt.Errorf("inspect existing prompt metadata: %w", metadataErr)
		}
		return nil
	}
	if !errors.Is(promptErr, os.ErrNotExist) {
		return fmt.Errorf("inspect existing original prompt: %w", promptErr)
	}
	if metadataErr == nil || !errors.Is(metadataErr, os.ErrNotExist) {
		if metadataErr != nil {
			return fmt.Errorf("inspect existing prompt metadata: %w", metadataErr)
		}
		return fmt.Errorf("work-log run %s/%s has prompt metadata without its immutable archive", effort, run)
	}
	// Once a run index or claim exists, an absent prompt is evidence from an
	// older/partial writer. Never guess that a newly supplied file was the
	// original request and silently rewrite history.
	if _, err := readBytesAt(runDir, "run.json"); err == nil {
		return fmt.Errorf("work-log run %s/%s already exists without an immutable original prompt", effort, run)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	claims, err := openPrivateChild(runDir, "claims", false)
	if err == nil {
		defer func() { _ = claims.Close() }()
		if names, readErr := claims.Readdirnames(1); readErr == nil && len(names) != 0 {
			return fmt.Errorf("work-log run %s/%s already has a claim without an immutable original prompt", effort, run)
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func normalizeWorkLogOptions(task string, options WorkLogOptions, now time.Time) (effort, run string, err error) {
	effort = strings.TrimSpace(options.EffortID)
	if effort == "" {
		effort = task
	}
	run = strings.TrimSpace(options.RunID)
	if run == "" {
		run = "wb-" + now.Format("20060102T150405.000000000Z")
	}
	if !validSafeSegment(effort) {
		return "", "", fmt.Errorf("work-log effort id %q must be one safe path segment", effort)
	}
	if !validSafeSegment(run) {
		return "", "", fmt.Errorf("work-log run id %q must be one safe path segment", run)
	}
	return effort, run, nil
}

func workLogClaimID(effort string, result CreateResult) string {
	hash := sha256.New()
	// Claim identity is portable: run IDs and machine-local worktree paths are
	// deliberately absent. The immutable private claim still records and
	// corroborates the absolute live path.
	for _, value := range []string{effort, result.Repository, result.Branch, result.Base, result.BaseSHA} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func successorWorkLogClaimID(parentClaimID, successor, disposition string) string {
	hash := sha256.New()
	for _, value := range []string{"successor", parentClaimID, successor, disposition} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validClaimID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// recordWorkLog writes one immutable private claim per worktree, a redacted
// pointer projection, and a collision-free per-claim outbox event.
func recordWorkLog(home, task string, result CreateResult, options WorkLogOptions) (string, error) {
	now := time.Now().UTC()
	effort, run, err := normalizeWorkLogOptions(task, options, now)
	if err != nil {
		return "", err
	}
	claimID := workLogClaimID(effort, result)
	runDir, runPath, err := openWorkLogRun(home, effort, run, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = runDir.Close() }()
	if err := migrateLegacySingletonClaim(runDir, runPath, home, effort, run); err != nil {
		return "", fmt.Errorf("migrate legacy singleton claim: %w", err)
	}

	promptArchive, promptDigest, err := ensureOriginalPromptArchive(runDir, options, now)
	if err != nil {
		return "", err
	}
	claim := workLogClaim{Version: 1, EffortID: effort, RunID: run, ClaimID: claimID, Task: task,
		Repository: result.Repository, Worktree: result.WorktreeDir, Branch: result.Branch,
		Base: result.Base, BaseSHA: result.BaseSHA, Lifecycle: "active", RecordedAt: now,
		Initiator: strings.TrimSpace(options.Initiator), AgentID: strings.TrimSpace(options.AgentID),
		AgentRuntime: strings.TrimSpace(options.AgentRuntime), Model: strings.TrimSpace(options.Model),
		PromptArchive: promptArchive, PromptDigest: promptDigest}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return "", err
	}
	defer func() { _ = claims.Close() }()
	claimName := claimID + ".json"
	if err := writeJSONImmutableAt(claims, claimName, claim, true); err != nil {
		return "", fmt.Errorf("write immutable work-log claim: %w", err)
	}
	if err := ensureWorkLogRunIndex(runDir, effort, run); err != nil {
		return "", err
	}
	projection := workLogProjection{Version: 1, EffortID: effort, RunID: run, ClaimID: claimID, Lifecycle: "active"}
	if err := writeWorkLogProjection(result.WorktreeDir, projection); err != nil {
		return "", err
	}
	outbox, err := openWorkLogOutbox(home, effort, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: now, EffortID: effort,
		RunID: run, ClaimID: claimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, Lifecycle: "active"}
	if err := writeJSONImmutableAt(outbox, run+"-"+claimID+"-claimed.json", event, true); err != nil {
		return "", err
	}
	return filepath.Join(runPath, "claims", claimName), nil
}

// reserveOriginalPromptArchive binds a coordinated run to its exact private
// prompt before any worktree is created. The reservation is idempotent for the
// same bytes and rejects a conflicting writer through immutable no-replace
// publication.
func reserveOriginalPromptArchive(home, task string, options WorkLogOptions) error {
	if len(options.originalPromptContents) == 0 && strings.TrimSpace(options.OriginalPrompt) == "" && !options.RequireOriginalPrompt {
		return nil
	}
	effort, run, err := normalizeWorkLogOptions(task, options, time.Now().UTC())
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, effort, run, true)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	_, _, err = ensureOriginalPromptArchive(runDir, options, time.Now().UTC())
	return err
}

func ensureOriginalPromptArchive(runDir *os.File, options WorkLogOptions, now time.Time) (archive, digest string, err error) {
	if len(options.originalPromptContents) == 0 {
		if err := snapshotOriginalPrompt(&options); err != nil {
			return "", "", err
		}
	}
	if len(options.originalPromptContents) == 0 {
		return "", "", nil
	}
	archive = "original-prompt.txt"
	digest = options.originalPromptDigest
	if err := writeBytesImmutableAt(runDir, archive, options.originalPromptContents, 0o600, true); err != nil {
		return "", "", fmt.Errorf("archive original prompt: %w", err)
	}
	var existing workLogPromptMetadata
	if err := readJSONAt(runDir, "original-prompt.json", &existing); err == nil {
		if existing.Version != 1 || existing.SHA256 != digest {
			return "", "", fmt.Errorf("existing original prompt metadata does not match immutable prompt archive")
		}
		return archive, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect original prompt metadata: %w", err)
	}
	metadata := workLogPromptMetadata{Version: 1, SHA256: digest,
		SourceReference: options.OriginalPrompt, CapturedAt: now}
	if err := writeJSONImmutableAt(runDir, "original-prompt.json", metadata, true); err != nil {
		// Concurrent same-run writers may race to publish metadata after both
		// observed the same immutable prompt bytes. Accept the winner only when
		// its digest corroborates those exact bytes.
		if readErr := readJSONAt(runDir, "original-prompt.json", &existing); readErr == nil && existing.Version == 1 && existing.SHA256 == digest {
			return archive, digest, nil
		}
		return "", "", fmt.Errorf("archive original prompt metadata: %w", err)
	}
	return archive, digest, nil
}

func ensureWorkLogRunIndex(runDir *os.File, effort, run string) error {
	var index struct {
		Version int    `json:"version"`
		Effort  string `json:"effort_id"`
		Run     string `json:"run_id"`
	}
	if err := readJSONAt(runDir, "run.json", &index); err == nil {
		if index.Version != 1 || index.Effort != effort || index.Run != run {
			return fmt.Errorf("existing run index identity mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	index.Version, index.Effort, index.Run = 1, effort, run
	return writeJSONImmutableAt(runDir, "run.json", index, true)
}

// migrateLegacySingletonClaim recovers the one surviving claim from the
// historical run-level claim.json layout. It also writes a deterministic
// machine-readable cardinality warning when live projections prove that the
// singleton had overwritten sibling repositories. The legacy bytes remain in
// place as evidence; migration is immutable and idempotent.
func migrateLegacySingletonClaim(runDir *os.File, runPath, home, effort, run string) error {
	var legacy legacyWorkLogClaim
	if err := readJSONAt(runDir, "claim.json", &legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if legacy.Version != 1 || legacy.EffortID != effort || legacy.RunID != run || legacy.Repository == "" || legacy.Worktree == "" || legacy.Branch == "" || legacy.Base == "" {
		return fmt.Errorf("legacy claim identity is incomplete or mismatched")
	}
	legacyResult := CreateResult{Repository: legacy.Repository, WorktreeDir: legacy.Worktree,
		Branch: legacy.Branch, Base: legacy.Base, BaseSHA: legacy.BaseSHA}
	claimID := workLogClaimID(effort, legacyResult)
	promptArchive := ""
	if legacy.PromptArchive != "" {
		cleanPrompt := filepath.Clean(legacy.PromptArchive)
		if filepath.Dir(cleanPrompt) == filepath.Clean(runPath) {
			promptArchive = filepath.Base(cleanPrompt)
		}
	}
	claim := workLogClaim{Version: 1, EffortID: effort, RunID: run, ClaimID: claimID,
		Task: legacy.Task, Repository: legacy.Repository, Worktree: legacy.Worktree,
		Branch: legacy.Branch, Base: legacy.Base, BaseSHA: legacy.BaseSHA,
		Lifecycle: legacy.Lifecycle, RecordedAt: legacy.RecordedAt, Initiator: legacy.Initiator,
		AgentID: legacy.AgentID, AgentRuntime: legacy.AgentRuntime, Model: legacy.Model,
		PromptArchive: promptArchive}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return err
	}
	if err := writeJSONImmutableAt(claims, claimID+".json", claim, true); err != nil {
		_ = claims.Close()
		return err
	}
	var existingMigration legacyClaimMigration
	if err := readJSONAt(runDir, "legacy-claim-migration.json", &existingMigration); err == nil {
		_ = claims.Close()
		if existingMigration.Version != 1 {
			return fmt.Errorf("legacy claim migration version mismatch")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = claims.Close()
		return err
	}
	claimEntries, err := claims.Readdirnames(-1)
	_ = claims.Close()
	if err != nil {
		return err
	}
	observed, err := countLegacyWorkLogProjections(filepath.Join(home, "worktrees"), effort, run)
	if err != nil {
		return err
	}
	migration := legacyClaimMigration{Version: 1, RecoveredClaims: len(claimEntries),
		ObservedProjections: observed, LostCardinality: observed > len(claimEntries)}
	return writeJSONImmutableAt(runDir, "legacy-claim-migration.json", migration, true)
}

func countLegacyWorkLogProjections(root, effort, run string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		isLegacyProjection := !entry.IsDir() && entry.Name() == legacyWorkLogProjectionName
		isCurrentProjection := !entry.IsDir() && entry.Name() == workLogProjectionName && filepath.Base(filepath.Dir(path)) == workLogProjectionDirectory
		if !isLegacyProjection && !isCurrentProjection {
			return nil
		}
		var identity struct {
			EffortID string `json:"effort_id"`
			RunID    string `json:"run_id"`
		}
		contents, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(contents, &identity) != nil {
			return nil
		}
		if identity.EffortID == effort && identity.RunID == run {
			count++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return count, err
}

func writeWorkLogProjection(worktree string, projection workLogProjection) error {
	if err := validateProjection(projection); err != nil {
		return err
	}
	if err := ensureWorkLogProjectionExclude(worktree); err != nil {
		return err
	}
	directory, err := openWorkLogProjectionDirectory(worktree, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return writeJSONAtomicAt(directory, workLogProjectionName, projection, 0o600)
}

func ensureWorkLogProjectionExclude(worktree string) error {
	gitPath, err := git(context.Background(), worktree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve per-worktree exclude: %w", err)
	}
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(worktree, gitPath)
	}
	if err := os.MkdirAll(filepath.Dir(gitPath), 0o700); err != nil {
		return fmt.Errorf("create per-worktree git info: %w", err)
	}
	exclude, err := os.ReadFile(gitPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read per-worktree exclude: %w", err)
	}
	if !strings.Contains("\n"+string(exclude)+"\n", "\n"+workLogProjectionExclude+"\n") {
		if err := writeBytesAtomic(filepath.Dir(gitPath), filepath.Base(gitPath), append(exclude, []byte("\n"+workLogProjectionExclude+"\n")...), 0o600); err != nil {
			return fmt.Errorf("exclude work-log projection: %w", err)
		}
	}
	return nil
}

func openWorkLogProjectionDirectory(worktree string, create bool) (*os.File, error) {
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var fd int
	if create {
		fd, err = openOrCreateNoFollowDirectory(int(root.Fd()), workLogProjectionDirectory)
	} else {
		fd, err = unix.Openat(int(root.Fd()), workLogProjectionDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if errors.Is(err, unix.ENOENT) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("open work-log projection directory: %w", err)
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "wb-worklog-projection")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap work-log projection directory")
	}
	path := filepath.Join(worktree, workLogProjectionDirectory)
	if !directoryStillMatches(path, directory) {
		_ = directory.Close()
		return nil, fmt.Errorf("work-log projection directory path changed: %s", path)
	}
	return directory, nil
}

func validateProjection(projection workLogProjection) error {
	if projection.Version != 1 || !validSafeSegment(projection.EffortID) || !validSafeSegment(projection.RunID) || !validClaimID(projection.ClaimID) {
		return fmt.Errorf("invalid work-log projection identity")
	}
	if projection.Lifecycle != "active" && projection.Lifecycle != "terminal" {
		return fmt.Errorf("invalid work-log projection lifecycle %q", projection.Lifecycle)
	}
	return nil
}

// sealWorkLogForRecycle treats the projection as an untrusted pointer, loads
// the immutable private claim, corroborates it with live Git, then writes one
// immutable terminal and outbox entry per claim before making the projection
// terminal. Retrying the exact transition is idempotent.
func sealWorkLogForRecycle(home, worktree, finalCommit, disposition string) error {
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil // legacy pre-work-log checkout
	}
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return fmt.Errorf("open private work-log run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	claimLock, err := lockClaim(runDir, projection.ClaimID)
	if err != nil {
		return err
	}
	defer claimLock()
	currentProjection, err := readWorkLogProjection(worktree)
	if err != nil || currentProjection != projection {
		return fmt.Errorf("work-log projection changed while waiting for claim fence")
	}
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return fmt.Errorf("read immutable work-log claim: %w", err)
	}
	if err := corroborateClaim(worktree, finalCommit, projection, claim); err != nil {
		return err
	}
	sealedAt, err := writeWorkLogTerminal(home, runDir, claim, finalCommit, disposition, "", "")
	if err != nil {
		return err
	}
	_ = sealedAt
	projection.Lifecycle = "terminal"
	return writeWorkLogProjection(worktree, projection)
}

// transferWorkLogClaim seals exactly one old claim and atomically rebinds the
// projection to one deterministic active successor. Dirty tracked state is
// intentionally untouched: handoff/not_landed is a control-plane transition,
// not discard. A crash before the final projection write is retryable because
// terminal and successor identities are immutable and deterministic.
func transferWorkLogClaim(home, worktree, finalCommit, disposition, successor string) error {
	successor = strings.TrimSpace(successor)
	if successor == "" || len(successor) > 200 || strings.ContainsAny(successor, "\x00\r\n") {
		return fmt.Errorf("one successor agent/session ID is required for %s", disposition)
	}
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claimLock, err := lockClaim(runDir, projection.ClaimID)
	if err != nil {
		return err
	}
	defer claimLock()
	currentProjection, err := readWorkLogProjection(worktree)
	if err != nil || currentProjection != projection {
		return fmt.Errorf("work-log projection changed while waiting for claim fence")
	}
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return err
	}
	if err := corroborateClaim(worktree, finalCommit, projection, claim); err != nil {
		return err
	}
	successorClaimID := successorWorkLogClaimID(claim.ClaimID, successor, disposition)
	sealedAt, err := writeWorkLogTerminal(home, runDir, claim, finalCommit, disposition, successorClaimID, successor)
	if err != nil {
		return err
	}
	successorClaim := claim
	successorClaim.ClaimID = successorClaimID
	successorClaim.Lifecycle = "active"
	successorClaim.RecordedAt = sealedAt
	successorClaim.AgentID = successor
	successorClaim.ParentClaimID = claim.ClaimID
	successorClaim.AcquiredVia = disposition
	if err := writeJSONImmutableAt(claims, successorClaimID+".json", successorClaim, true); err != nil {
		return fmt.Errorf("write immutable successor claim: %w", err)
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: sealedAt,
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: successorClaimID,
		Repository: claim.Repository, Branch: claim.Branch, Base: claim.Base,
		BaseSHA: claim.BaseSHA, Lifecycle: "active", Disposition: disposition}
	if err := writeJSONImmutableAt(outbox, claim.RunID+"-"+successorClaimID+"-claimed.json", event, true); err != nil {
		return fmt.Errorf("write successor outbox: %w", err)
	}
	projection.ClaimID = successorClaimID
	projection.Lifecycle = "active"
	return writeWorkLogProjection(worktree, projection)
}

// recoverFailedRecycleClaim gives a checkout moved back to its original path
// one new active recovery identity after the prior claim was durably sealed as
// recycled. It never revives the terminal claim. The deterministic successor
// makes a repeated rollback idempotent and keeps the old identity out of a new
// task path.
func recoverFailedRecycleClaim(home, worktree, finalCommit string, prior workLogProjection) error {
	runDir, _, err := openWorkLogRun(home, prior.EffortID, prior.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claimLock, err := lockClaim(runDir, prior.ClaimID)
	if err != nil {
		return err
	}
	defer claimLock()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, prior.ClaimID+".json", &claim); err != nil {
		return err
	}
	if err := corroborateClaim(worktree, finalCommit, prior, claim); err != nil {
		return err
	}
	terminals, err := openPrivateChild(runDir, "terminals", false)
	if err != nil {
		return err
	}
	var terminal workLogTerminalRecord
	if err := readJSONAt(terminals, prior.ClaimID+".json", &terminal); err != nil {
		_ = terminals.Close()
		return fmt.Errorf("read failed-recycle terminal: %w", err)
	}
	_ = terminals.Close()
	if terminal.Disposition != "recycled" || terminal.FinalCommit != finalCommit || terminal.Lifecycle != "terminal" {
		return fmt.Errorf("prior claim is not a matching recycled terminal")
	}
	const recoveryAgent = "wb-recycle-recovery"
	const recoveryVia = "recycle_failed"
	recoveryID := successorWorkLogClaimID(prior.ClaimID, recoveryAgent, recoveryVia)
	recovery := claim
	recovery.ClaimID = recoveryID
	recovery.Lifecycle = "active"
	recovery.RecordedAt = terminal.SealedAt
	recovery.AgentID = recoveryAgent
	recovery.ParentClaimID = prior.ClaimID
	recovery.AcquiredVia = recoveryVia
	if err := writeJSONImmutableAt(claims, recoveryID+".json", recovery, true); err != nil {
		return err
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.claimed", At: recovery.RecordedAt,
		EffortID: recovery.EffortID, RunID: recovery.RunID, ClaimID: recoveryID,
		Repository: recovery.Repository, Branch: recovery.Branch, Base: recovery.Base,
		BaseSHA: recovery.BaseSHA, Lifecycle: "active", Disposition: recoveryVia}
	if err := writeJSONImmutableAt(outbox, recovery.RunID+"-"+recoveryID+"-claimed.json", event, true); err != nil {
		return err
	}
	return writeWorkLogProjection(worktree, workLogProjection{Version: 1, EffortID: prior.EffortID, RunID: prior.RunID, ClaimID: recoveryID, Lifecycle: "active"})
}

func writeWorkLogTerminal(home string, runDir *os.File, claim workLogClaim, finalCommit, disposition, successorClaimID, successorAgentID string) (time.Time, error) {
	sealedAt := time.Now().UTC()
	claim.Lifecycle = "terminal"
	terminal := workLogTerminalRecord{workLogClaim: claim, FinalCommit: finalCommit,
		Disposition: disposition, SealedAt: sealedAt, SuccessorClaimID: successorClaimID, SuccessorAgentID: successorAgentID}
	terminals, err := openPrivateChild(runDir, "terminals", true)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = terminals.Close() }()
	terminalName := claim.ClaimID + ".json"
	var existing workLogTerminalRecord
	if err := readJSONAt(terminals, terminalName, &existing); err == nil {
		if existing.ClaimID != claim.ClaimID || existing.FinalCommit != finalCommit || existing.Disposition != disposition || existing.Lifecycle != "terminal" || existing.SuccessorClaimID != successorClaimID || existing.SuccessorAgentID != successorAgentID {
			return time.Time{}, fmt.Errorf("immutable terminal conflicts with requested transition")
		}
		sealedAt = existing.SealedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return time.Time{}, fmt.Errorf("inspect immutable terminal: %w", err)
	} else if err := writeJSONImmutableAt(terminals, terminalName, terminal, false); err != nil {
		return time.Time{}, fmt.Errorf("write immutable terminal: %w", err)
	}
	outbox, err := openWorkLogOutbox(home, claim.EffortID, true)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = outbox.Close() }()
	event := workLogPublicEvent{Version: 1, Type: "worktree.sealed", At: sealedAt, EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: finalCommit, Lifecycle: "terminal", Disposition: disposition}
	if err := writeJSONImmutableAt(outbox, claim.RunID+"-"+claim.ClaimID+"-sealed.json", event, true); err != nil {
		return time.Time{}, fmt.Errorf("write immutable terminal outbox: %w", err)
	}
	return sealedAt, nil
}

// preflightWorkLogSeal resolves and corroborates the projection/claim/live-Git
// chain without writing. Coordinated operations run this for every repository
// before terminalizing the first claim.
func preflightWorkLogSeal(home, worktree, finalCommit string) error {
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return err
	}
	return corroborateClaim(worktree, finalCommit, projection, claim)
}

func readWorkLogProjection(worktree string) (workLogProjection, error) {
	var projection workLogProjection
	directory, err := openWorkLogProjectionDirectory(worktree, false)
	if err != nil {
		return projection, err
	}
	defer func() { _ = directory.Close() }()
	if err := readJSONAt(directory, workLogProjectionName, &projection); err != nil {
		return projection, fmt.Errorf("decode work-log projection: %w", err)
	}
	if err := validateProjection(projection); err != nil {
		return projection, err
	}
	return projection, nil
}

// readWorkLogProjectionForClaim performs the one-way migration from the
// short-lived .wb-worklog.json pointer used by the first Hybrid Work Log
// implementation. The editable legacy pointer is never trusted: WB first
// validates its opaque IDs, loads the immutable private claim, and
// corroborates that claim against live Git. Only then does it write the
// approved .wb-worklog/recovery.json projection and unlink the old pointer.
func readWorkLogProjectionForClaim(home, worktree string) (workLogProjection, error) {
	projection, currentErr := readWorkLogProjection(worktree)
	legacy, legacyErr := readLegacyWorkLogProjection(worktree)
	switch {
	case currentErr == nil:
		if legacyErr == nil {
			if legacy != projection {
				return workLogProjection{}, fmt.Errorf("legacy and current work-log projections disagree")
			}
			if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
				return workLogProjection{}, err
			}
			if err := removeLegacyWorkLogProjection(worktree); err != nil {
				return workLogProjection{}, err
			}
		} else if !errors.Is(legacyErr, os.ErrNotExist) {
			return workLogProjection{}, legacyErr
		}
		return projection, nil
	case !errors.Is(currentErr, os.ErrNotExist):
		return workLogProjection{}, currentErr
	case errors.Is(legacyErr, os.ErrNotExist):
		return workLogProjection{}, errWorkLogProjectionNotFound
	case legacyErr != nil:
		return workLogProjection{}, legacyErr
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, legacy); err != nil {
		return workLogProjection{}, fmt.Errorf("corroborate legacy work-log projection: %v", err)
	}
	if err := writeWorkLogProjection(worktree, legacy); err != nil {
		return workLogProjection{}, fmt.Errorf("migrate legacy work-log projection: %w", err)
	}
	if err := removeLegacyWorkLogProjection(worktree); err != nil {
		return workLogProjection{}, err
	}
	return legacy, nil
}

func readLegacyWorkLogProjection(worktree string) (workLogProjection, error) {
	var projection workLogProjection
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return projection, err
	}
	defer func() { _ = root.Close() }()
	if err := readJSONAt(root, legacyWorkLogProjectionName, &projection); err != nil {
		return projection, err
	}
	if err := validateProjection(projection); err != nil {
		return projection, err
	}
	return projection, nil
}

func corroborateProjectionWithPrivateClaim(home, worktree string, projection workLogProjection) error {
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return err
	}
	head, err := git(context.Background(), worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return corroborateClaim(worktree, head, projection, claim)
}

func corroborateClaim(worktree, finalCommit string, projection workLogProjection, claim workLogClaim) error {
	if claim.Version != 1 || claim.EffortID != projection.EffortID || claim.RunID != projection.RunID || claim.ClaimID != projection.ClaimID || claim.Lifecycle != "active" {
		return fmt.Errorf("work-log projection does not match immutable active claim")
	}
	if !validSafeSegment(claim.EffortID) || !validSafeSegment(claim.RunID) || !validClaimID(claim.ClaimID) || filepath.Clean(claim.Worktree) != filepath.Clean(worktree) {
		return fmt.Errorf("private work-log claim identity/path mismatch")
	}
	wantID := workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	if claim.ParentClaimID != "" {
		if !validClaimID(claim.ParentClaimID) || claim.AgentID == "" || (claim.AcquiredVia != "handoff" && claim.AcquiredVia != "not_landed" && claim.AcquiredVia != "recycle_failed") {
			return fmt.Errorf("private successor claim metadata is invalid")
		}
		wantID = successorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia)
	}
	if wantID != claim.ClaimID {
		return fmt.Errorf("private work-log claim digest mismatch")
	}
	branch, err := git(context.Background(), worktree, "branch", "--show-current")
	if err != nil || branch != claim.Branch {
		return fmt.Errorf("live branch %q does not match private claim %q", branch, claim.Branch)
	}
	head, err := git(context.Background(), worktree, "rev-parse", "HEAD")
	if err != nil || head != finalCommit {
		return fmt.Errorf("live HEAD %q does not match terminal commit %q", head, finalCommit)
	}
	if !isGitObjectID(claim.BaseSHA) {
		return fmt.Errorf("private claim has invalid base SHA")
	}
	if _, err := git(context.Background(), worktree, "merge-base", "--is-ancestor", claim.BaseSHA, head); err != nil {
		return fmt.Errorf("live HEAD is not descended from claimed base %s: %w", claim.BaseSHA, err)
	}
	return nil
}

func removeWorkLogProjection(worktree string) error {
	directory, err := openWorkLogProjectionDirectory(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := unix.Unlinkat(int(directory.Fd()), workLogProjectionName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		_ = directory.Close()
		return fmt.Errorf("reset old work-log projection: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	_ = directory.Close()
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := unix.Unlinkat(int(root.Fd()), workLogProjectionDirectory, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTEMPTY) {
		return fmt.Errorf("remove empty work-log projection directory: %w", err)
	}
	return nil
}

func removeLegacyWorkLogProjection(worktree string) error {
	root, err := openAbsoluteDirectoryNoFollow(worktree, false)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := unix.Unlinkat(int(root.Fd()), legacyWorkLogProjectionName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove legacy work-log projection: %w", err)
	}
	return root.Sync()
}

func openWorkLogRun(home, effort, run string, create bool) (*os.File, string, error) {
	if !validSafeSegment(effort) || !validSafeSegment(run) {
		return nil, "", fmt.Errorf("invalid work-log effort/run identity")
	}
	homeDir, err := openAbsoluteDirectoryNoFollow(home, create)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = homeDir.Close() }()
	current := homeDir
	for _, segment := range []string{"worklogs", effort, "runs", run} {
		next, openErr := openPrivateChild(current, segment, create)
		if current != homeDir {
			_ = current.Close()
		}
		if openErr != nil {
			return nil, "", openErr
		}
		current = next
	}
	return current, filepath.Join(home, "worklogs", effort, "runs", run), nil
}

func openWorkLogOutbox(home, effort string, create bool) (*os.File, error) {
	if !validSafeSegment(effort) {
		return nil, fmt.Errorf("invalid work-log effort identity")
	}
	homeDir, err := openAbsoluteDirectoryNoFollow(home, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeDir.Close() }()
	worklogs, err := openPrivateChild(homeDir, "worklogs", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = worklogs.Close() }()
	effortDir, err := openPrivateChild(worklogs, effort, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = effortDir.Close() }()
	return openPrivateChild(effortDir, "outbox", create)
}

func openPrivateChild(parent *os.File, name string, create bool) (*os.File, error) {
	if !validSafeSegment(name) {
		return nil, fmt.Errorf("unsafe private directory segment %q", name)
	}
	var fd int
	var err error
	if create {
		fd, err = openOrCreateNoFollowDirectory(int(parent.Fd()), name)
	} else {
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, err
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "wb-worklog-"+name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap private directory %s", name)
	}
	return file, nil
}

func lockClaim(runDir *os.File, claimID string) (func(), error) {
	locks, err := openPrivateChild(runDir, "locks", true)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(locks.Fd()), claimID+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW, 0o600)
	_ = locks.Close()
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
}

func writeJSONImmutableAt(directory *os.File, name string, value any, idempotent bool) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return writeBytesImmutableAt(directory, name, content, 0o600, idempotent)
}

func writeBytesImmutableAt(directory *os.File, name string, content []byte, mode os.FileMode, idempotent bool) error {
	if strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return fmt.Errorf("unsafe immutable filename %q", name)
	}
	if existing, err := readBytesAt(directory, name); err == nil {
		if idempotent && bytes.Equal(existing, content) {
			return nil
		}
		return fmt.Errorf("immutable file already exists: %s", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := "." + name + ".tmp-" + hex.EncodeToString(random)
	fd, err := unix.Openat(int(directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := renameNoReplace(int(directory.Fd()), temporary, int(directory.Fd()), name); err != nil {
		if existing, readErr := readBytesAt(directory, name); idempotent && readErr == nil && bytes.Equal(existing, content) {
			return nil
		}
		return err
	}
	cleanup = false
	return directory.Sync()
}

func readBytesAt(directory *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}

func readJSONAt(directory *os.File, name string, target any) error {
	content, err := readBytesAt(directory, name)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writeBytesAtomic(filepath.Dir(path), filepath.Base(path), append(content, '\n'), mode)
}

func writeJSONAtomicAt(directory *os.File, name string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomicAt(directory, name, append(content, '\n'), mode)
}

func writeBytesAtomicAt(directory *os.File, name string, content []byte, mode os.FileMode) error {
	if directory == nil || strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return fmt.Errorf("unsafe atomic filename %q", name)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := "." + name + ".tmp-" + hex.EncodeToString(random)
	fd, err := unix.Openat(int(directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.Fd()), temporary, int(directory.Fd()), name); err != nil {
		return err
	}
	cleanup = false
	return directory.Sync()
}

func writeBytesAtomic(directory, name string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, name)); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
