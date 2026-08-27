// Package sessionpark stores a complete, durable checkpoint for a parked WB
// session. It deliberately contains no Git mutation or transport logic.
package sessionpark

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"golang.org/x/sys/unix"
)

const (
	SchemaVersion        = 1
	MaxContinuationBytes = 64 << 10
	MaxBundleBytes       = 4 << 20
	maxParkedWorktrees   = 1024
	SourceDirName        = "parked-sessions"
	BundleFileName       = "bundle.json"
	LocalNeutralDirName  = "local-resume-root"

	sourceBundleFileName       = BundleFileName
	sourceContinuationFileName = "continuation.md"
	sourceEventsDirName        = "events"
	sourceResumeLockName       = "resume.lock"
	sourceResumeRouteFileName  = "resume-route.json"
)

type Status string

const (
	StatusParked  Status = "parked"
	StatusResumed Status = "resumed"
)

// Worktree is exact local evidence at park time. Dirty content is intentionally
// not captured; it remains in place for local resume and fails closed remotely.
type Worktree struct {
	Repository string `json:"repository"`
	// RepositoryRemote is the credential-free canonical fetch URL captured at
	// park time. Remote resume never rediscovers this mutable local setting.
	RepositoryRemote string `json:"repository_remote,omitempty"`
	CanonicalDir     string `json:"canonical_dir,omitempty"`
	WorktreeDir      string `json:"worktree_dir"`
	WorktreesRoot    string `json:"worktrees_root,omitempty"`
	Branch           string `json:"branch"`
	Head             string `json:"head"`
	Dirty            bool   `json:"dirty"`
	Status           string `json:"status,omitempty"`
	RemoteHead       string `json:"remote_head,omitempty"`
	// WorkLogReference binds this member to the source session's exact active
	// claim. OwnerEventID binds it to the precise source custody record, so a
	// later sequential session cannot be mistaken for the parked owner.
	WorkLogReference string `json:"work_log_reference,omitempty"`
	OwnerEventID     string `json:"owner_event_id,omitempty"`
}

type Bundle struct {
	SchemaVersion   int            `json:"schema_version"`
	ParkedSessionID string         `json:"parked_session_id"`
	Source          session.Record `json:"source"`
	Continuation    string         `json:"continuation"`
	Worktrees       []Worktree     `json:"worktrees"`
	ParkedAt        time.Time      `json:"parked_at"`
}

type Event struct {
	SchemaVersion  int                `json:"schema_version"`
	Sequence       uint64             `json:"sequence"`
	Type           string             `json:"type"`
	At             time.Time          `json:"at"`
	Successor      *session.Record    `json:"successor,omitempty"`
	RemoteResumeID string             `json:"remote_resume_id,omitempty"`
	RequestDigest  sessionmove.Digest `json:"request_digest,omitempty"`
	TargetMachine  string             `json:"target_machine,omitempty"`
}

type State struct {
	Bundle        Bundle          `json:"bundle"`
	Events        []Event         `json:"events"`
	Status        Status          `json:"status"`
	Successor     *session.Record `json:"successor,omitempty"`
	RemoteReceipt *Receipt        `json:"remote_receipt,omitempty"`
	ResumeRoute   *ResumeRoute    `json:"resume_route,omitempty"`
}

type ResumeRoute struct {
	SchemaVersion   int       `json:"schema_version"`
	ParkedSessionID string    `json:"parked_session_id"`
	Mode            string    `json:"mode"`
	TargetMachine   string    `json:"target_machine,omitempty"`
	Courier         string    `json:"courier,omitempty"`
	SSHHost         string    `json:"ssh_host,omitempty"`
	SSHUser         string    `json:"ssh_user,omitempty"`
	ClaimedAt       time.Time `json:"claimed_at"`
}

const (
	ResumeRouteLocal  = "local"
	ResumeRouteRemote = "remote"
)

type Store struct{ Root string }

type SourceLock struct {
	mu         sync.Mutex
	root       *os.File
	aggregate  *os.File
	bundleFile *os.File
	file       *os.File
	rootPath   string
	parkID     string
	bundle     Bundle
}

type RemoteAdmission struct {
	Envelope Envelope
	Raw      []byte
	Digest   sessionmove.Digest
	Route    ResumeRoute
	Replay   bool
}

func NewStore(root string) Store { return Store{Root: root} }

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate parked session ID: %w", err)
	}
	return "park-" + hex.EncodeToString(b[:]), nil
}

