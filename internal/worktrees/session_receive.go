package worktrees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// SessionReceiveOptions is the target-only Git boundary for a portable
// session handoff. It deliberately accepts the validated protocol request as
// one unit so the branch, commits, path, and digest cannot drift apart while
// being copied through a general worktree-create option surface.
type SessionReceiveOptions struct {
	ProjectsRoot  string
	Request       sessionmove.Request
	RequestDigest sessionmove.Digest
	// ExecutionLock is the already-held per-handoff receiver fence. Its
	// exact admitted Store/request authority can authorize recovery of this
	// handoff's interrupted worktree operation; ordinary direct callers and
	// locks from another Store remain fail-closed.
	ExecutionLock *sessionmove.ExecutionLock

	// Test-only coordination after origin authentication and before the exact
	// request URL is fetched. Production callers cannot set unexported seams.
	afterFetchRemoteAuthentication func()
}

// SessionReceiveSpec is the protocol-neutral exact Git authority below
// session-move and parked-bundle receivers. The existing Request adapter keeps
// every legacy ancestor/handover proof; parked members deliberately omit those
// move-only fields while retaining exact remote-tip, pin, replay, and fence
// proofs.
type SessionReceiveSpec struct {
	AuthorityID      string
	AuthorityDigest  sessionmove.Digest
	AuthorityStore   string
	Fence            sessionauthority.Fence
	OperationID      string
	MemberKey        string
	RepositoryRemote string
	Branch           string
	Commit           string
	PinBranch        string
	SourceWorkCommit string
	HandoverPath     string
	HandoverDigest   sessionmove.Digest
}

type SessionMemberReceiveOptions struct {
	ProjectsRoot string
	Spec         SessionReceiveSpec
}

func sessionMoveReceiveSpec(projectsRoot string, options SessionReceiveOptions) (SessionReceiveSpec, error) {
	request := options.Request
	if _, err := sessionmove.EncodeRequest(request); err != nil {
		return SessionReceiveSpec{}, err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return SessionReceiveSpec{}, err
	}
	return SessionReceiveSpec{
		AuthorityID: request.HandoffID, AuthorityDigest: options.RequestDigest,
		AuthorityStore: filepath.Join(home, sessionmove.DirName), Fence: options.ExecutionLock,
		OperationID: request.HandoffID, MemberKey: "primary", RepositoryRemote: request.RepositoryRemote,
		Branch: request.Branch, Commit: request.BundleCommit, PinBranch: "wb-session/" + request.HandoffID,
		SourceWorkCommit: request.SourceWorkCommit, HandoverPath: request.HandoverPath, HandoverDigest: request.HandoverDigest,
	}, nil
}

func validateSessionReceiveSpec(ctx context.Context, spec SessionReceiveSpec) error {
	for name, value := range map[string]string{"authority ID": spec.AuthorityID, "operation ID": spec.OperationID, "member key": spec.MemberKey} {
		if !sessionauthority.ValidID(value) {
			return fmt.Errorf("session receive %s is not one fixed safe ID", name)
		}
	}
	if _, err := gitremote.Parse(spec.RepositoryRemote); err != nil {
		return err
	}
	if !validBranch(ctx, spec.Branch) || !validBranch(ctx, spec.PinBranch) {
		return fmt.Errorf("session receive branch or pin is invalid")
	}
	if !isGitObjectID(spec.Commit) {
		return fmt.Errorf("session receive commit is not one full Git object ID")
	}
	if spec.SourceWorkCommit != "" && !isGitObjectID(spec.SourceWorkCommit) {
		return fmt.Errorf("session receive source commit is not one full Git object ID")
	}
	if spec.HandoverPath == "" != (spec.HandoverDigest == "") {
		return fmt.Errorf("session receive tracked handover path and digest must be supplied together")
	}
	if spec.AuthorityStore != "" {
		if !filepath.IsAbs(spec.AuthorityStore) || filepath.Clean(spec.AuthorityStore) != spec.AuthorityStore || spec.Fence == nil {
			return fmt.Errorf("session receive replay authority is incomplete")
		}
	}
	return nil
}

// SessionReceiveResult identifies the exact target checkout. Task 3 creates
// no process and no receipt; later receiver stages use this pinned path.
type SessionReceiveResult struct {
	Repository    string `json:"repository"`
	CanonicalDir  string `json:"canonical_dir"`
	WorktreeDir   string `json:"worktree_dir"`
	Commit        string `json:"commit"`
	HandoverBytes []byte `json:"-"`
	Reused        bool   `json:"reused"`
}

