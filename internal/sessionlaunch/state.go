package sessionlaunch

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"golang.org/x/sys/unix"
)

const launchSchemaVersion = 1
const maxLaunchArtifactBytes = 1 << 20

const (
	launchDirectoryName   = "launch"
	attemptsDirectoryName = "attempts"
	readyDirectoryName    = "ready"
	execDirectoryName     = "exec"
)

type launchPlan struct {
	SchemaVersion          int                `json:"schema_version"`
	HandoffID              string             `json:"handoff_id"`
	RequestDigest          sessionmove.Digest `json:"request_digest"`
	SuccessorWBSessionID   string             `json:"successor_wb_session_id"`
	PredecessorWBSessionID string             `json:"predecessor_wb_session_id"`
	Machine                string             `json:"machine"`
	TmuxName               string             `json:"tmux_name"`
	Runtime                string             `json:"runtime"`
	Model                  string             `json:"model,omitempty"`
	StoreRoot              string             `json:"store_root"`
	WorktreeDir            string             `json:"worktree_dir"`
	PinnedCommit           string             `json:"pinned_commit"`
	PinnedBranch           string             `json:"pinned_branch"`
	HandoverPath           string             `json:"handover_path"`
	AuthorityFile          string             `json:"authority_file"`
	ContinuationKind       string             `json:"continuation_kind"`
	ContinuationDigest     sessionmove.Digest `json:"continuation_digest"`
	WBExecutable           string             `json:"wb_executable"`
	HarnessExecutable      string             `json:"harness_executable"`
	HarnessArgs            []string           `json:"harness_args"`
}

type launcherReady struct {
	SchemaVersion int                `json:"schema_version"`
	HandoffID     string             `json:"handoff_id"`
	AttemptID     string             `json:"attempt_id"`
	AttemptIndex  uint64             `json:"attempt_index"`
	RequestDigest sessionmove.Digest `json:"request_digest"`
	PlanDigest    sessionmove.Digest `json:"plan_digest"`
	PID           int                `json:"pid"`
	Session       session.Record     `json:"session"`
}

type launcherRelease struct {
	SchemaVersion    int                `json:"schema_version"`
	HandoffID        string             `json:"handoff_id"`
	AttemptID        string             `json:"attempt_id"`
	AttemptIndex     uint64             `json:"attempt_index"`
	RequestDigest    sessionmove.Digest `json:"request_digest"`
	PlanDigest       sessionmove.Digest `json:"plan_digest"`
	ReadyDigest      sessionmove.Digest `json:"ready_digest"`
	PID              int                `json:"pid"`
	TargetWorkLogRef string             `json:"target_work_log_ref,omitempty"`
	ReleasedAt       time.Time          `json:"released_at"`
}

type launcherFailure struct {
	SchemaVersion int                `json:"schema_version"`
	HandoffID     string             `json:"handoff_id"`
	AttemptID     string             `json:"attempt_id"`
	AttemptIndex  uint64             `json:"attempt_index"`
	RequestDigest sessionmove.Digest `json:"request_digest"`
	PlanDigest    sessionmove.Digest `json:"plan_digest"`
	ReadyDigest   sessionmove.Digest `json:"ready_digest"`
	ReleaseDigest sessionmove.Digest `json:"release_digest"`
	PID           int                `json:"pid"`
	Diagnostic    string             `json:"diagnostic"`
	FailedAt      time.Time          `json:"failed_at"`
}

// launcherAbandonment is receiver-authored terminal evidence for the narrow
// pre-release crash case where a wrapper left exact process artifacts but
// cannot publish its own failure. It is valid only after the receiver, while
// holding the handoff execution lock, proves the fence released, PID ESRCH,
// exact tmux absent, and no release/started marker.
type launcherAbandonment struct {
	SchemaVersion int                `json:"schema_version"`
	HandoffID     string             `json:"handoff_id"`
	AttemptID     string             `json:"attempt_id"`
	AttemptIndex  uint64             `json:"attempt_index"`
	RequestDigest sessionmove.Digest `json:"request_digest"`
	PlanDigest    sessionmove.Digest `json:"plan_digest"`
	ReadyDigest   sessionmove.Digest `json:"ready_digest,omitempty"`
	PID           int                `json:"pid"`
	AbandonedAt   time.Time          `json:"abandoned_at"`
}

type launcherStarted struct {
	SchemaVersion int                `json:"schema_version"`
	HandoffID     string             `json:"handoff_id"`
	AttemptID     string             `json:"attempt_id"`
	AttemptIndex  uint64             `json:"attempt_index"`
	RequestDigest sessionmove.Digest `json:"request_digest"`
	PlanDigest    sessionmove.Digest `json:"plan_digest"`
	ReleaseDigest sessionmove.Digest `json:"release_digest"`
	PID           int                `json:"pid"`
	StartedAt     time.Time          `json:"started_at"`
}