func (s Store) Create(bundle Bundle) (Bundle, error) {
	if err := validateBundle(bundle); err != nil {
		return Bundle{}, err
	}
	raw, err := EncodeBundle(bundle)
	if err != nil {
		return Bundle{}, err
	}
	root, err := openPrivateStoreRoot(s.Root, true)
	if err != nil {
		return Bundle{}, err
	}
	defer func() { _ = root.Close() }()
	if err := unix.Mkdirat(int(root.Fd()), bundle.ParkedSessionID, 0o700); err != nil {
		return Bundle{}, fmt.Errorf("create parked session aggregate: %w", err)
	}
	aggregate, err := openPrivateDirectoryAt(root, bundle.ParkedSessionID)
	if err != nil {
		return Bundle{}, err
	}
	defer func() { _ = aggregate.Close() }()
	if err := writeExactPrivateAt(aggregate, sourceBundleFileName, raw); err != nil {
		return Bundle{}, fmt.Errorf("persist exact parked session bundle: %w", err)
	}
	if err := writeExactPrivateAt(aggregate, sourceContinuationFileName, []byte(bundle.Continuation)); err != nil {
		return Bundle{}, fmt.Errorf("persist private parked session continuation: %w", err)
	}
	if err := unix.Mkdirat(int(aggregate.Fd()), sourceEventsDirName, 0o700); err != nil {
		return Bundle{}, err
	}
	events, err := openPrivateDirectoryAt(aggregate, sourceEventsDirName)
	if err != nil {
		return Bundle{}, err
	}
	if err := events.Sync(); err != nil {
		_ = events.Close()
		return Bundle{}, err
	}
	_ = events.Close()
	if err := aggregate.Sync(); err != nil {
		return Bundle{}, err
	}
	if err := root.Sync(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

// FindBySource returns the existing aggregate for a source declaration. It is
// used to repair a crash between aggregate publication and lifecycle marking,
// so retry never allocates a second parked identity.
func (s Store) FindBySource(wbSessionID string) (Bundle, bool, error) {
	if !sessionauthority.ValidID(wbSessionID) {
		return Bundle{}, false, fmt.Errorf("source WB session ID is invalid")
	}
	root, err := openPrivateStoreRoot(s.Root, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return Bundle{}, false, nil
	}
	if err != nil {
		return Bundle{}, false, err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Seek(0, io.SeekStart); err != nil {
		return Bundle{}, false, err
	}
	entries, err := root.ReadDir(-1)
	if err != nil {
		return Bundle{}, false, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() || !validParkID(entry.Name()) {
			return Bundle{}, false, fmt.Errorf("unexpected parked session store artifact %q", entry.Name())
		}
		state, loadErr := s.Load(entry.Name())
		if loadErr != nil {
			return Bundle{}, false, loadErr
		}
		if state.Bundle.Source.WBSessionID == wbSessionID {
			return state.Bundle, true, nil
		}
	}
	return Bundle{}, false, nil
}

func (s Store) Load(id string) (State, error) {
	if !validParkID(id) {
		return State{}, fmt.Errorf("invalid parked session ID")
	}
	root, err := openPrivateStoreRoot(s.Root, false)
	if err != nil {
		return State{}, fmt.Errorf("open parked session store: %w", err)
	}
	defer func() { _ = root.Close() }()
	aggregate, err := openPrivateDirectoryAt(root, id)
	if err != nil {
		return State{}, fmt.Errorf("open parked session aggregate: %w", err)
	}
	defer func() { _ = aggregate.Close() }()
	bundle, _, err := loadBundleAt(aggregate, id)
	if err != nil {
		return State{}, err
	}
	return loadSourceStateAt(aggregate, bundle)
}

func (s Store) Resume(id string, successor session.Record, now time.Time) (State, error) {
	lock, err := s.Acquire(context.Background(), id)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = lock.Close() }()
	if _, _, err := s.PrepareLocalUnderLock(lock, now); err != nil {
		return State{}, err
	}
	return s.ResumeUnderLock(lock, successor, now)
}

func validateBundle(bundle Bundle) error {
	if bundle.SchemaVersion != SchemaVersion {
		return fmt.Errorf("parked session schema_version %d unsupported; want %d", bundle.SchemaVersion, SchemaVersion)
	}
	if !validParkID(bundle.ParkedSessionID) {
		return fmt.Errorf("parked session ID is invalid")
	}
	if bundle.Source.PID <= 0 || !sessionauthority.ValidID(bundle.Source.WBSessionID) ||
		!sessionauthority.ValidID(bundle.Source.Machine) || strings.TrimSpace(bundle.Source.Runtime) == "" ||
		bundle.Source.StartedAt.IsZero() || bundle.ParkedAt.IsZero() {
		return fmt.Errorf("parked session source identity is incomplete")
	}
	if len(bundle.Continuation) == 0 || len([]byte(bundle.Continuation)) > MaxContinuationBytes || !utf8.ValidString(bundle.Continuation) {
		return fmt.Errorf("parked session continuation must be valid UTF-8 and between 1 and %d bytes", MaxContinuationBytes)
	}
	if len(bundle.Worktrees) > maxParkedWorktrees {
		return fmt.Errorf("parked session owns more than %d worktrees", maxParkedWorktrees)
	}
	seen := make(map[string]struct{}, len(bundle.Worktrees))
	for index, member := range bundle.Worktrees {
		if strings.TrimSpace(member.Repository) == "" || strings.ContainsAny(member.Repository+member.Branch, "\r\n") ||
			!filepath.IsAbs(member.WorktreeDir) || filepath.Clean(member.WorktreeDir) != member.WorktreeDir ||
			strings.TrimSpace(member.Branch) == "" || !gitObjectID.MatchString(member.Head) {
			return fmt.Errorf("parked session worktree %d is invalid", index)
		}
		if member.RepositoryRemote != "" {
			remote, err := gitremote.Parse(member.RepositoryRemote)
			if err != nil || remote.Identity.Repository != member.Repository {
				return fmt.Errorf("parked session worktree %d remote is unsafe or conflicts with repository", index)
			}
		}
		if member.RemoteHead != "" && !gitObjectID.MatchString(member.RemoteHead) {
			return fmt.Errorf("parked session worktree %d remote head is invalid", index)
		}
		if member.WorkLogReference != "" {
			if _, err := sessionmove.ParseWorkLogReference(member.WorkLogReference); err != nil {
				return fmt.Errorf("parked session worktree %d Work Log reference: %w", index, err)
			}
		}
		if member.OwnerEventID != "" && !validSHA256Hex(member.OwnerEventID) {
			return fmt.Errorf("parked session worktree %d owner event identity is invalid", index)
		}
		if _, exists := seen[member.WorktreeDir]; exists {
			return fmt.Errorf("parked session worktree path %q is duplicated", member.WorktreeDir)
		}
		seen[member.WorktreeDir] = struct{}{}
	}
	return nil
}

func EncodeBundle(bundle Bundle) ([]byte, error) {
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxBundleBytes {
		return nil, fmt.Errorf("parked session bundle exceeds %d bytes", MaxBundleBytes)
	}
	return raw, nil
}

func DecodeBundle(raw []byte) (Bundle, error) {
	var bundle Bundle
	if len(raw) == 0 || len(raw) > MaxBundleBytes {
		return bundle, fmt.Errorf("parked session bundle must be non-empty and bounded")
	}
	if err := strictDecode(raw, &bundle); err != nil {
		return bundle, fmt.Errorf("parse parked session bundle: %w", err)
	}
	if err := validateBundle(bundle); err != nil {
		return bundle, err
	}
	canonical, err := EncodeBundle(bundle)
	if err != nil || !bytes.Equal(canonical, raw) {
		return bundle, fmt.Errorf("parked session bundle must use WB canonical JSON encoding")
	}
	return bundle, nil
}
func EqualBundle(a, b Bundle) bool {
	ar, _ := EncodeBundle(a)
	br, _ := EncodeBundle(b)
	return bytes.Equal(ar, br)
}

func validParkID(id string) bool {
	return strings.HasPrefix(id, "park-") && sessionauthority.ValidID(id)
}

func sourceEnvelopeName(target string) string { return "remote-" + target + ".json" }
func sourceReceiptName(target string) string  { return "receipt-" + target + ".json" }

func (s Store) Acquire(ctx context.Context, id string) (*SourceLock, error) {
	if !validParkID(id) {
		return nil, fmt.Errorf("invalid parked session ID")
	}
	rootPath, err := cleanAbsoluteStoreRoot(s.Root)
	if err != nil {
		return nil, err
	}
	root, err := openPrivateStoreRoot(rootPath, false)
	if err != nil {
		return nil, err
	}
	aggregate, err := openPrivateDirectoryAt(root, id)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	bundleFD, err := unix.Openat(int(aggregate.Fd()), sourceBundleFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = aggregate.Close()
		_ = root.Close()
		return nil, err
	}
	bundleFile := os.NewFile(uintptr(bundleFD), "wb-parked-source-bundle")
	bundleRaw, err := readPrivateFile(bundleFile, MaxBundleBytes)
	if err != nil {
		_ = bundleFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, err
	}
	var bundle Bundle
	decodeErr := strictDecode(bundleRaw, &bundle)
	validationErr := validateBundle(bundle)
	if decodeErr != nil || validationErr != nil || bundle.ParkedSessionID != id {
		_ = bundleFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, fmt.Errorf("parked session bundle identity is invalid")
	}
	canonical, _ := EncodeBundle(bundle)
	if !bytes.Equal(bundleRaw, canonical) {
		_ = bundleFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, fmt.Errorf("parked session bundle is not canonical")
	}
	lockFD, err := openOrCreateRegularAt(int(aggregate.Fd()), sourceResumeLockName, 0o600)
	if err != nil {
		_ = bundleFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, err
	}
	file := os.NewFile(uintptr(lockFD), "wb-parked-source-resume-lock")
	for {
		if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return &SourceLock{root: root, aggregate: aggregate, bundleFile: bundleFile, file: file,
				rootPath: rootPath, parkID: id, bundle: bundle}, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			_ = bundleFile.Close()
			_ = aggregate.Close()
			_ = root.Close()
			return nil, fmt.Errorf("lock parked session resume: %w", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			_ = bundleFile.Close()
			_ = aggregate.Close()
			_ = root.Close()
			return nil, fmt.Errorf("wait for parked session resume fence: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (lock *SourceLock) HeldForSession(storeRoot, parkID, digest string) bool {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return lock.heldLocked(storeRoot, parkID, digest)
}

func (lock *SourceLock) held(storeRoot, parkID string) bool {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return lock.heldLocked(storeRoot, parkID, "")
}

func (lock *SourceLock) heldLocked(storeRoot, parkID, digest string) bool {
	if lock == nil || lock.root == nil || lock.aggregate == nil || lock.bundleFile == nil || lock.file == nil || lock.parkID != parkID {
		return false
	}
	wantBundle, wantErr := EncodeBundle(lock.bundle)
	if wantErr != nil || digest != "" && string(sessionmove.DigestBytes(wantBundle)) != digest {
		return false
	}
	rootPath, err := filepath.Abs(storeRoot)
	if err != nil || filepath.Clean(rootPath) != lock.rootPath {
		return false
	}
	root, err := openPrivateStoreRoot(rootPath, false)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	if !sameFile(lock.root, root) {
		return false
	}
	aggregate, err := openPrivateDirectoryAt(root, parkID)
	if err != nil {
		return false
	}
	defer func() { _ = aggregate.Close() }()
	if !sameFile(lock.aggregate, aggregate) {
		return false
	}
	bundleFD, err := unix.Openat(int(aggregate.Fd()), sourceBundleFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	bundleFile := os.NewFile(uintptr(bundleFD), "wb-parked-source-bundle-check")
	defer func() { _ = bundleFile.Close() }()
	if !sameFile(lock.bundleFile, bundleFile) {
		return false
	}
	raw, err := readPrivateFile(bundleFile, MaxBundleBytes)
	if err != nil || !bytes.Equal(raw, wantBundle) {
		return false
	}
	lockFD, err := unix.Openat(int(aggregate.Fd()), sourceResumeLockName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(lockFD), "wb-parked-source-lock-check")
	defer func() { _ = file.Close() }()
	return sameFile(lock.file, file)
}

func (lock *SourceLock) RetainSessionDir(storeRoot, parkID, digest string) (*os.File, error) {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if !lock.heldLocked(storeRoot, parkID, digest) {
		return nil, fmt.Errorf("parked source lock does not retain the exact admitted aggregate")
	}
	fd, err := unix.Dup(int(lock.aggregate.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "wb-parked-source-retained-aggregate"), nil
}

func (lock *SourceLock) Bundle() Bundle { return lock.bundle }

func (lock *SourceLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	var errs []error
	if lock.file != nil {
		errs = append(errs, unix.Flock(int(lock.file.Fd()), unix.LOCK_UN), lock.file.Close())
	}
	for _, file := range []*os.File{lock.bundleFile, lock.aggregate, lock.root} {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	lock.file, lock.bundleFile, lock.aggregate, lock.root = nil, nil, nil, nil
	return errors.Join(errs...)
}

func (s Store) LoadUnderLock(lock *SourceLock) (State, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return State{}, fmt.Errorf("load parked session requires retained resume authority")
	}
	return loadSourceStateAt(lock.aggregate, lock.bundle)
}

// ContinuationPathUnderLock returns the deterministic private local-resume
// artifact only after re-reading it through the retained aggregate descriptor.
// Callers must pass it through WB_SESSION_CONTINUATION_FILE, never stdout or
// harness argv.
func (s Store) ContinuationPathUnderLock(lock *SourceLock) (string, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return "", fmt.Errorf("read parked continuation requires retained source authority")
	}
	raw, err := readPrivateRegularAt(lock.aggregate, sourceContinuationFileName, MaxContinuationBytes)
	if err != nil || !bytes.Equal(raw, []byte(lock.bundle.Continuation)) {
		return "", fmt.Errorf("private parked continuation conflicts with exact source bundle")
	}
	return filepath.Join(s.Root, lock.parkID, sourceContinuationFileName), nil
}

// EnsureLocalSuccessorContextUnderLock publishes the single private file read
// by a local successor. It binds the original continuation to every retained
// member path without copying or modifying worktree bytes.
func (s Store) EnsureLocalSuccessorContextUnderLock(lock *SourceLock) (string, []byte, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return "", nil, fmt.Errorf("publish local successor context requires retained source authority")
	}
	if _, err := s.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil {
		return "", nil, err
	}
	var body strings.Builder
	body.WriteString(lock.bundle.Continuation)
	if !strings.HasSuffix(lock.bundle.Continuation, "\n") {
		body.WriteByte('\n')
	}
	body.WriteString("\nRetained local worktrees:\n")
	if len(lock.bundle.Worktrees) == 0 {
		body.WriteString("- none\n")
	}
	for index, member := range lock.bundle.Worktrees {
		fmt.Fprintf(&body, "- member-%03d %s\n  path: %s\n  branch: %s\n  commit: %s\n  work_log: %s\n",
			index+1, member.Repository, member.WorktreeDir, member.Branch, member.Head, member.WorkLogReference)
	}
	raw := []byte(body.String())
	if len(raw) > MaxSuccessorContextBytes {
		return "", nil, fmt.Errorf("private local successor context exceeds %d bytes", MaxSuccessorContextBytes)
	}
	if err := writeExactPrivateAt(lock.aggregate, SuccessorContextFileName, raw); err != nil {
		return "", nil, err
	}
	return filepath.Join(s.Root, lock.parkID, SuccessorContextFileName), raw, nil
}

func (s Store) LoadLocalSuccessorContextUnderLock(lock *SourceLock) (string, []byte, bool, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return "", nil, false, fmt.Errorf("inspect local successor context requires retained source authority")
	}
	if _, err := s.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil {
		return "", nil, false, err
	}
	raw, err := readPrivateRegularAt(lock.aggregate, SuccessorContextFileName, MaxSuccessorContextBytes)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if !bytes.HasPrefix(raw, []byte(lock.bundle.Continuation)) {
		return "", nil, false, fmt.Errorf("private local successor context conflicts with exact parked bundle")
	}
	return filepath.Join(s.Root, lock.parkID, SuccessorContextFileName), raw, true, nil
}

func (s Store) ExistingLocalLaunchRootUnderLock(lock *SourceLock) (string, bool, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return "", false, fmt.Errorf("inspect local launch root requires retained source authority")
	}
	if _, err := s.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil {
		return "", false, err
	}
	if len(lock.bundle.Worktrees) != 0 {
		return lock.bundle.Worktrees[0].WorktreeDir, true, nil
	}
	neutral, err := openPrivateDirectoryAt(lock.aggregate, LocalNeutralDirName)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_ = neutral.Close()
	return filepath.Join(s.Root, lock.parkID, LocalNeutralDirName), true, nil
}

// LocalLaunchRootUnderLock returns the first exact retained member, or creates
// the deterministic aggregate-bound 0700 neutral root for a zero-member park.
func (s Store) LocalLaunchRootUnderLock(lock *SourceLock) (string, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return "", fmt.Errorf("select local launch root requires retained source authority")
	}
	if _, err := s.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil {
		return "", err
	}
	if len(lock.bundle.Worktrees) != 0 {
		return lock.bundle.Worktrees[0].WorktreeDir, nil
	}
	if err := unix.Mkdirat(int(lock.aggregate.Fd()), LocalNeutralDirName, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return "", fmt.Errorf("create private local resume root: %w", err)
	}
	neutral, err := openPrivateDirectoryAt(lock.aggregate, LocalNeutralDirName)
	if err != nil {
		return "", fmt.Errorf("open private local resume root: %w", err)
	}
	defer func() { _ = neutral.Close() }()
	if err := neutral.Sync(); err != nil {
		return "", err
	}
	if err := lock.aggregate.Sync(); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, lock.parkID, LocalNeutralDirName), nil
}

// PrepareLocalUnderLock durably selects the only permitted resume route before
// any local context, launch plan, or successor process can be created.
func (s Store) PrepareLocalUnderLock(lock *SourceLock, now time.Time) (ResumeRoute, bool, error) {
	return s.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, now)
}