// SessionReceiveWorktreePath derives the one accepted target checkout path
// from local ProjectsRoot plus the immutable request. It performs no Git or
// network access and is safe for successor-start replay after the harness may
// already have changed the worktree.
func SessionReceiveWorktreePath(projectsRoot string, request sessionmove.Request) (string, error) {
	if _, err := sessionmove.EncodeRequest(request); err != nil {
		return "", err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	return SessionReceiveMemberPath(projectsRoot, SessionReceiveSpec{
		AuthorityID: request.HandoffID, AuthorityDigest: sessionmove.DigestBytes([]byte("path-only")),
		AuthorityStore: filepath.Join(home, sessionmove.DirName), OperationID: request.HandoffID, MemberKey: "primary",
		RepositoryRemote: request.RepositoryRemote, Branch: request.Branch, Commit: request.BundleCommit,
		PinBranch: "wb-session/" + request.HandoffID, SourceWorkCommit: request.SourceWorkCommit,
		HandoverPath: request.HandoverPath, HandoverDigest: request.HandoverDigest,
	})
}

func SessionReceiveMemberPath(projectsRoot string, spec SessionReceiveSpec) (string, error) {
	if err := validateSessionReceiveSpec(context.Background(), spec); err != nil {
		// Path derivation does not need a live fence. Admit an intentionally
		// absent store/fence only for this read-only helper.
		if spec.AuthorityStore != "" && spec.Fence == nil {
			spec.AuthorityStore = ""
			if retry := validateSessionReceiveSpec(context.Background(), spec); retry != nil {
				return "", retry
			}
		} else {
			return "", err
		}
	}
	root, err := absoluteProjectsRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	remote, err := gitremote.Parse(spec.RepositoryRemote)
	if err != nil {
		return "", err
	}
	owner, name, _, err := canonicalRepositoryPath(root, remote.Identity.Repository)
	if err != nil {
		return "", err
	}
	home, err := wbhome.Root(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "worktrees", "session-"+spec.OperationID, owner, name), nil
}

// VerifyReceivedSessionBundle is the local-only replay boundary after a
// worktree_ready event. It deliberately performs no fetch or remote-tip check:
// a later legitimate source-branch push must not strand an already accepted
// exact handoff. The immutable request, held receive lock, local pin branch,
// clean checkout, commit, and handover blob remain mandatory.
func VerifyReceivedSessionBundle(ctx context.Context, options SessionReceiveOptions) (SessionReceiveResult, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	spec, err := sessionMoveReceiveSpec(projectsRoot, options)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	return VerifyReceivedSessionMember(ctx, SessionMemberReceiveOptions{ProjectsRoot: projectsRoot, Spec: spec})
}

func VerifyReceivedSessionMember(ctx context.Context, options SessionMemberReceiveOptions) (SessionReceiveResult, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	spec := options.Spec
	if err := validateSessionReceiveSpec(ctx, spec); err != nil {
		return SessionReceiveResult{}, err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	if spec.Fence == nil || !spec.Fence.HeldForSession(spec.AuthorityStore, spec.AuthorityID, string(spec.AuthorityDigest)) {
		return SessionReceiveResult{}, fmt.Errorf("local session receive replay requires exact admitted handoff authority")
	}
	remote, err := gitremote.Parse(spec.RepositoryRemote)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	owner, name, canonicalPath, err := canonicalRepositoryPath(projectsRoot, remote.Identity.Repository)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	held, err := openAbsoluteDirectoryNoFollow(canonicalPath, false)
	if err != nil {
		return SessionReceiveResult{}, fmt.Errorf("open accepted canonical repository: %w", err)
	}
	defer held.Close()
	canonical, err := openSessionReceiveCanonicalFromHeldRoot(canonicalPath, held)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	defer canonical.close()
	if err := verifySessionReceiveCanonical(ctx, canonical, remote.Identity); err != nil {
		return SessionReceiveResult{}, err
	}
	operationPath := filepath.Join(home, "worktrees", "session-"+spec.OperationID)
	worktreePath := filepath.Join(operationPath, owner, name)
	if err := verifySessionReceiveReuse(ctx, canonical, operationPath, worktreePath, spec.PinBranch, spec.Commit); err != nil {
		return SessionReceiveResult{}, fmt.Errorf("verify accepted pinned target worktree locally: %w", err)
	}
	var handoverBytes []byte
	if spec.HandoverPath != "" {
		handoverBytes, err = gitCanonicalBytes(ctx, canonical, "cat-file", "blob", spec.Commit+":"+spec.HandoverPath)
		if err != nil {
			return SessionReceiveResult{}, fmt.Errorf("read accepted handover blob locally: %w", err)
		}
		if !spec.HandoverDigest.Matches(handoverBytes) {
			return SessionReceiveResult{}, fmt.Errorf("accepted handover blob digest changed")
		}
	}
	return SessionReceiveResult{Repository: remote.Identity.Repository, CanonicalDir: canonicalPath, WorktreeDir: worktreePath,
		Commit: spec.Commit, HandoverBytes: handoverBytes, Reused: true}, nil
}

// ReceiveSessionBundle fetches and verifies a request's exact public Git
// evidence, then creates or verifies one deterministic isolated target
// worktree. It never starts a successor or changes source custody.
func ReceiveSessionBundle(ctx context.Context, options SessionReceiveOptions) (SessionReceiveResult, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	spec, err := sessionMoveReceiveSpec(projectsRoot, options)
	if err != nil {
		return SessionReceiveResult{}, fmt.Errorf("validate target session request: %w", err)
	}
	memberOptions := SessionMemberReceiveOptions{ProjectsRoot: projectsRoot, Spec: spec}
	result, err := receiveSessionMember(ctx, memberOptions, options.afterFetchRemoteAuthentication)
	return result, err
}

func ReceiveSessionMember(ctx context.Context, options SessionMemberReceiveOptions) (SessionReceiveResult, error) {
	return receiveSessionMember(ctx, options, nil)
}

func receiveSessionMember(ctx context.Context, options SessionMemberReceiveOptions, afterFetchRemoteAuthentication func()) (SessionReceiveResult, error) {
	var result SessionReceiveResult
	spec := options.Spec
	if err := validateSessionReceiveSpec(ctx, spec); err != nil {
		return result, fmt.Errorf("validate target session receive authority: %w", err)
	}
	remote, err := gitremote.Parse(spec.RepositoryRemote)
	if err != nil {
		return result, err
	}
	repository := remote.Identity.Repository
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	owner, name, canonicalPath, err := canonicalRepositoryPath(projectsRoot, repository)
	if err != nil {
		return result, err
	}
	if err := requireGitFilesystemCapability(); err != nil {
		return result, err
	}
	canonical, err := openOrCloneSessionReceiveCanonical(
		ctx, projectsRoot, owner, name, canonicalPath, spec.RepositoryRemote, remote.Identity,
	)
	if err != nil {
		return result, err
	}
	defer canonical.close()
	if err := verifySessionReceiveCanonical(ctx, canonical, remote.Identity); err != nil {
		return result, err
	}
	if afterFetchRemoteAuthentication != nil {
		afterFetchRemoteAuthentication()
	}

	// Fetch the declared source ref itself into a handoff-private exact ref.
	// FETCH_HEAD and remote-tracking refs are shared mutable state and are never
	// evidence for receive; the already-parsed safe request URL is the network
	// authority, not origin's mutable configuration.
	fetchedRef := sessionReceiveFetchRef(spec.OperationID)
	refspec := "+refs/heads/" + spec.Branch + ":" + fetchedRef
	if _, err := gitCanonical(ctx, canonical, "fetch", "--no-tags", "--force", "--no-write-fetch-head", "--", remote.Raw, refspec); err != nil {
		return result, fmt.Errorf("fetch live session branch %s/%s: %w", repository, spec.Branch, err)
	}
	if err := verifySessionReceiveCanonical(ctx, canonical, remote.Identity); err != nil {
		return result, fmt.Errorf("reauthenticate canonical origin after exact fetch: %w", err)
	}
	fetchedTip, err := gitCanonical(ctx, canonical, "rev-parse", "--verify", fetchedRef+"^{commit}")
	if err != nil || !isGitObjectID(fetchedTip) {
		return result, fmt.Errorf("resolve live fetched branch tip for %s/%s: %w", repository, spec.Branch, err)
	}
	if fetchedTip != spec.Commit {
		return result, fmt.Errorf("remote branch tip moved for %s/%s: fetched %s, session requires exact commit %s", repository, spec.Branch, fetchedTip, spec.Commit)
	}
	if spec.SourceWorkCommit != "" {
		if _, err := gitCanonical(ctx, canonical, "cat-file", "-e", spec.SourceWorkCommit+"^{commit}"); err != nil {
			return result, fmt.Errorf("source work commit is missing from %s: %s: %w", repository, spec.SourceWorkCommit, err)
		}
		if _, err := gitCanonical(ctx, canonical, "merge-base", "--is-ancestor", spec.SourceWorkCommit, spec.Commit); err != nil {
			return result, fmt.Errorf("source work commit %s is not an ancestor of admitted commit %s", spec.SourceWorkCommit, spec.Commit)
		}
	}
	var handoverBytes []byte
	if spec.HandoverPath != "" {
		handoverBytes, err = gitCanonicalBytes(ctx, canonical, "cat-file", "blob", spec.Commit+":"+spec.HandoverPath)
		if err != nil {
			return result, fmt.Errorf("read tracked handover blob %s from admitted commit %s: %w", spec.HandoverPath, spec.Commit, err)
		}
		if !spec.HandoverDigest.Matches(handoverBytes) {
			return result, fmt.Errorf("handover digest mismatch for %s: committed bytes compute %s, request declares %s", spec.HandoverPath, sessionmove.DigestBytes(handoverBytes), spec.HandoverDigest)
		}
	}
	pinBranch := spec.PinBranch

	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return result, err
	}
	operationName := "session-" + spec.OperationID
	operation, err := prepareOperationRoot(home, operationName, nil)
	if err != nil {
		return result, err
	}
	defer operation.close()
	reclaimInterrupted := spec.Fence != nil && spec.Fence.HeldForSession(spec.AuthorityStore, spec.AuthorityID, string(spec.AuthorityDigest))
	lock, err := acquireLockAtReclaimingInterrupted(operation.Directory, reclaimInterrupted, operationName)
	if err != nil {
		return result, err
	}
	if lock.interrupted {
		if spec.Fence == nil || !spec.Fence.HeldForSession(spec.AuthorityStore, spec.AuthorityID, string(spec.AuthorityDigest)) {
			_ = lock.file.Close()
			lock.file = nil
			return result, fmt.Errorf("exact admitted handoff authority changed before interrupted target recovery")
		}
		if _, err := interruptedTaskLockPID(lock.file, operationName); err != nil {
			_ = lock.file.Close() // preserve the ambiguous named remnant.
			lock.file = nil
			return result, fmt.Errorf("validate interrupted target receive lock: %w", err)
		}
	}
	defer func() { _ = lock.release() }()

	worktreePath, exists, err := prepareWorktreeDestination(operation.Path, operation.Directory, owner, name)
	if err != nil {
		return result, err
	}
	result = SessionReceiveResult{
		Repository: repository, CanonicalDir: canonicalPath, WorktreeDir: worktreePath,
		Commit: spec.Commit, HandoverBytes: append([]byte(nil), handoverBytes...), Reused: exists,
	}
	if lock.interrupted {
		recovered, recoveryErr := recoverInterruptedSessionReceivePublication(
			ctx, canonical, operation.Path, operation.Directory, owner, name, worktreePath,
			pinBranch, spec.Commit, exists,
		)
		if recoveryErr != nil {
			// Keep the exact dead-owner lock record named when recovery cannot
			// be completed. A later identical admitted receive may retry it;
			// ordinary calls still cannot claim the remnant.
			_ = lock.file.Close()
			lock.file = nil
			return SessionReceiveResult{}, recoveryErr
		}
		if recovered {
			result.Reused = true
			return result, nil
		}
	}
	if exists {
		if err := verifySessionReceiveReuse(ctx, canonical, operation.Path, worktreePath, pinBranch, spec.Commit); err != nil {
			return SessionReceiveResult{}, err
		}
		return result, nil
	}

	branch := pinBranch
	branchExists, err := localBranchExistsCanonical(ctx, canonical, branch)
	if err != nil {
		return SessionReceiveResult{}, err
	}
	if branchExists {
		tip, tipErr := gitCanonical(ctx, canonical, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
		if tipErr != nil || tip != spec.Commit {
			return SessionReceiveResult{}, fmt.Errorf("existing target pin branch %q does not identify exact admitted commit %s", branch, spec.Commit)
		}
		if occupied, path, occupiedErr := branchWorktreeCanonical(ctx, canonical, branch); occupiedErr != nil {
			return SessionReceiveResult{}, occupiedErr
		} else if occupied {
			return SessionReceiveResult{}, fmt.Errorf("existing target pin branch %q is already checked out at conflicting path %s", branch, path)
		}
	}
	var publication *createdWorktreePublication
	if err := addWorktreeAtSecureDestination(
		ctx, canonical, operation.Path, operation.Directory, owner, name,
		branch, spec.Branch, spec.Commit, branchExists,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &publication,
	); err != nil {
		return SessionReceiveResult{}, fmt.Errorf("create pinned target worktree: %w", err)
	}
	if publication != nil {
		defer publication.close()
	}
	if err := verifySessionReceiveReuse(ctx, canonical, operation.Path, worktreePath, pinBranch, spec.Commit); err != nil {
		return SessionReceiveResult{}, fmt.Errorf("verify new pinned target worktree: %w", err)
	}
	return result, nil
}

func sessionReceiveFetchRef(handoffID string) string {
	sum := sha256.Sum256([]byte(handoffID))
	return fmt.Sprintf("refs/wb/session-receive/%x", sum[:])
}

func verifySessionReceiveCanonical(ctx context.Context, canonical *canonicalRepository, declared gitremote.Identity) error {
	repository := declared.Repository
	root, err := gitCanonical(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if filepath.Clean(root) != filepath.Clean(canonical.path) {
		return fmt.Errorf("%s is not the root of canonical clone %s", canonical.path, repository)
	}
	gitDir, commonDir, err := gitDirectoriesCanonical(ctx, canonical)
	if err != nil {
		return err
	}
	if gitDir != commonDir {
		return fmt.Errorf("%s is a linked worktree, not the canonical clone for %s", canonical.path, repository)
	}
	origin, err := readCanonicalOriginRemote(ctx, canonical, false)
	if err != nil {
		return fmt.Errorf("read canonical origin for %s: %w", repository, err)
	}
	parsedOrigin, err := gitremote.Parse(origin)
	if err != nil {
		return fmt.Errorf("canonical origin logical identity for %s is invalid: %w", repository, err)
	}
	if !parsedOrigin.Identity.Equal(declared) {
		return fmt.Errorf("canonical origin logical identity does not match handoff remote logical identity for %s", repository)
	}
	return nil
}

// openOrCloneSessionReceiveCanonical resolves one canonical clone without
// granting a request-controlled path authority. A missing clone is created in
// a private descriptor-held stage below the validated owner directory,
// verified there, published with rename-no-replace, and verified again at the
// configured canonical path before it can participate in a receive.
func openOrCloneSessionReceiveCanonical(
	ctx context.Context,
	projectsRoot, owner, name, canonicalPath, declaredRemote string,
	declared gitremote.Identity,
) (*canonicalRepository, error) {
	projects, err := openAbsoluteDirectoryNoFollow(projectsRoot, true)
	if err != nil {
		return nil, fmt.Errorf("open projects root for target receive: %w", err)
	}
	defer func() { _ = projects.Close() }()
	ownerFD, err := openOrCreateNoFollowDirectory(int(projects.Fd()), owner)
	if err != nil {
		return nil, err
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-session-receive-owner")
	if ownerDirectory == nil {
		_ = unix.Close(ownerFD)
		return nil, fmt.Errorf("wrap canonical owner directory for %s", declared.Repository)
	}
	defer func() { _ = ownerDirectory.Close() }()

	existingFD, openErr := unix.Openat(int(ownerDirectory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if openErr == nil {
		existing := os.NewFile(uintptr(existingFD), "wb-session-receive-existing-canonical")
		if existing == nil {
			_ = unix.Close(existingFD)
			return nil, fmt.Errorf("wrap existing canonical clone for %s", declared.Repository)
		}
		defer func() { _ = existing.Close() }()
		return openSessionReceiveCanonicalFromHeldRoot(canonicalPath, existing)
	}
	if !errors.Is(openErr, unix.ENOENT) {
		return nil, fmt.Errorf("inspect canonical clone for %s: %w", declared.Repository, openErr)
	}
	return cloneSessionReceiveCanonical(
		ctx, ownerDirectory, filepath.Join(projectsRoot, owner), name, canonicalPath, declaredRemote, declared,
	)
}

func cloneSessionReceiveCanonical(
	ctx context.Context,
	ownerDirectory *os.File,
	ownerPath, name, canonicalPath, declaredRemote string,
	declared gitremote.Identity,
) (canonical *canonicalRepository, returnErr error) {
	if err := requireAbsentNoFollowChild(int(ownerDirectory.Fd()), name); err != nil {
		return nil, err
	}
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return nil, fmt.Errorf("canonical owner path changed before cloning %s", declared.Repository)
	}
	stageName, err := makeSecureStageDirectory(ownerDirectory)
	if err != nil {
		return nil, fmt.Errorf("create secure canonical clone stage for %s: %w", declared.Repository, err)
	}
	stageFD, err := unix.Openat(int(ownerDirectory.Fd()), stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open secure canonical clone stage for %s: %w", declared.Repository, err)
	}
	stage := os.NewFile(uintptr(stageFD), "wb-session-receive-clone-stage")
	if stage == nil {
		_ = unix.Close(stageFD)
		return nil, fmt.Errorf("wrap secure canonical clone stage for %s", declared.Repository)
	}
	defer func() {
		quarantineErr := quarantineMatchingStageDirectoryAt(ownerDirectory, stage)
		_ = stage.Close()
		if quarantineErr != nil && returnErr == nil {
			if canonical != nil {
				canonical.close()
				canonical = nil
			}
			returnErr = fmt.Errorf("retire secure canonical clone stage for %s: %w", declared.Repository, quarantineErr)
		}
	}()
	if err := verifySecureStageDirectory(ctx, stage, ownerPath); err != nil {
		return nil, err
	}
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		return nil, err
	}
	if _, err := runSecureStageHelper(
		ctx, stage, ownerPath, gitExecutable,
		"clone", "--origin", "origin", "--", declaredRemote, "checkout",
	); err != nil {
		// Deliberately omit Git's command output: a malformed or legacy remote
		// can contain credentials, and receive diagnostics must never echo it.
		return nil, fmt.Errorf("secure clone of canonical repository %s failed", declared.Repository)
	}
	if err := verifySecureStageDirectory(ctx, stage, ownerPath); err != nil {
		return nil, err
	}
	stagePath, err := secureDirectoryPath(ctx, stage)
	if err != nil {
		return nil, err
	}
	stagedPath := filepath.Join(stagePath, "checkout")
	checkoutFD, err := unix.Openat(int(stage.Fd()), "checkout", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open staged canonical clone for %s: %w", declared.Repository, err)
	}
	checkout := os.NewFile(uintptr(checkoutFD), "wb-session-receive-staged-canonical")
	if checkout == nil {
		_ = unix.Close(checkoutFD)
		return nil, fmt.Errorf("wrap staged canonical clone for %s", declared.Repository)
	}
	defer func() { _ = checkout.Close() }()
	staged, err := openSessionReceiveCanonicalFromHeldRoot(stagedPath, checkout)
	if err != nil {
		return nil, fmt.Errorf("retain staged canonical clone for %s: %w", declared.Repository, err)
	}
	defer staged.close()
	if err := verifySessionReceiveCanonical(ctx, staged, declared); err != nil {
		return nil, fmt.Errorf("verify staged canonical clone for %s: %w", declared.Repository, err)
	}
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return nil, fmt.Errorf("canonical owner path changed before publishing %s", declared.Repository)
	}
	published, err := moveExpectedDirectoryNoReplace(stage, "checkout", ownerDirectory, name, staged.root, nil)
	if err != nil {
		if published != nil {
			_ = published.Close()
		}
		return nil, fmt.Errorf("publish canonical clone for %s: %w", declared.Repository, err)
	}
	defer func() { _ = published.Close() }()

	canonical, err = openSessionReceiveCanonicalFromHeldRoot(canonicalPath, published)
	if err == nil {
		err = verifySessionReceiveCanonical(ctx, canonical, declared)
	}
	if err == nil {
		return canonical, nil
	}
	if canonical != nil {
		canonical.close()
		canonical = nil
	}
	rolledBack, rollbackErr := moveExpectedDirectoryNoReplace(ownerDirectory, name, stage, "checkout", published, nil)
	if rolledBack != nil {
		_ = rolledBack.Close()
	}
	return nil, errors.Join(
		fmt.Errorf("verify published canonical clone for %s: %w", declared.Repository, err),
		rollbackErr,
	)
}

// openSessionReceiveCanonicalFromHeldRoot duplicates one already-authorized
// repository-root descriptor before opening `.git`. The final canonical path
// validation therefore proves the public spelling still names that exact
// retained directory; it cannot silently adopt a replacement inserted after
// the parent's openat or rename-no-replace publication.
func openSessionReceiveCanonicalFromHeldRoot(path string, held *os.File) (*canonicalRepository, error) {
	if held == nil {
		return nil, fmt.Errorf("canonical repository root descriptor is unavailable")
	}
	rootFD, err := unix.Dup(int(held.Fd()))
	if err != nil {
		return nil, fmt.Errorf("retain canonical repository root: %w", err)
	}
	unix.CloseOnExec(rootFD)
	root := os.NewFile(uintptr(rootFD), "wb-session-receive-canonical-root")
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, fmt.Errorf("wrap retained canonical repository root")
	}
	gitFD, err := unix.Openat(int(root.Fd()), ".git", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open canonical Git directory without following links: %w", err)
	}
	common := os.NewFile(uintptr(gitFD), "wb-session-receive-canonical-git-directory")
	if common == nil {
		_ = unix.Close(gitFD)
		_ = root.Close()
		return nil, fmt.Errorf("wrap canonical Git directory")
	}
	canonical := &canonicalRepository{path: path, root: root, common: common}
	if err := canonical.validate(); err != nil {
		canonical.close()
		return nil, err
	}
	return canonical, nil
}

func recoverInterruptedSessionReceivePublication(
	ctx context.Context,
	canonical *canonicalRepository,
	operationRoot string,
	operationDirectory *os.File,
	owner, repository, finalPath, pinBranch, bundleCommit string,
	finalExists bool,
) (bool, error) {
	occupied, registeredPath, err := branchWorktreeCanonical(ctx, canonical, pinBranch)
	if err != nil {
		return false, fmt.Errorf("inspect interrupted target pin registration: %w", err)
	}
	registeredPath = filepath.Clean(registeredPath)
	if !occupied {
		if finalExists {
			return false, fmt.Errorf("interrupted deterministic target exists without its exact pin registration")
		}
		return false, nil
	}
	if registeredPath == filepath.Clean(finalPath) {
		if !finalExists {
			return false, fmt.Errorf("interrupted target pin registration names a missing deterministic target")
		}
		if err := verifySessionReceiveReuse(ctx, canonical, operationRoot, finalPath, pinBranch, bundleCommit); err != nil {
			return false, fmt.Errorf("verify interrupted published target: %w", err)
		}
		if err := retireCompletedInterruptedSessionStage(ctx, operationRoot, operationDirectory); err != nil {
			return false, err
		}
		return true, nil
	}

	stageName, ok := exactInterruptedSessionStage(operationRoot, registeredPath)
	if !ok {
		return false, fmt.Errorf("refusing interrupted receive recovery from a path outside its exact WB stage")
	}
	if err := requireOnlyInterruptedSessionStage(operationDirectory, stageName); err != nil {
		return false, err
	}
	stageFD, err := unix.Openat(int(operationDirectory.Fd()), stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("open exact interrupted receive stage: %w", err)
	}
	stage := os.NewFile(uintptr(stageFD), "wb-session-receive-interrupted-stage")
	if stage == nil {
		_ = unix.Close(stageFD)
		return false, fmt.Errorf("wrap exact interrupted receive stage")
	}
	defer func() { _ = stage.Close() }()
	stagePath := filepath.Join(operationRoot, stageName)
	if !directoryStillMatches(stagePath, stage) {
		return false, fmt.Errorf("exact interrupted receive stage path changed before recovery")
	}
	if err := verifySecureStageDirectory(ctx, stage, operationRoot); err != nil {
		return false, fmt.Errorf("verify exact interrupted receive stage: %w", err)
	}

	ownerFD, err := openOrCreateNoFollowDirectory(int(operationDirectory.Fd()), owner)
	if err != nil {
		return false, err
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-session-receive-recovery-owner")
	if ownerDirectory == nil {
		_ = unix.Close(ownerFD)
		return false, fmt.Errorf("wrap interrupted receive owner directory")
	}
	defer func() { _ = ownerDirectory.Close() }()
	ownerPath := filepath.Join(operationRoot, owner)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return false, fmt.Errorf("interrupted receive owner path changed before recovery")
	}

	var finalDirectory *os.File
	if finalExists {
		if _, statErr := secureDirectoryIdentityAt(stageFD, "checkout"); statErr == nil {
			return false, fmt.Errorf("interrupted receive has both staged and published checkouts; refusing ambiguous recovery")
		} else if !errors.Is(statErr, unix.ENOENT) {
			return false, fmt.Errorf("inspect interrupted staged checkout: %w", statErr)
		}
		finalFD, openErr := unix.Openat(ownerFD, repository, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return false, fmt.Errorf("open interrupted published target: %w", openErr)
		}
		finalDirectory = os.NewFile(uintptr(finalFD), "wb-session-receive-interrupted-published")
		if finalDirectory == nil {
			_ = unix.Close(finalFD)
			return false, fmt.Errorf("wrap interrupted published target")
		}
	} else {
		if err := requireAbsentNoFollowChild(ownerFD, repository); err != nil {
			return false, err
		}
		checkoutFD, openErr := unix.Openat(stageFD, "checkout", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return false, fmt.Errorf("open exact interrupted staged checkout: %w", openErr)
		}
		checkout := os.NewFile(uintptr(checkoutFD), "wb-session-receive-interrupted-checkout")
		if checkout == nil {
			_ = unix.Close(checkoutFD)
			return false, fmt.Errorf("wrap exact interrupted staged checkout")
		}
		defer func() { _ = checkout.Close() }()
		if !directoryStillMatches(registeredPath, checkout) {
			return false, fmt.Errorf("interrupted staged checkout no longer matches its exact Git registration")
		}
		if err := verifyHeldSessionReceiveCheckout(ctx, canonical.path, operationRoot, registeredPath, checkout, pinBranch, bundleCommit); err != nil {
			return false, fmt.Errorf("verify exact interrupted staged checkout: %w", err)
		}
		finalDirectory, err = moveExpectedDirectoryNoReplace(stage, "checkout", ownerDirectory, repository, checkout, nil)
		if err != nil {
			if finalDirectory != nil {
				_ = finalDirectory.Close()
			}
			return false, fmt.Errorf("publish exact interrupted staged checkout: %w", err)
		}
	}
	defer func() { _ = finalDirectory.Close() }()
	if !directoryStillMatches(finalPath, finalDirectory) {
		return false, fmt.Errorf("interrupted published target path changed before repair")
	}
	if err := verifyHeldSessionReceiveCheckout(ctx, canonical.path, operationRoot, finalPath, finalDirectory, pinBranch, bundleCommit); err != nil {
		return false, fmt.Errorf("verify interrupted published checkout before repair: %w", err)
	}
	if err := runSecureCleanupGitHelper(
		ctx, canonical, ownerDirectory, finalDirectory, ownerPath, finalPath,
		"worktree", "repair", finalPath,
	); err != nil {
		return false, fmt.Errorf("repair exact interrupted target registration: %w", err)
	}
	if err := verifyHeldSessionReceiveCheckout(ctx, canonical.path, operationRoot, finalPath, finalDirectory, pinBranch, bundleCommit); err != nil {
		return false, fmt.Errorf("verify exact interrupted target after repair: %w", err)
	}
	occupied, repairedPath, err := branchWorktreeCanonical(ctx, canonical, pinBranch)
	if err != nil || !occupied || filepath.Clean(repairedPath) != filepath.Clean(finalPath) {
		return false, fmt.Errorf("exact interrupted target registration was not repaired to its deterministic path")
	}
	if err := quarantineMatchingStageDirectoryAt(operationDirectory, stage); err != nil {
		return false, fmt.Errorf("retire recovered interrupted receive stage: %w", err)
	}
	return true, nil
}

func exactInterruptedSessionStage(operationRoot, registeredPath string) (string, bool) {
	relative, err := filepath.Rel(filepath.Clean(operationRoot), filepath.Clean(registeredPath))
	if err != nil {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 || parts[1] != "checkout" || !validInterruptedSessionStageName(parts[0]) {
		return "", false
	}
	return parts[0], true
}

func validInterruptedSessionStageName(name string) bool {
	const prefix = ".wb-stage-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	for _, character := range name[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func requireOnlyInterruptedSessionStage(operationDirectory *os.File, wanted string) error {
	names, err := interruptedSessionStageNames(operationDirectory)
	if err != nil {
		return err
	}
	if len(names) != 1 || names[0] != wanted {
		return fmt.Errorf("multiple, missing, or mismatched active receive stages make interrupted recovery ambiguous")
	}
	return nil
}

func interruptedSessionStageNames(operationDirectory *os.File) ([]string, error) {
	if _, err := operationDirectory.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind interrupted receive operation directory: %w", err)
	}
	entries, err := operationDirectory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("inspect interrupted receive stages: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if validInterruptedSessionStageName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func retireCompletedInterruptedSessionStage(ctx context.Context, operationRoot string, operationDirectory *os.File) error {
	names, err := interruptedSessionStageNames(operationDirectory)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	if len(names) != 1 {
		return fmt.Errorf("multiple active receive stages make completed interrupted recovery ambiguous")
	}
	stageFD, err := unix.Openat(int(operationDirectory.Fd()), names[0], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open completed interrupted receive stage: %w", err)
	}
	stage := os.NewFile(uintptr(stageFD), "wb-session-receive-completed-stage")
	if stage == nil {
		_ = unix.Close(stageFD)
		return fmt.Errorf("wrap completed interrupted receive stage")
	}
	defer func() { _ = stage.Close() }()
	stagePath := filepath.Join(operationRoot, names[0])
	if !directoryStillMatches(stagePath, stage) {
		return fmt.Errorf("completed interrupted receive stage path changed before retirement")
	}
	if err := verifySecureStageDirectory(ctx, stage, operationRoot); err != nil {
		return fmt.Errorf("verify completed interrupted receive stage: %w", err)
	}
	empty, err := directoryEmpty(stage)
	if err != nil || !empty {
		if err != nil {
			return fmt.Errorf("inspect completed interrupted receive stage: %w", err)
		}
		return fmt.Errorf("completed interrupted receive stage is not empty; refusing retirement")
	}
	if err := quarantineMatchingStageDirectoryAt(operationDirectory, stage); err != nil {
		return fmt.Errorf("retire completed interrupted receive stage: %w", err)
	}
	return nil
}

func verifySessionReceiveReuse(ctx context.Context, canonical *canonicalRepository, operationRoot, worktreePath, pinBranch, bundleCommit string) error {
	handle, err := openAdoptedCleanupWorktree(worktreePath)
	if err != nil {
		return fmt.Errorf("open existing target linked worktree: %w", err)
	}
	defer handle.close()
	if err := verifyHeldSessionReceiveCheckout(ctx, canonical.path, operationRoot, worktreePath, handle.worktree, pinBranch, bundleCommit); err != nil {
		return err
	}
	occupied, registeredPath, err := branchWorktreeCanonical(ctx, canonical, pinBranch)
	if err != nil {
		return fmt.Errorf("inspect existing target pin registration: %w", err)
	}
	if !occupied || filepath.Clean(registeredPath) != filepath.Clean(worktreePath) {
		return fmt.Errorf("existing target pin branch is not registered at exact deterministic worktree %s", worktreePath)
	}
	return nil
}

func verifyHeldSessionReceiveCheckout(
	ctx context.Context,
	canonicalDir, operationRoot, worktreePath string,
	worktree *os.File,
	pinBranch, bundleCommit string,
) error {
	rootRaw, err := runSecureRenameGitBytesWithHeldWorktree(ctx, canonicalDir, operationRoot, worktreePath, worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("verify existing target linked worktree common directory: %w", err)
	}
	if filepath.Clean(strings.TrimSpace(string(rootRaw))) != filepath.Clean(worktreePath) {
		return fmt.Errorf("existing target linked worktree root does not match %s", worktreePath)
	}
	wantedRef := "refs/heads/" + pinBranch
	attachedRaw, err := runSecureRenameGitBytesWithHeldWorktree(ctx, canonicalDir, operationRoot, worktreePath, worktree, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || strings.TrimSpace(string(attachedRaw)) != wantedRef {
		return fmt.Errorf("existing target worktree is not attached to exact pin branch %s", wantedRef)
	}
	pinRaw, err := runSecureRenameGitBytesWithHeldWorktree(ctx, canonicalDir, operationRoot, worktreePath, worktree, "rev-parse", "--verify", wantedRef+"^{commit}")
	if err != nil || strings.TrimSpace(string(pinRaw)) != bundleCommit {
		return fmt.Errorf("existing target pin branch %s does not identify exact bundle commit %s", wantedRef, bundleCommit)
	}
	headRaw, err := runSecureRenameGitBytesWithHeldWorktree(ctx, canonicalDir, operationRoot, worktreePath, worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("verify existing target HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headRaw))
	if head != bundleCommit {
		return fmt.Errorf("existing target HEAD is %s, want exact bundle commit %s", head, bundleCommit)
	}
	status, err := runSecureRenameGitBytesWithHeldWorktree(ctx, canonicalDir, operationRoot, worktreePath, worktree, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("inspect existing target worktree status: %w", err)
	}
	if len(bytes.TrimSpace(status)) != 0 {
		return fmt.Errorf("existing target worktree is dirty; refusing reuse: %s", strings.TrimSpace(string(status)))
	}
	return nil
}

func sessionReceiveRepositoryFromRemote(remote string) (string, error) {
	parsed, err := gitremote.Parse(remote)
	if err != nil {
		return "", err
	}
	return parsed.Identity.Repository, nil
}