// launchState retains one admitted handoff directory and its fixed private
// launch subdirectories for the whole authorization transaction. Every read,
// lock check, and publication is descriptor-relative to these same inodes.
type launchState struct {
	handoffID string
	handoff   *os.File
	launch    *os.File
	attempts  *os.File
}

type launchAttempt struct {
	state *launchState
	id    string
	index uint64
	root  *os.File
	ready *os.File
	exec  *os.File
}

func (state *launchState) savePlan(plan launchPlan) (launchPlan, sessionmove.Digest, bool, error) {
	raw, err := encodeLaunchJSON(plan)
	if err != nil {
		return launchPlan{}, "", false, err
	}
	created, err := state.publish("", "plan.json", raw)
	if err != nil {
		return launchPlan{}, "", false, fmt.Errorf("persist immutable launch plan: %w", err)
	}
	if !created {
		existing, readErr := state.read("", "plan.json")
		if readErr != nil {
			return launchPlan{}, "", false, readErr
		}
		if !bytes.Equal(existing, raw) {
			return launchPlan{}, "", false, fmt.Errorf("handoff %s has a conflicting immutable launch plan", plan.HandoffID)
		}
	}
	return plan, sessionmove.DigestBytes(raw), !created, nil
}

func (state *launchState) loadPlan() (launchPlan, sessionmove.Digest, error) {
	raw, err := state.read("", "plan.json")
	if err != nil {
		return launchPlan{}, "", err
	}
	var plan launchPlan
	if err := decodeLaunchJSON(raw, &plan); err != nil {
		return launchPlan{}, "", err
	}
	if plan.SchemaVersion != launchSchemaVersion || plan.HandoffID != state.handoffID {
		return launchPlan{}, "", fmt.Errorf("invalid immutable launch plan for %s", state.handoffID)
	}
	return plan, sessionmove.DigestBytes(raw), nil
}

func (attempt *launchAttempt) saveReady(plan launchPlan, expectedPlanDigest sessionmove.Digest, record session.Record) (launcherReady, error) {
	current, planDigest, err := attempt.state.loadPlan()
	if err != nil {
		return launcherReady{}, err
	}
	if planDigest != expectedPlanDigest || !equalLaunchPlan(current, plan) {
		return launcherReady{}, fmt.Errorf("immutable launch plan changed before launcher readiness")
	}
	ready := launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index,
		RequestDigest: plan.RequestDigest, PlanDigest: planDigest, PID: record.PID, Session: record}
	raw, err := encodeLaunchJSON(ready)
	if err != nil {
		return launcherReady{}, err
	}
	name := strconv.Itoa(record.PID) + ".json"
	created, err := attempt.publish(readyDirectoryName, name, raw)
	if err != nil {
		return launcherReady{}, err
	}
	if !created {
		existing, readErr := attempt.read(readyDirectoryName, name)
		if readErr != nil {
			return launcherReady{}, readErr
		}
		if !bytes.Equal(existing, raw) {
			return launcherReady{}, fmt.Errorf("launcher ready PID %d conflicts with immutable state", record.PID)
		}
	}
	return ready, nil
}

func (attempt *launchAttempt) loadReady(pid int) (launcherReady, sessionmove.Digest, error) {
	raw, err := attempt.read(readyDirectoryName, strconv.Itoa(pid)+".json")
	if err != nil {
		return launcherReady{}, "", err
	}
	var ready launcherReady
	if err := decodeLaunchJSON(raw, &ready); err != nil {
		return launcherReady{}, "", err
	}
	if ready.SchemaVersion != launchSchemaVersion || ready.HandoffID != attempt.state.handoffID ||
		ready.AttemptID != attempt.id || ready.AttemptIndex != attempt.index || ready.PID != pid {
		return launcherReady{}, "", fmt.Errorf("invalid launcher ready artifact for PID %d", pid)
	}
	return ready, sessionmove.DigestBytes(raw), nil
}