func (s Store) PrepareRemoteUnderLock(lock *SourceLock, target, requestedHarness, courier string, ssh sessionmove.SSHConfig, now time.Time) (RemoteAdmission, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return RemoteAdmission{}, fmt.Errorf("prepare remote park resume requires retained source authority")
	}
	if !sessionauthority.ValidID(target) {
		return RemoteAdmission{}, fmt.Errorf("target machine is not one fixed safe ID")
	}
	if courier != string(sessionmove.CourierSSH) || ssh.WBPath != "" {
		return RemoteAdmission{}, fmt.Errorf("parked-session remote route must use the fixed SSH courier")
	}
	if err := ssh.Validate(); err != nil {
		return RemoteAdmission{}, fmt.Errorf("parked-session SSH route is invalid")
	}
	state, err := s.LoadUnderLock(lock)
	if err != nil {
		return RemoteAdmission{}, err
	}
	if state.Status == StatusResumed {
		return RemoteAdmission{}, fmt.Errorf("parked session was already resumed by a different target")
	}
	route, _, err := s.claimResumeRouteUnderLock(lock, ResumeRouteRemote, target, courier, ssh, now)
	if err != nil {
		return RemoteAdmission{}, err
	}
	name := sourceEnvelopeName(target)
	raw, err := readPrivateRegularAt(lock.aggregate, name, MaxEnvelopeBytes)
	if err == nil {
		envelope, decodeErr := DecodeEnvelope(raw)
		if decodeErr != nil || envelope.Request.ParkedSessionID != lock.parkID || envelope.Request.TargetMachine != target ||
			envelope.Request.RequestedHarness != strings.TrimSpace(requestedHarness) {
			return RemoteAdmission{}, fmt.Errorf("durable remote park resume envelope conflicts with requested target or harness")
		}
		return RemoteAdmission{Envelope: envelope, Raw: raw, Digest: sessionmove.DigestBytes(raw), Route: route, Replay: true}, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, unix.ENOENT) {
		return RemoteAdmission{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	request := BuildRemoteRequest(lock.bundle, target, requestedHarness, now.UTC())
	envelope := Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: request}
	raw, err = EncodeEnvelope(envelope)
	if err != nil {
		return RemoteAdmission{}, err
	}
	if err := writeExactPrivateAt(lock.aggregate, name, raw); err != nil {
		return RemoteAdmission{}, err
	}
	return RemoteAdmission{Envelope: envelope, Raw: raw, Digest: sessionmove.DigestBytes(raw), Route: route}, nil
}

