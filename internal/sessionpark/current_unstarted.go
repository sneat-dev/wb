package sessionpark

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

const unstartedLaunchAssertion = "operator verified a saved positive failure before StartDetached; empty directories alone do not prove process absence"

// UnstartedLaunchAttestation is an explicit registered operator assertion about
// historical preflight evidence. It is not a proof of live process absence.
// The original actor and state remain immutable across pickup/recovery retries.
type UnstartedLaunchAttestation struct {
	SchemaVersion int                          `json:"schema_version"`
	BundleDigest  sessionmove.Digest           `json:"bundle_digest"`
	PlanDigest    sessionmove.Digest           `json:"plan_digest"`
	RouteDigest   sessionmove.Digest           `json:"route_digest"`
	AttemptID     string                       `json:"attempt_id"`
	EmptyLayout   string                       `json:"empty_layout"`
	ActorDigest   sessionmove.Digest           `json:"actor_digest"`
	Assertion     string                       `json:"assertion"`
	Directories   []unstartedDirectoryIdentity `json:"directories"`
}

type unstartedDirectoryIdentity struct {
	Name   string `json:"name"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func unstartedDirectory(file *os.File, name string) (unstartedDirectoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return unstartedDirectoryIdentity{}, err
	}
	if stat.Ino == 0 {
		return unstartedDirectoryIdentity{}, fmt.Errorf("legacy attempt directory identity is unavailable")
	}
	return unstartedDirectoryIdentity{Name: name, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

func unstartedActorDigest(caller session.Record) sessionmove.Digest {
	raw, _ := json.Marshal(caller)
	return sessionmove.DigestBytes(raw)
}

func (a UnstartedLaunchAttestation) Digest() sessionmove.Digest {
	raw, _ := json.Marshal(a)
	return sessionmove.DigestBytes(raw)
}

type legacyUnstartedPlan struct {
	SchemaVersion      int                `json:"schema_version"`
	HandoffID          string             `json:"handoff_id"`
	RequestDigest      sessionmove.Digest `json:"request_digest"`
	Successor          string             `json:"successor_wb_session_id"`
	Predecessor        string             `json:"predecessor_wb_session_id"`
	Machine            string             `json:"machine"`
	TmuxName           string             `json:"tmux_name"`
	Transport          string             `json:"transport,omitempty"`
	Runtime            string             `json:"runtime"`
	Model              string             `json:"model,omitempty"`
	StoreRoot          string             `json:"store_root"`
	WorktreeDir        string             `json:"worktree_dir"`
	PinnedCommit       string             `json:"pinned_commit"`
	PinnedBranch       string             `json:"pinned_branch"`
	RootMode           string             `json:"root_mode,omitempty"`
	HandoverPath       string             `json:"handover_path"`
	AuthorityFile      string             `json:"authority_file"`
	ContinuationKind   string             `json:"continuation_kind"`
	ContinuationDigest sessionmove.Digest `json:"continuation_digest"`
	WBExecutable       string             `json:"wb_executable"`
	HarnessExecutable  string             `json:"harness_executable"`
	HarnessArgs        []string           `json:"harness_args"`
}

// inspectUnstartedLauncherUnderLock retains the original files and only reads
// their fixed private children. A nested empty attempt requires an explicit
// historical assertion; it never becomes automatic absence evidence.
func (s Store) inspectUnstartedLauncherUnderLock(lock *SourceLock) (*UnstartedLaunchAttestation, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return nil, fmt.Errorf("inspect unstarted launcher requires retained source authority")
	}
	launch, err := openPrivateDirectoryAt(lock.aggregate, "launch")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = launch.Close() }()
	entries, err := launch.ReadDir(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	if len(entries) > 2 {
		return nil, fmt.Errorf("current pickup refuses a launcher with attempt or execution evidence")
	}
	for _, entry := range entries {
		if entry.Name() != "plan.json" && entry.Name() != "attempts" {
			return nil, fmt.Errorf("current pickup refuses a launcher with attempt or execution evidence")
		}
	}
	raw, err := readPrivateRegularAt(launch, "plan.json", 256<<10)
	if err != nil {
		return nil, err
	}
	var plan legacyUnstartedPlan
	if json.Unmarshal(raw, &plan) != nil || plan.HandoffID != lock.parkID || plan.Predecessor != lock.bundle.Source.WBSessionID {
		return nil, fmt.Errorf("retained never-started launcher plan has a different source identity")
	}
	bundleRaw, err := EncodeBundle(lock.bundle)
	if err != nil || plan.RequestDigest != sessionmove.DigestBytes(bundleRaw) || plan.Successor != localSuccessorID(lock.parkID) {
		return nil, fmt.Errorf("retained never-started launcher plan has a different bundle identity")
	}
	if len(entries) == 1 && entries[0].Name() == "plan.json" {
		return nil, nil
	}
	if err := strictDecode(raw, &plan); err != nil {
		return nil, err
	}
	if err := s.validateLegacyUnstartedPlan(lock, plan); err != nil {
		return nil, err
	}
	attempts, err := openPrivateDirectoryAt(launch, "attempts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = attempts.Close() }()
	refs, err := attempts.ReadDir(2)
	if err != nil && !errors.Is(err, io.EOF) || len(refs) != 1 || !validUnstartedAttemptID(refs[0].Name()) {
		return nil, fmt.Errorf("unstarted launch recovery requires exactly one standard first attempt")
	}
	attempt, err := openPrivateDirectoryAt(attempts, refs[0].Name())
	if err != nil {
		return nil, err
	}
	defer func() { _ = attempt.Close() }()
	children, err := attempt.ReadDir(3)
	if err != nil && !errors.Is(err, io.EOF) || len(children) != 2 {
		return nil, fmt.Errorf("unstarted launch recovery refuses attempt execution or unknown evidence")
	}
	directories := make([]unstartedDirectoryIdentity, 0, 5)
	for _, item := range []struct {
		file *os.File
		name string
	}{{launch, "launch"}, {attempts, "attempts"}, {attempt, "attempt"}} {
		identity, err := unstartedDirectory(item.file, item.name)
		if err != nil {
			return nil, err
		}
		directories = append(directories, identity)
	}
	seen := map[string]bool{}
	for _, entry := range children {
		if (entry.Name() != "ready" && entry.Name() != "exec") || seen[entry.Name()] {
			return nil, fmt.Errorf("unstarted launch recovery refuses attempt execution or unknown evidence")
		}
		seen[entry.Name()] = true
		child, err := openPrivateDirectoryAt(attempt, entry.Name())
		if err != nil {
			return nil, err
		}
		identity, statErr := unstartedDirectory(child, entry.Name())
		if statErr != nil {
			_ = child.Close()
			return nil, statErr
		}
		directories = append(directories, identity)
		contents, readErr := child.ReadDir(1)
		_ = child.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if len(contents) != 0 {
			return nil, fmt.Errorf("unstarted launch recovery refuses ready, fence or execution evidence")
		}
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].Name < directories[j].Name })
	routeRaw, err := readPrivateRegularAt(lock.aggregate, sourceResumeRouteFileName, 16<<10)
	if err != nil {
		return nil, err
	}
	if _, err := s.validateResumeRouteUnderLock(lock, ResumeRouteLocal, ""); err != nil {
		return nil, err
	}
	return &UnstartedLaunchAttestation{SchemaVersion: 1, BundleDigest: sessionmove.DigestBytes(bundleRaw), PlanDigest: sessionmove.DigestBytes(raw), RouteDigest: sessionmove.DigestBytes(routeRaw), AttemptID: refs[0].Name(), EmptyLayout: "legacy-v1/sole-first-attempt/ready-empty/exec-empty", Assertion: unstartedLaunchAssertion, Directories: directories}, nil
}

func validUnstartedAttemptID(id string) bool {
	if !strings.HasPrefix(id, "000001-") || len(id) != 39 {
		return false
	}
	for _, r := range id[7:] {
		if r < '0' || r > '9' {
			if r < 'a' || r > 'f' {
				return false
			}
		}
	}
	return true
}

func (s Store) validateLegacyUnstartedPlan(lock *SourceLock, p legacyUnstartedPlan) error {
	path, continuation, found, err := s.LoadLocalSuccessorContextUnderLock(lock)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("legacy unstarted launch has no retained successor context")
	}
	bundleRaw, _ := EncodeBundle(lock.bundle)
	authority, err := LocalLaunchAuthority(lock.bundle, sessionmove.DigestBytes(bundleRaw), path, continuation)
	if err != nil {
		return err
	}
	worktree := filepath.Join(s.Root, lock.parkID, LocalNeutralDirName)
	if len(lock.bundle.Worktrees) != 0 {
		worktree = lock.bundle.Worktrees[0].WorktreeDir
	}
	if p.SchemaVersion != 1 || p.Transport != "" || p.Machine != authority.TargetMachine || p.TmuxName != "wb-session-"+authority.SuccessorWBSessionID || p.Runtime != authority.SourceRuntime || p.Model != authority.SourceModel || p.StoreRoot != s.Root || p.WorktreeDir != worktree || p.PinnedCommit != authority.PinnedCommit || p.PinnedBranch != authority.PinnedBranch || p.RootMode != string(authority.RootMode) || p.HandoverPath != path || p.AuthorityFile != BundleFileName || p.ContinuationKind != string(authority.ContinuationKind) || p.ContinuationDigest != sessionmove.DigestBytes(continuation) || !filepath.IsAbs(p.WBExecutable) || filepath.Clean(p.WBExecutable) != p.WBExecutable || !filepath.IsAbs(p.HarnessExecutable) || filepath.Clean(p.HarnessExecutable) != p.HarnessExecutable {
		return fmt.Errorf("legacy unstarted launch plan contradicts exact parked authority")
	}
	prompt := fmt.Sprintf("Continue WB parked session %s as session %s from predecessor %s. Read the file named by WB_SESSION_CONTINUATION_FILE before acting.", authority.AggregateID, authority.SuccessorWBSessionID, authority.PredecessorWBSessionID)
	var args []string
	executable := ""
	switch p.Runtime {
	case "codex":
		executable, args = "codex", []string{"-C", worktree}
		if p.Model != "" {
			args = append(args, "-m", p.Model)
		}
	case "claude-code":
		executable = "claude"
		if p.Model != "" {
			args = append(args, "--model", p.Model)
		}
		args = append(args, "--name", authority.SuccessorWBSessionID)
	default:
		return fmt.Errorf("legacy unstarted launch uses unsupported harness")
	}
	args = append(args, prompt)
	if filepath.Base(p.HarnessExecutable) != executable || !reflect.DeepEqual(args, p.HarnessArgs) {
		return fmt.Errorf("legacy unstarted launch plan contradicts fixed harness argv")
	}
	return nil
}

func (s Store) validateUnstartedAttestationUnderLock(lock *SourceLock, caller session.Record, attestation *UnstartedLaunchAttestation) error {
	state, err := s.inspectUnstartedLauncherUnderLock(lock)
	if err != nil {
		return err
	}
	if state == nil || attestation == nil {
		return fmt.Errorf("current launch attestation has no exact retained legacy state")
	}
	state.ActorDigest = unstartedActorDigest(caller)
	if !reflect.DeepEqual(*state, *attestation) {
		return fmt.Errorf("unstarted launch attestation conflicts with the exact retained state or original actor")
	}
	return nil
}

func (s Store) unstartedAttestationForCallerUnderLock(lock *SourceLock, caller session.Record, acknowledgment sessionmove.Digest) (*UnstartedLaunchAttestation, error) {
	state, err := s.inspectUnstartedLauncherUnderLock(lock)
	if err != nil {
		return nil, err
	}
	if state == nil {
		if acknowledgment != "" {
			return nil, fmt.Errorf("--ack-unstarted-launch applies only to a retained legacy empty first attempt")
		}
		return nil, nil
	}
	initial, found, err := s.currentPreparationFileUnderLock(lock, currentPreparationName)
	if err != nil {
		return nil, err
	}
	if found {
		if initial.UnstartedLaunch == nil {
			return nil, fmt.Errorf("existing current preparation has no unstarted launch attestation")
		}
		if acknowledgment != "" && acknowledgment != initial.UnstartedLaunch.Digest() {
			return nil, fmt.Errorf("unstarted launch acknowledgment differs from the immutable original assertion")
		}
		return initial.UnstartedLaunch, nil
	}
	state.ActorDigest = unstartedActorDigest(caller)
	if acknowledgment != state.Digest() {
		return nil, fmt.Errorf("legacy empty launcher attempt requires externally verified positive pre-start failure evidence; then acknowledge its exact state with --ack-unstarted-launch %s (empty directories alone do not prove absence)", state.Digest())
	}
	return state, nil
}

func (s Store) requireUnstartedLauncherUnderLock(lock *SourceLock) error {
	state, err := s.inspectUnstartedLauncherUnderLock(lock)
	if err != nil || state == nil {
		return err
	}
	initial, found, err := s.currentPreparationFileUnderLock(lock, currentPreparationName)
	if err != nil {
		return err
	}
	if !found || initial.UnstartedLaunch == nil {
		return fmt.Errorf("legacy empty launcher attempt requires --ack-unstarted-launch through current pickup")
	}
	return nil
}