func (attempt *launchAttempt) saveRelease(plan launchPlan, expectedPlanDigest sessionmove.Digest, ready launcherReady, targetWorkLogRef string, now time.Time) (launcherRelease, bool, error) {
	if _, err := attempt.loadAbandonment(); err == nil {
		return launcherRelease{}, false, fmt.Errorf("abandoned launcher attempt cannot be released")
	} else if !errors.Is(err, os.ErrNotExist) {
		return launcherRelease{}, false, err
	}
	current, planDigest, err := attempt.state.loadPlan()
	if err != nil {
		return launcherRelease{}, false, err
	}
	if planDigest != expectedPlanDigest || !equalLaunchPlan(current, plan) || ready.PlanDigest != expectedPlanDigest {
		return launcherRelease{}, false, fmt.Errorf("immutable launch plan changed before launcher release")
	}
	loadedReady, readyDigest, err := attempt.loadReady(ready.PID)
	if err != nil {
		return launcherRelease{}, false, err
	}
	if !equalReady(loadedReady, ready) {
		return launcherRelease{}, false, fmt.Errorf("immutable launcher ready artifact changed before release")
	}
	held, err := attempt.execFenceHeld(ready.PID)
	if err != nil {
		return launcherRelease{}, false, err
	}
	if !held {
		return launcherRelease{}, false, fmt.Errorf("launcher PID %d did not hold its exec-success fence before release", ready.PID)
	}
	release := launcherRelease{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index,
		RequestDigest: plan.RequestDigest, PlanDigest: planDigest, ReadyDigest: readyDigest, PID: ready.PID,
		TargetWorkLogRef: strings.TrimSpace(targetWorkLogRef), ReleasedAt: now.UTC()}
	raw, err := encodeLaunchJSON(release)
	if err != nil {
		return launcherRelease{}, false, err
	}
	created, err := attempt.publish("", "release.json", raw)
	if err != nil {
		return launcherRelease{}, false, err
	}
	if created {
		return release, false, nil
	}
	existing, _, err := attempt.loadRelease()
	if err != nil {
		return launcherRelease{}, false, err
	}
	if existing.RequestDigest != release.RequestDigest || existing.PlanDigest != release.PlanDigest ||
		existing.ReadyDigest != release.ReadyDigest || existing.PID != release.PID || existing.TargetWorkLogRef != release.TargetWorkLogRef {
		return launcherRelease{}, false, fmt.Errorf("handoff %s has a conflicting immutable launcher release", plan.HandoffID)
	}
	return existing, true, nil
}

func (attempt *launchAttempt) loadRelease() (launcherRelease, sessionmove.Digest, error) {
	raw, err := attempt.read("", "release.json")
	if err != nil {
		return launcherRelease{}, "", err
	}
	var release launcherRelease
	if err := decodeLaunchJSON(raw, &release); err != nil {
		return launcherRelease{}, "", err
	}
	if release.SchemaVersion != launchSchemaVersion || release.HandoffID != attempt.state.handoffID ||
		release.AttemptID != attempt.id || release.AttemptIndex != attempt.index || release.PID <= 0 || release.ReleasedAt.IsZero() {
		return launcherRelease{}, "", fmt.Errorf("invalid immutable launcher release for %s attempt %s", attempt.state.handoffID, attempt.id)
	}
	return release, sessionmove.DigestBytes(raw), nil
}

func (attempt *launchAttempt) saveExecFailure(plan launchPlan, planDigest, readyDigest, releaseDigest sessionmove.Digest, pid int, failure error, now time.Time) error {
	diagnostic := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, failure.Error())
	if len(diagnostic) > 1024 {
		diagnostic = diagnostic[:1024]
	}
	record := launcherFailure{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index,
		RequestDigest: plan.RequestDigest, PlanDigest: planDigest, ReadyDigest: readyDigest, ReleaseDigest: releaseDigest,
		PID: pid, Diagnostic: diagnostic, FailedAt: now.UTC()}
	raw, err := encodeLaunchJSON(record)
	if err != nil {
		return err
	}
	name := strconv.Itoa(pid) + ".failure.json"
	created, err := attempt.publish(execDirectoryName, name, raw)
	if err != nil {
		return err
	}
	if created {
		return nil
	}
	existing, found, err := attempt.loadExecFailure(pid)
	if err != nil {
		return err
	}
	if !found || existing.RequestDigest != record.RequestDigest || existing.PlanDigest != record.PlanDigest ||
		existing.ReadyDigest != record.ReadyDigest || existing.ReleaseDigest != record.ReleaseDigest || existing.PID != record.PID {
		return fmt.Errorf("launcher PID %d has conflicting immutable exec-failure evidence", pid)
	}
	return nil
}

func (attempt *launchAttempt) loadExecFailure(pid int) (launcherFailure, bool, error) {
	raw, err := attempt.read(execDirectoryName, strconv.Itoa(pid)+".failure.json")
	if errors.Is(err, os.ErrNotExist) {
		return launcherFailure{}, false, nil
	}
	if err != nil {
		return launcherFailure{}, false, err
	}
	var failure launcherFailure
	if err := decodeLaunchJSON(raw, &failure); err != nil {
		return launcherFailure{}, false, err
	}
	if failure.SchemaVersion != launchSchemaVersion || failure.HandoffID != attempt.state.handoffID ||
		failure.AttemptID != attempt.id || failure.AttemptIndex != attempt.index || failure.PID != pid ||
		failure.Diagnostic == "" || failure.FailedAt.IsZero() {
		return launcherFailure{}, false, fmt.Errorf("invalid immutable launcher failure for PID %d", pid)
	}
	return failure, true, nil
}