func (s Store) validateRemoteAdmission(lock *SourceLock, admission RemoteAdmission) error {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return fmt.Errorf("remote park resume requires retained source authority")
	}
	durableRoute, err := s.validateResumeRouteUnderLock(lock, ResumeRouteRemote, admission.Envelope.Request.TargetMachine)
	if err != nil {
		return err
	}
	if durableRoute != admission.Route {
		return fmt.Errorf("remote park resume admission is not bound to the exact durable courier route")
	}
	canonical, err := EncodeEnvelope(admission.Envelope)
	if err != nil || !bytes.Equal(canonical, admission.Raw) || admission.Digest != sessionmove.DigestBytes(admission.Raw) ||
		admission.Envelope.Request.ParkedSessionID != lock.parkID {
		return fmt.Errorf("remote park resume admission is not the exact durable envelope")
	}
	durable, err := readPrivateRegularAt(lock.aggregate, sourceEnvelopeName(admission.Envelope.Request.TargetMachine), MaxEnvelopeBytes)
	if err != nil || !bytes.Equal(durable, admission.Raw) {
		return fmt.Errorf("remote park resume envelope changed after durable admission")
	}
	return nil
}

func (s Store) LoadRemoteReceiptUnderLock(lock *SourceLock, admission RemoteAdmission) (*Receipt, error) {
	if err := s.validateRemoteAdmission(lock, admission); err != nil {
		return nil, err
	}
	raw, err := readPrivateRegularAt(lock.aggregate, sourceReceiptName(admission.Envelope.Request.TargetMachine), MaxEnvelopeBytes)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	receipt, err := DecodeReceipt(raw)
	if err != nil {
		return nil, err
	}
	if err := ValidateReceipt(receipt, admission.Envelope.Request, admission.Digest); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// SaveRemoteReceiptUnderLock is the source acknowledgement durability
// boundary. A crash after this write is repaired without courier redelivery.
func (s Store) SaveRemoteReceiptUnderLock(lock *SourceLock, admission RemoteAdmission, receipt Receipt) error {
	if err := s.validateRemoteAdmission(lock, admission); err != nil {
		return err
	}
	if err := ValidateReceipt(receipt, admission.Envelope.Request, admission.Digest); err != nil {
		return err
	}
	raw, err := EncodeReceipt(receipt)
	if err != nil {
		return err
	}
	return writeExactPrivateAt(lock.aggregate, sourceReceiptName(admission.Envelope.Request.TargetMachine), raw)
}

func (s Store) FinalizeRemoteUnderLock(lock *SourceLock, admission RemoteAdmission, now time.Time) (State, error) {
	if err := s.validateRemoteAdmission(lock, admission); err != nil {
		return State{}, err
	}
	state, err := s.LoadUnderLock(lock)
	if err != nil || state.Status == StatusResumed {
		return state, err
	}
	receipt, err := s.LoadRemoteReceiptUnderLock(lock, admission)
	if err != nil || receipt == nil {
		if err == nil {
			err = fmt.Errorf("remote park resume has no durable validated receipt")
		}
		return State{}, err
	}
	event := resumedEvent(state, ReceiptSession(*receipt), now)
	event.RemoteResumeID, event.RequestDigest, event.TargetMachine = admission.Envelope.Request.ResumeID, admission.Digest, admission.Envelope.Request.TargetMachine
	if err := appendSourceEventAt(lock.aggregate, lock.parkID, event); err != nil {
		return State{}, err
	}
	return s.LoadUnderLock(lock)
}

func (s Store) ResumeUnderLock(lock *SourceLock, successor session.Record, now time.Time) (State, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return State{}, fmt.Errorf("local park resume requires retained source authority")
	}
	if _, err := s.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil {
		return State{}, err
	}
	state, err := s.LoadUnderLock(lock)
	if err != nil || state.Status == StatusResumed {
		return state, err
	}
	if successor.PID <= 0 || !sessionauthority.ValidID(successor.WBSessionID) {
		return State{}, fmt.Errorf("successor must have a stable WB session ID and positive PID")
	}
	if successor.PredecessorWBSessionID == "" {
		successor.PredecessorWBSessionID = lock.bundle.Source.WBSessionID
	}
	if successor.WBSessionID == lock.bundle.Source.WBSessionID || successor.PredecessorWBSessionID != lock.bundle.Source.WBSessionID {
		return State{}, fmt.Errorf("successor lineage does not descend from parked source session")
	}
	if err := appendSourceEventAt(lock.aggregate, lock.parkID, resumedEvent(state, successor, now)); err != nil {
		return State{}, err
	}
	return s.LoadUnderLock(lock)
}