func (attempt *launchAttempt) saveAbandonment(plan launchPlan, expectedPlanDigest sessionmove.Digest, pid int, now time.Time) (launcherAbandonment, bool, error) {
	current, planDigest, err := attempt.state.loadPlan()
	if err != nil {
		return launcherAbandonment{}, false, err
	}
	if planDigest != expectedPlanDigest || !equalLaunchPlan(current, plan) {
		return launcherAbandonment{}, false, fmt.Errorf("immutable launch plan changed before attempt abandonment")
	}
	if _, _, err := attempt.loadRelease(); err == nil {
		return launcherAbandonment{}, false, fmt.Errorf("released launcher attempt cannot be abandoned")
	} else if !errors.Is(err, os.ErrNotExist) {
		return launcherAbandonment{}, false, err
	}
	if _, err := attempt.state.loadStarted(); err == nil {
		return launcherAbandonment{}, false, fmt.Errorf("started launcher attempt cannot be abandoned")
	} else if !errors.Is(err, os.ErrNotExist) {
		return launcherAbandonment{}, false, err
	}
	evidencePID, found, err := attempt.preReleaseProcessEvidence()
	if err != nil {
		return launcherAbandonment{}, false, err
	}
	if !found || evidencePID != pid {
		return launcherAbandonment{}, false, fmt.Errorf("attempt abandonment does not bind one exact process-evidence PID")
	}
	held, err := attempt.execFenceHeld(pid)
	if err != nil {
		return launcherAbandonment{}, false, err
	}
	if held {
		return launcherAbandonment{}, false, fmt.Errorf("live launcher fence cannot be abandoned")
	}
	readyDigest := sessionmove.Digest("")
	if ready, digest, readyErr := attempt.loadReady(pid); readyErr == nil {
		if ready.RequestDigest != plan.RequestDigest || ready.PlanDigest != planDigest {
			return launcherAbandonment{}, false, fmt.Errorf("pre-release ready artifact conflicts with abandoned plan")
		}
		readyDigest = digest
	} else if !errors.Is(readyErr, os.ErrNotExist) {
		return launcherAbandonment{}, false, readyErr
	}
	abandonment := launcherAbandonment{
		SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest,
		PlanDigest: planDigest, ReadyDigest: readyDigest, PID: pid, AbandonedAt: now.UTC(),
	}
	raw, err := encodeLaunchJSON(abandonment)
	if err != nil {
		return launcherAbandonment{}, false, err
	}
	created, err := attempt.publish("", "abandoned.json", raw)
	if err != nil {
		return launcherAbandonment{}, false, err
	}
	if created {
		return abandonment, false, nil
	}
	existing, err := attempt.loadAbandonment()
	if err != nil {
		return launcherAbandonment{}, false, err
	}
	if existing.HandoffID != abandonment.HandoffID || existing.AttemptID != abandonment.AttemptID ||
		existing.AttemptIndex != abandonment.AttemptIndex || existing.RequestDigest != abandonment.RequestDigest ||
		existing.PlanDigest != abandonment.PlanDigest || existing.ReadyDigest != abandonment.ReadyDigest || existing.PID != abandonment.PID {
		return launcherAbandonment{}, false, fmt.Errorf("launcher attempt %s has conflicting immutable abandonment evidence", attempt.id)
	}
	return existing, true, nil
}

func (attempt *launchAttempt) loadAbandonment() (launcherAbandonment, error) {
	raw, err := attempt.read("", "abandoned.json")
	if err != nil {
		return launcherAbandonment{}, err
	}
	var abandonment launcherAbandonment
	if err := decodeLaunchJSON(raw, &abandonment); err != nil {
		return launcherAbandonment{}, err
	}
	if abandonment.SchemaVersion != launchSchemaVersion || abandonment.HandoffID != attempt.state.handoffID ||
		abandonment.AttemptID != attempt.id || abandonment.AttemptIndex != attempt.index ||
		abandonment.RequestDigest == "" || abandonment.PlanDigest == "" || abandonment.PID <= 0 || abandonment.AbandonedAt.IsZero() {
		return launcherAbandonment{}, fmt.Errorf("invalid immutable launcher abandonment for %s attempt %s", attempt.state.handoffID, attempt.id)
	}
	return abandonment, nil
}

func (state *launchState) saveStarted(attempt *launchAttempt, plan launchPlan, planDigest, releaseDigest sessionmove.Digest, release launcherRelease, now time.Time) (launcherStarted, bool, error) {
	started := launcherStarted{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest,
		PlanDigest: planDigest, ReleaseDigest: releaseDigest, PID: release.PID, StartedAt: now.UTC()}
	raw, err := encodeLaunchJSON(started)
	if err != nil {
		return launcherStarted{}, false, err
	}
	created, err := state.publish("", "started.json", raw)
	if err != nil {
		return launcherStarted{}, false, err
	}
	if created {
		return started, false, nil
	}
	existing, err := state.loadStarted()
	if err != nil {
		return launcherStarted{}, false, err
	}
	if existing.HandoffID != started.HandoffID || existing.AttemptID != started.AttemptID ||
		existing.AttemptIndex != started.AttemptIndex || existing.RequestDigest != started.RequestDigest ||
		existing.PlanDigest != started.PlanDigest || existing.ReleaseDigest != started.ReleaseDigest || existing.PID != started.PID {
		return launcherStarted{}, false, fmt.Errorf("handoff %s already selected a different immutable started attempt", plan.HandoffID)
	}
	return existing, true, nil
}

func (state *launchState) loadStarted() (launcherStarted, error) {
	raw, err := state.read("", "started.json")
	if err != nil {
		return launcherStarted{}, err
	}
	var started launcherStarted
	if err := decodeLaunchJSON(raw, &started); err != nil {
		return launcherStarted{}, err
	}
	index, idErr := parseAttemptID(started.AttemptID)
	if started.SchemaVersion != launchSchemaVersion || started.HandoffID != state.handoffID ||
		idErr != nil || index != started.AttemptIndex || started.PID <= 0 || started.StartedAt.IsZero() {
		return launcherStarted{}, fmt.Errorf("invalid immutable started marker for %s", state.handoffID)
	}
	return started, nil
}

func encodeLaunchJSON(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func decodeLaunchJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("launch artifact contains trailing JSON")
		}
		return err
	}
	return nil
}

func openLaunchState(root, handoffID string, create bool) (*launchState, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("launch store root must be a clean absolute path")
	}
	if handoffID == "" || handoffID == "." || handoffID == ".." || filepath.Base(handoffID) != handoffID {
		return nil, fmt.Errorf("invalid launch handoff ID %q", handoffID)
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open launch store root: %w", err)
	}
	handoffFD, err := unix.Openat(rootFD, handoffID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(rootFD)
	if err != nil {
		return nil, fmt.Errorf("open launch handoff directory: %w", err)
	}
	handoff, err := fileForFD(handoffFD, "wb-session-launch-handoff")
	if err != nil {
		return nil, err
	}
	return openLaunchStateFromHandoff(handoffID, handoff, create)
}

func openLaunchStateFromHandoff(handoffID string, handoff *os.File, create bool) (*launchState, error) {
	if handoff == nil {
		return nil, fmt.Errorf("retained launch handoff directory is required")
	}
	state := &launchState{handoffID: handoffID, handoff: handoff}
	fail := func(err error) (*launchState, error) {
		_ = state.Close()
		return nil, err
	}
	launchFD, err := openPrivateDirectoryAt(int(handoff.Fd()), launchDirectoryName, create)
	if err != nil {
		return fail(err)
	}
	state.launch, err = fileForFD(launchFD, "wb-session-launch-directory")
	if err != nil {
		return fail(err)
	}
	attemptsFD, childErr := openPrivateDirectoryAt(int(state.launch.Fd()), attemptsDirectoryName, create)
	if childErr != nil {
		if !create && errors.Is(childErr, os.ErrNotExist) {
			return state, nil
		}
		return fail(childErr)
	}
	state.attempts, childErr = fileForFD(attemptsFD, "wb-session-launch-attempts")
	if childErr != nil {
		return fail(childErr)
	}
	return state, nil
}

func (state *launchState) Close() error {
	if state == nil {
		return nil
	}
	var errs []error
	for _, file := range []*os.File{state.attempts, state.launch, state.handoff} {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	state.attempts, state.launch, state.handoff = nil, nil, nil
	return errors.Join(errs...)

}

func (state *launchState) directory(child string) (*os.File, error) {
	switch child {
	case "":
		if state.launch != nil {
			return state.launch, nil
		}
	case attemptsDirectoryName:
		if state.attempts != nil {
			return state.attempts, nil
		}
	default:
		return nil, fmt.Errorf("invalid launch artifact directory %q", child)
	}
	return nil, fmt.Errorf("launch artifact directory %q is unavailable: %w", child, os.ErrNotExist)
}

func (state *launchState) openAttempt(attemptID string) (*launchAttempt, error) {
	index, err := parseAttemptID(attemptID)
	if err != nil {
		return nil, err
	}
	if state.attempts == nil {
		return nil, fmt.Errorf("launch attempts directory is unavailable: %w", os.ErrNotExist)
	}
	rootFD, err := openPrivateDirectoryAt(int(state.attempts.Fd()), attemptID, false)
	if err != nil {
		return nil, err
	}
	attempt := &launchAttempt{state: state, id: attemptID, index: index}
	attempt.root, err = fileForFD(rootFD, "wb-session-launch-attempt")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*launchAttempt, error) {
		_ = attempt.Close()
		return nil, err
	}
	readyFD, err := openPrivateDirectoryAt(int(attempt.root.Fd()), readyDirectoryName, false)
	if err != nil {
		return fail(err)
	}
	attempt.ready, err = fileForFD(readyFD, "wb-session-launch-attempt-ready")
	if err != nil {
		return fail(err)
	}
	execFD, err := openPrivateDirectoryAt(int(attempt.root.Fd()), execDirectoryName, false)
	if err != nil {
		return fail(err)
	}
	attempt.exec, err = fileForFD(execFD, "wb-session-launch-attempt-exec")
	if err != nil {
		return fail(err)
	}
	return attempt, nil
}