func resumedEvent(state State, successor session.Record, now time.Time) Event {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return Event{SchemaVersion: SchemaVersion, Sequence: uint64(len(state.Events) + 1), Type: "resumed", At: now.UTC(), Successor: &successor}
}

func (s Store) claimResumeRouteUnderLock(lock *SourceLock, mode, target, courier string, ssh sessionmove.SSHConfig, now time.Time) (ResumeRoute, bool, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return ResumeRoute{}, false, fmt.Errorf("claim park resume route requires retained source authority")
	}
	if mode != ResumeRouteLocal && mode != ResumeRouteRemote {
		return ResumeRoute{}, false, fmt.Errorf("park resume route mode is invalid")
	}
	if (mode == ResumeRouteLocal && (target != "" || courier != "" || ssh != (sessionmove.SSHConfig{}))) ||
		(mode == ResumeRouteRemote && (!sessionauthority.ValidID(target) || courier != string(sessionmove.CourierSSH) || ssh.WBPath != "" || ssh.Validate() != nil)) {
		return ResumeRoute{}, false, fmt.Errorf("park resume route target is invalid")
	}
	existing, found, err := loadResumeRouteAt(lock.aggregate, lock.parkID)
	if err != nil {
		return ResumeRoute{}, false, err
	}
	if found {
		if existing.Mode != mode || existing.TargetMachine != target || existing.Courier != courier || existing.SSHHost != ssh.Host || existing.SSHUser != ssh.User {
			return ResumeRoute{}, false, fmt.Errorf("parked session resume route is already claimed by %s", resumeRouteLabel(existing))
		}
		return existing, true, nil
	}
	state, err := s.LoadUnderLock(lock)
	if err != nil {
		return ResumeRoute{}, false, err
	}
	if state.Status == StatusResumed {
		return ResumeRoute{}, false, fmt.Errorf("resumed parked session has no authenticated route marker")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	route := ResumeRoute{SchemaVersion: SchemaVersion, ParkedSessionID: lock.parkID, Mode: mode, TargetMachine: target,
		Courier: courier, SSHHost: ssh.Host, SSHUser: ssh.User, ClaimedAt: now.UTC()}
	raw, err := jsonMarshal(route)
	if err != nil {
		return ResumeRoute{}, false, err
	}
	if err := writeExactPrivateAt(lock.aggregate, sourceResumeRouteFileName, raw); err != nil {
		return ResumeRoute{}, false, err
	}
	return route, false, nil
}