// openOrRecoverClaimedAttempt completes only the fixed empty children of the
// latest already-claimed attempt. Any artifact or unknown entry makes the
// crash window ambiguous and therefore non-recoverable.
func (state *launchState) openOrRecoverClaimedAttempt(attemptID string) (*launchAttempt, error) {
	if _, err := parseAttemptID(attemptID); err != nil {
		return nil, err
	}
	rootFD, err := openPrivateDirectoryAt(int(state.attempts.Fd()), attemptID, false)
	if err != nil {
		return nil, err
	}
	root, err := fileForFD(rootFD, "wb-session-launch-attempt-recovery")
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := root.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() != readyDirectoryName && entry.Name() != execDirectoryName {
			return nil, fmt.Errorf("claimed attempt %s has ambiguous artifact %q", attemptID, entry.Name())
		}
		childFD, err := openPrivateDirectoryAt(int(root.Fd()), entry.Name(), false)
		if err != nil {
			return nil, err
		}
		child, err := fileForFD(childFD, "wb-session-launch-attempt-recovery-child")
		if err != nil {
			return nil, err
		}
		childEntries, readErr := child.ReadDir(-1)
		_ = child.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(childEntries) != 0 {
			return nil, fmt.Errorf("claimed attempt %s has ambiguous state in %s", attemptID, entry.Name())
		}
	}
	for _, child := range []string{readyDirectoryName, execDirectoryName} {
		childFD, err := openPrivateDirectoryAt(int(root.Fd()), child, true)
		if err != nil {
			return nil, err
		}
		_ = unix.Close(childFD)
	}
	if err := root.Sync(); err != nil {
		return nil, err
	}
	return state.openAttempt(attemptID)
}

func (state *launchState) createAttempt() (*launchAttempt, error) {
	refs, err := state.listAttempts()
	if err != nil {
		return nil, err
	}
	index := uint64(len(refs) + 1)
	for tries := 0; tries < 100; tries++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		attemptID := fmt.Sprintf("%06d-%s", index, hex.EncodeToString(random[:]))
		if err := unix.Mkdirat(int(state.attempts.Fd()), attemptID, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return nil, fmt.Errorf("claim launcher attempt: %w", err)
		}
		rootFD, err := openPrivateDirectoryAt(int(state.attempts.Fd()), attemptID, false)
		if err != nil {
			return nil, err
		}
		root, err := fileForFD(rootFD, "wb-session-launch-attempt")
		if err != nil {
			return nil, err
		}
		for _, child := range []string{readyDirectoryName, execDirectoryName} {
			childFD, childErr := openPrivateDirectoryAt(int(root.Fd()), child, true)
			if childErr != nil {
				_ = root.Close()
				return nil, childErr
			}
			// openAttempt retains fresh descriptors below.
			_ = unix.Close(childFD)
		}
		_ = root.Close()
		if err := state.attempts.Sync(); err != nil {
			return nil, err
		}
		return state.openAttempt(attemptID)
	}
	return nil, fmt.Errorf("claim launcher attempt: too many random ID collisions")
}

type attemptRef struct {
	id    string
	index uint64
}

func (state *launchState) listAttempts() ([]attemptRef, error) {
	if state.attempts == nil {
		return nil, nil
	}
	if _, err := state.attempts.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := state.attempts.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	refs := make([]attemptRef, 0, len(entries))
	for _, entry := range entries {
		index, err := parseAttemptID(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("unexpected launch attempt entry %q: %w", entry.Name(), err)
		}
		refs = append(refs, attemptRef{id: entry.Name(), index: index})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].index < refs[j].index })
	for i, ref := range refs {
		if ref.index != uint64(i+1) {
			return nil, fmt.Errorf("launch attempt history is not contiguous at %s", ref.id)
		}
	}
	return refs, nil
}

func parseAttemptID(value string) (uint64, error) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 || len(parts[0]) != 6 || len(parts[1]) != 32 {
		return 0, fmt.Errorf("invalid immutable attempt ID")
	}
	index, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || index == 0 || fmt.Sprintf("%06d", index) != parts[0] {
		return 0, fmt.Errorf("invalid immutable attempt index")
	}
	if decoded, err := hex.DecodeString(parts[1]); err != nil || len(decoded) != 16 {
		return 0, fmt.Errorf("invalid immutable attempt entropy")
	}
	return index, nil
}

func (attempt *launchAttempt) Close() error {
	if attempt == nil {
		return nil
	}
	err := errors.Join(closeFile(attempt.exec), closeFile(attempt.ready), closeFile(attempt.root))
	attempt.exec, attempt.ready, attempt.root = nil, nil, nil
	return err
}

func closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func (attempt *launchAttempt) directory(child string) (*os.File, error) {
	switch child {
	case "":
		return attempt.root, nil
	case readyDirectoryName:
		return attempt.ready, nil
	case execDirectoryName:
		return attempt.exec, nil
	default:
		return nil, fmt.Errorf("invalid attempt artifact directory %q", child)
	}
}

func (attempt *launchAttempt) preReleaseProcessEvidence() (int, bool, error) {
	if _, err := attempt.ready.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	readyEntries, err := attempt.ready.ReadDir(-1)
	if err != nil {
		return 0, false, err
	}
	readyPID := 0
	for _, entry := range readyEntries {
		name := entry.Name()
		pid, parseErr := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if parseErr != nil || pid <= 0 || name != strconv.Itoa(pid)+".json" || readyPID != 0 {
			return 0, false, fmt.Errorf("ambiguous pre-release ready evidence %q", name)
		}
		readyPID = pid
	}
	if _, err := attempt.exec.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	execEntries, err := attempt.exec.ReadDir(-1)
	if err != nil {
		return 0, false, err
	}
	lockPID := 0
	for _, entry := range execEntries {
		name := entry.Name()
		pid, parseErr := strconv.Atoi(strings.TrimSuffix(name, ".lock"))
		if parseErr != nil || pid <= 0 || name != strconv.Itoa(pid)+".lock" || lockPID != 0 {
			return 0, false, fmt.Errorf("ambiguous pre-release exec evidence %q", name)
		}
		lockPID = pid
	}
	if readyPID == 0 && lockPID == 0 {
		return 0, false, nil
	}
	if lockPID == 0 || readyPID != 0 && readyPID != lockPID {
		return 0, false, fmt.Errorf("pre-release process evidence does not bind one exact PID")
	}
	return lockPID, true, nil
}

func openPrivateDirectoryAt(parentFD int, name string, create bool) (int, error) {
	if create {
		if err := unix.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("create private launch directory %s: %w", name, err)
		}
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open private launch directory %s: %w", name, err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("launch directory %s must be one private 0700 directory", name)
	}
	return fd, nil
}

func fileForFD(fd int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap %s", name)
	}
	return file, nil
}

func (state *launchState) read(child, name string) ([]byte, error) {
	directory, err := state.directory(child)
	if err != nil {
		return nil, err
	}
	return readLaunchArtifact(directory, name)
}

func (attempt *launchAttempt) read(child, name string) ([]byte, error) {
	directory, err := attempt.directory(child)
	if err != nil {
		return nil, err
	}
	return readLaunchArtifact(directory, name)
}

func readLaunchArtifact(directory *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file, err := fileForFD(fd, "wb-session-launch-artifact")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if err := validatePrivateLaunchFile(fd, name, maxLaunchArtifactBytes); err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxLaunchArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxLaunchArtifactBytes || int64(len(raw)) != stat.Size {
		return nil, fmt.Errorf("launch artifact %s is oversized or changed while being read", name)
	}
	return raw, nil
}

func (state *launchState) publish(child, name string, raw []byte) (bool, error) {
	directory, err := state.directory(child)
	if err != nil {
		return false, err
	}
	return publishLaunchArtifact(directory, name, raw)
}

func (attempt *launchAttempt) publish(child, name string, raw []byte) (bool, error) {
	directory, err := attempt.directory(child)
	if err != nil {
		return false, err
	}
	return publishLaunchArtifact(directory, name, raw)
}