func (s Store) validateResumeRouteUnderLock(lock *SourceLock, mode, target string) (ResumeRoute, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return ResumeRoute{}, fmt.Errorf("validate park resume route requires retained source authority")
	}
	route, found, err := loadResumeRouteAt(lock.aggregate, lock.parkID)
	if err != nil {
		return ResumeRoute{}, err
	}
	if !found || route.Mode != mode || route.TargetMachine != target {
		if found {
			return ResumeRoute{}, fmt.Errorf("parked session resume route is claimed by %s, not %s", resumeRouteLabel(route), resumeRouteLabel(ResumeRoute{Mode: mode, TargetMachine: target}))
		}
		return ResumeRoute{}, fmt.Errorf("parked session resume route is not durably claimed")
	}
	return route, nil
}

func (route ResumeRoute) SSHConfig() (sessionmove.SSHConfig, error) {
	config := sessionmove.SSHConfig{Host: route.SSHHost, User: route.SSHUser}
	if route.Mode != ResumeRouteRemote || route.Courier != string(sessionmove.CourierSSH) || config.Validate() != nil {
		return sessionmove.SSHConfig{}, fmt.Errorf("stored parked-session SSH route is invalid")
	}
	return config, nil
}

func loadResumeRouteAt(aggregate *os.File, parkID string) (ResumeRoute, bool, error) {
	raw, err := readPrivateRegularAt(aggregate, sourceResumeRouteFileName, 16<<10)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return ResumeRoute{}, false, nil
	}
	if err != nil {
		return ResumeRoute{}, false, err
	}
	var route ResumeRoute
	if err := strictDecode(raw, &route); err != nil {
		return ResumeRoute{}, false, fmt.Errorf("decode parked session resume route: %w", err)
	}
	canonical, marshalErr := jsonMarshal(route)
	ssh := sessionmove.SSHConfig{Host: route.SSHHost, User: route.SSHUser}
	if marshalErr != nil || !bytes.Equal(raw, canonical) || route.SchemaVersion != SchemaVersion || route.ParkedSessionID != parkID || route.ClaimedAt.IsZero() ||
		(route.Mode != ResumeRouteLocal && route.Mode != ResumeRouteRemote) ||
		(route.Mode == ResumeRouteLocal && (route.TargetMachine != "" || route.Courier != "" || route.SSHHost != "" || route.SSHUser != "")) ||
		(route.Mode == ResumeRouteRemote && (!sessionauthority.ValidID(route.TargetMachine) || route.Courier != string(sessionmove.CourierSSH) || ssh.Validate() != nil)) {
		return ResumeRoute{}, false, fmt.Errorf("parked session resume route is invalid")
	}
	return route, true, nil
}