func publishLaunchArtifact(directory *os.File, name string, raw []byte) (bool, error) {
	if len(raw) > maxLaunchArtifactBytes {
		return false, fmt.Errorf("launch artifact exceeds %d bytes", maxLaunchArtifactBytes)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, err
	}
	temporaryName := ".pending-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(directory.Fd()), temporaryName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return false, err
	}
	temporary, err := fileForFD(fd, "wb-session-launch-pending")
	if err != nil {
		_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		return false, err
	}
	closed := false
	linked := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if !linked {
			_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return false, err
	}
	written, err := temporary.Write(raw)
	if err != nil || written != len(raw) {
		if err != nil {
			return false, err
		}
		return false, io.ErrShortWrite
	}
	if err := temporary.Sync(); err != nil {
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	closed = true
	if err := unix.Linkat(int(directory.Fd()), temporaryName, int(directory.Fd()), name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return false, nil
		}
		return false, err
	}
	if err := unix.Unlinkat(int(directory.Fd()), temporaryName, 0); err != nil {
		return false, err
	}
	linked = true
	if err := directory.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func (attempt *launchAttempt) acquireExecFence(pid int) (*os.File, error) {
	directory, err := attempt.directory(execDirectoryName)
	if err != nil {
		return nil, err
	}
	name := strconv.Itoa(pid) + ".lock"
	fd, err := unix.Openat(int(directory.Fd()), name,
		unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := validatePrivateLaunchFile(fd, name, 0); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("acquire launcher exec-success fence for PID %d: %w", pid, err)
	}
	return fileForFD(fd, "wb-session-launch-exec-fence")
}

// execFenceHeld reports whether the private WB wrapper still holds the
// exclusive CLOEXEC fence. Successful Exec atomically changes this to false.
func (attempt *launchAttempt) execFenceHeld(pid int) (bool, error) {
	directory, err := attempt.directory(execDirectoryName)
	if err != nil {
		return false, err
	}
	name := strconv.Itoa(pid) + ".lock"
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := validatePrivateLaunchFile(fd, name, 0); err != nil {
		return false, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	if err := unix.Flock(fd, unix.LOCK_UN); err != nil {
		return false, err
	}
	return false, nil
}

// Path-opening wrappers are intentionally limited to tests and isolated
// one-shot inspection. Authorization flows retain one launchState and call its
// methods directly so no transaction crosses directory identities.
func savePlan(root string, plan launchPlan) (launchPlan, sessionmove.Digest, bool, error) {
	state, err := openLaunchState(root, plan.HandoffID, true)
	if err != nil {
		return launchPlan{}, "", false, err
	}
	defer func() { _ = state.Close() }()
	return state.savePlan(plan)
}

func loadPlan(root, handoffID string) (launchPlan, sessionmove.Digest, error) {
	state, err := openLaunchState(root, handoffID, false)
	if err != nil {
		return launchPlan{}, "", err
	}
	defer func() { _ = state.Close() }()
	return state.loadPlan()
}

func loadReady(root, handoffID string, pid int) (launcherReady, sessionmove.Digest, error) {
	state, err := openLaunchState(root, handoffID, false)
	if err != nil {
		return launcherReady{}, "", err
	}
	defer func() { _ = state.Close() }()
	attempt, err := latestAttempt(state)
	if err != nil {
		return launcherReady{}, "", err
	}
	defer func() { _ = attempt.Close() }()
	return attempt.loadReady(pid)
}

func saveRelease(root string, plan launchPlan, planDigest sessionmove.Digest, ready launcherReady, targetWorkLogRef string, now time.Time) (launcherRelease, bool, error) {
	state, err := openLaunchState(root, plan.HandoffID, true)
	if err != nil {
		return launcherRelease{}, false, err
	}
	defer func() { _ = state.Close() }()
	attempt, err := state.openAttempt(ready.AttemptID)
	if err != nil {
		return launcherRelease{}, false, err
	}
	defer func() { _ = attempt.Close() }()
	return attempt.saveRelease(plan, planDigest, ready, targetWorkLogRef, now)
}

func loadRelease(root, handoffID string) (launcherRelease, sessionmove.Digest, error) {
	state, err := openLaunchState(root, handoffID, false)
	if err != nil {
		return launcherRelease{}, "", err
	}
	defer func() { _ = state.Close() }()
	attempt, err := latestAttempt(state)
	if err != nil {
		return launcherRelease{}, "", err
	}
	defer func() { _ = attempt.Close() }()
	return attempt.loadRelease()
}

func latestAttempt(state *launchState) (*launchAttempt, error) {
	refs, err := state.listAttempts()
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, os.ErrNotExist
	}
	return state.openAttempt(refs[len(refs)-1].id)
}

func validatePrivateLaunchFile(fd int, name string, maximum int64) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Nlink != 1 || stat.Size < 0 || stat.Size > maximum {
		return fmt.Errorf("launch artifact %s must be one private bounded 0600 regular file", name)
	}
	return nil
}

func equalLaunchPlan(left, right launchPlan) bool {
	leftRaw, leftErr := encodeLaunchJSON(left)
	rightRaw, rightErr := encodeLaunchJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func equalReady(left, right launcherReady) bool {
	leftRaw, leftErr := encodeLaunchJSON(left)
	rightRaw, rightErr := encodeLaunchJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}