func resumeRouteLabel(route ResumeRoute) string {
	if route.Mode == ResumeRouteRemote {
		return ResumeRouteRemote + ":" + route.TargetMachine
	}
	return route.Mode
}

func appendSourceEventAt(aggregate *os.File, parkID string, event Event) error {
	events, err := openPrivateDirectoryAt(aggregate, sourceEventsDirName)
	if err != nil {
		return err
	}
	defer func() { _ = events.Close() }()
	history, err := listSourceEventsAt(events, parkID)
	if err != nil {
		return err
	}
	if len(history) != int(event.Sequence)-1 {
		if len(history) != 0 && history[len(history)-1].Type == "resumed" {
			return nil
		}
		return fmt.Errorf("parked session event sequence changed under resume fence")
	}
	raw, err := jsonMarshal(event)
	if err != nil {
		return err
	}
	created, err := writeImmutableAt(events, fmt.Sprintf("%020d.json", event.Sequence), raw, 0o600)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("parked session resumed event already exists with unknown bytes")
	}
	return events.Sync()
}

func loadBundleAt(aggregate *os.File, id string) (Bundle, []byte, error) {
	raw, err := readPrivateRegularAt(aggregate, sourceBundleFileName, MaxBundleBytes)
	if err != nil {
		return Bundle{}, nil, fmt.Errorf("load parked session bundle: %w", err)
	}
	var bundle Bundle
	if err := strictDecode(raw, &bundle); err != nil {
		return Bundle{}, nil, fmt.Errorf("parse parked session bundle: %w", err)
	}
	if err := validateBundle(bundle); err != nil || bundle.ParkedSessionID != id {
		return Bundle{}, nil, fmt.Errorf("parked session bundle identity is invalid")
	}
	canonical, _ := EncodeBundle(bundle)
	if !bytes.Equal(raw, canonical) {
		return Bundle{}, nil, fmt.Errorf("parked session bundle must use WB's canonical JSON encoding")
	}
	return bundle, raw, nil
}

func loadSourceStateAt(aggregate *os.File, bundle Bundle) (State, error) {
	events, err := openPrivateDirectoryAt(aggregate, sourceEventsDirName)
	if err != nil {
		return State{}, err
	}
	history, err := listSourceEventsAt(events, bundle.ParkedSessionID)
	_ = events.Close()
	if err != nil {
		return State{}, err
	}
	state := State{Bundle: bundle, Events: history, Status: StatusParked}
	if route, found, routeErr := loadResumeRouteAt(aggregate, bundle.ParkedSessionID); routeErr != nil {
		return State{}, routeErr
	} else if found {
		state.ResumeRoute = &route
	}
	for index := range history {
		event := history[index]
		if event.Type != "resumed" {
			continue
		}
		state.Status, state.Successor = StatusResumed, event.Successor
		if event.TargetMachine != "" {
			raw, readErr := readPrivateRegularAt(aggregate, sourceReceiptName(event.TargetMachine), MaxEnvelopeBytes)
			if readErr != nil {
				return State{}, fmt.Errorf("load source remote receipt: %w", readErr)
			}
			receipt, decodeErr := DecodeReceipt(raw)
			if decodeErr != nil || receipt.ResumeID != event.RemoteResumeID || receipt.RequestDigest != event.RequestDigest {
				return State{}, fmt.Errorf("source remote receipt conflicts with resumed event")
			}
			state.RemoteReceipt = &receipt
		}
	}
	return state, nil
}

var sourceEventName = regexp.MustCompile(`^[0-9]{20}\.json$`)

func listSourceEventsAt(events *os.File, parkID string) ([]Event, error) {
	if _, err := events.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := events.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	history := make([]Event, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !sourceEventName.MatchString(entry.Name()) {
			return nil, fmt.Errorf("unexpected parked session event artifact %q", entry.Name())
		}
		raw, err := readPrivateRegularAt(events, entry.Name(), 64<<10)
		if err != nil {
			return nil, err
		}
		var event Event
		sequence := uint64(len(history) + 1)
		if err := strictDecode(raw, &event); err != nil || event.SchemaVersion != SchemaVersion || event.Sequence != sequence ||
			entry.Name() != fmt.Sprintf("%020d.json", sequence) || event.Type != "resumed" || event.At.IsZero() || event.Successor == nil ||
			event.Successor.WBSessionID == "" || event.Successor.PredecessorWBSessionID == "" {
			return nil, fmt.Errorf("invalid parked session event history for %s at %q", parkID, entry.Name())
		}
		remote := event.RemoteResumeID != "" || event.RequestDigest != "" || event.TargetMachine != ""
		if remote && (!sessionauthority.ValidID(event.RemoteResumeID) || !sessionauthority.ValidID(event.TargetMachine) ||
			!strings.HasPrefix(string(event.RequestDigest), sessionmove.DigestAlgorithmSHA256+":")) {
			return nil, fmt.Errorf("invalid remote parked session event history at %q", entry.Name())
		}
		history = append(history, event)
	}
	return history, nil
}

func openPrivateStoreRoot(root string, create bool) (*os.File, error) {
	clean, err := cleanAbsoluteStoreRoot(root)
	if err != nil {
		return nil, err
	}
	parentPath, name := filepath.Dir(clean), filepath.Base(clean)
	if create {
		if err := os.MkdirAll(parentPath, 0o700); err != nil {
			return nil, err
		}
	}
	parentFD, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(parentFD), "wb-park-store-parent")
	defer func() { _ = parent.Close() }()
	if create {
		if err := unix.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), name)
	if create {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = directory.Close()
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			_ = directory.Close()
			return nil, err
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
		_ = directory.Close()
		return nil, fmt.Errorf("park resume store root is not one 0700 directory")
	}
	return directory, nil
}

func openPrivateDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("private directory %q is not one 0700 directory", name)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readPrivateRegularAt(directory *os.File, name string, maximum int64) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	return readPrivateFile(file, maximum)
}

func readPrivateFile(file *os.File, maximum int64) ([]byte, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Nlink != 1 {
		return nil, fmt.Errorf("private artifact is not one 0600 regular file")
	}
	return readBoundedRegular(file, maximum)
}
