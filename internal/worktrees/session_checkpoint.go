package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const sessionHandoverSchemaVersion = 1

// SessionHandover is the source agent's deliberately supplied continuation
// context. WB adds bounded identity and Git evidence, but never captures an
// environment or other ambient data automatically.
type SessionHandover struct {
	Summary            string
	ValidationEvidence string
	RemainingWork      string
	Body               []byte
}

// SessionCheckpointOptions describes the source-owned portion of a move. The
// optional IDs and timestamp are deterministic seams for callers resuming a
// preallocated operation and for tests; ordinary CLI callers leave them zero.
type SessionCheckpointOptions struct {
	ProjectsRoot         string
	Worktree             string
	SourceSession        session.Record
	TargetMachine        string
	RequestedHarness     string
	HandoffID            string
	SuccessorWBSessionID string
	Handover             SessionHandover
	Now                  time.Time

	// Test-only coordination after post-commit remote authentication and
	// before publication through the already-parsed exact push URL.
	afterPushRemoteAuthentication func()
	afterAdmission                func() error
}

// SessionCheckpointResult is the immutable courier input plus the exact
// tracked and private bytes from which its two digests were computed.
type SessionCheckpointResult struct {
	Request       sessionmove.Request `json:"request"`
	Digest        sessionmove.Digest  `json:"request_digest"`
	RequestBytes  []byte              `json:"-"`
	HandoverBytes []byte              `json:"-"`
	WorkLogEvent  LocalWorkLogEvent   `json:"work_log_event"`
}

type sessionCheckpointPreflight struct {
	root             string
	branch           string
	sourceCommit     string
	repositoryRemote string
	pushRemote       string
	workLogReference string
	canonicalDir     string
	worktreesRoot    string
	canonical        *canonicalRepository
	worktree         *cleanupWorktreeHandle
}

func (p *sessionCheckpointPreflight) close() {
	if p == nil {
		return
	}
	if p.worktree != nil {
		p.worktree.close()
	}
	if p.canonical != nil {
		p.canonical.close()
	}
}

// CreateSessionCheckpoint performs the source-owned transaction up to, but
// never including, courier delivery. All refusal predicates are evaluated
// before the tracked handover path, index, branch, remote, aggregate, or Work
// Log is mutated. The final Work Log record is an offer (Apply=false), so the
// predecessor keeps custody until a later receipt-gated task completes it.
func CreateSessionCheckpoint(ctx context.Context, options SessionCheckpointOptions) (SessionCheckpointResult, error) {
	var result SessionCheckpointResult
	if err := validateSessionHandover(options.Handover); err != nil {
		return result, err
	}
	if err := validateSourceSession(options.SourceSession); err != nil {
		return result, err
	}
	options.TargetMachine = strings.TrimSpace(options.TargetMachine)
	if options.TargetMachine == "" {
		return result, fmt.Errorf("target machine is required")
	}
	options.RequestedHarness = strings.TrimSpace(options.RequestedHarness)
	if strings.ContainsAny(options.RequestedHarness, "\r\n") {
		return result, fmt.Errorf("requested harness must be single-line")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	} else {
		options.Now = options.Now.UTC()
	}
	if options.Now.Before(options.SourceSession.StartedAt.UTC()) {
		return result, fmt.Errorf("session checkpoint time cannot precede the source session start")
	}

	handoffID := strings.TrimSpace(options.HandoffID)
	if handoffID == "" {
		var err error
		handoffID, err = sessionmove.NewHandoffID()
		if err != nil {
			return result, err
		}
	}
	successorID := strings.TrimSpace(options.SuccessorWBSessionID)
	if successorID == "" {
		var err error
		successorID, err = session.NewID()
		if err != nil {
			return result, err
		}
	}

	preflight, err := preflightSessionCheckpoint(ctx, options, handoffID, successorID)
	if err != nil {
		return result, err
	}
	defer preflight.close()

	// The handover document is never written into the repo under work: it
	// travels inline on Request.HandoverContent instead, admitted straight
	// into WB's own private handoff store below. See
	// sessionmove.Store.EnsureHandoverUnderLock for how the target later
	// materializes it as a private file for the successor.
	document := renderSessionHandover(options, preflight, handoffID, successorID)
	handoverDigest := sessionmove.DigestBytes(document)
	offerMessage, offerNextAction := sessionmove.NormalizeSourceOfferContent(
		normalizedHandoverSummary(options.Handover), normalizedRemainingWork(options.Handover),
	)
	request := sessionmove.Request{
		SchemaVersion:          sessionmove.RequestSchemaVersion,
		HandoffID:              handoffID,
		SuccessorWBSessionID:   successorID,
		PredecessorWBSessionID: options.SourceSession.WBSessionID,
		SourceMachine:          options.SourceSession.Machine,
		TargetMachine:          options.TargetMachine,
		RepositoryRemote:       preflight.repositoryRemote,
		Branch:                 preflight.branch,
		SourceWorkCommit:       preflight.sourceCommit,
		// No commit is added on top of the source's own work anymore, so the
		// exact commit the successor pins is simply the source commit itself.
		BundleCommit:          preflight.sourceCommit,
		HandoverContent:       string(document),
		HandoverDigest:        handoverDigest,
		SourceRuntime:         options.SourceSession.Runtime,
		SourceModel:           options.SourceSession.Model,
		SourceNativeHarnessID: sourceNativeHarnessID(options.SourceSession),
		RequestedHarness:      options.RequestedHarness,
		WorkLogReference:      preflight.workLogReference,
		SourceOfferMessage:    offerMessage,
		SourceOfferNextAction: offerNextAction,
		SourceOfferDigest:     sessionmove.DigestSourceOffer(offerMessage, offerNextAction),
		CreatedAt:             options.Now,
	}
	// Validate every request-carried value while the operation is still
	// read-only.
	if _, err := sessionmove.EncodeRequest(request); err != nil {
		return result, fmt.Errorf("validate session checkpoint before mutation: %w", err)
	}
	if err := verifySessionCheckpointUnchanged(ctx, preflight); err != nil {
		return result, err
	}
	requestRaw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		return result, fmt.Errorf("encode exact session move request: %w", err)
	}
	digest := sessionmove.DigestBytes(requestRaw)

	// preflight's dry run already proved this exact HEAD (== request.BundleCommit)
	// can be pushed without force, and nothing local has mutated it since.
	// Re-authenticate both descriptor-read origin routes before allowing the
	// first network mutation; a hook must not redirect publication after the
	// zero-mutation preflight.
	if err := verifySessionCheckpointRemotesUnchanged(ctx, preflight); err != nil {
		return result, fmt.Errorf("origin remote changed after checkpoint validation; refusing publication: %w", err)
	}
	if options.afterPushRemoteAuthentication != nil {
		options.afterPushRemoteAuthentication()
	}
	if err := runSessionPushGit(ctx, preflight, false); err != nil {
		return result, fmt.Errorf("push exact source commit without force: %w", err)
	}
	if err := verifySessionCheckpointRemotesUnchanged(ctx, preflight); err != nil {
		return result, fmt.Errorf("origin remote changed while publishing source commit: %w", err)
	}
	remoteTip, err := sessionRemoteBranchTip(ctx, preflight, preflight.branch)
	if err != nil {
		return result, fmt.Errorf("verify exact remote branch tip after push: %w", err)
	}
	if remoteTip != request.BundleCommit {
		return result, fmt.Errorf("remote branch %s tip is %q after push, want exact source commit %s", preflight.branch, remoteTip, request.BundleCommit)
	}

	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	store := sessionmove.NewStore(filepath.Join(home, sessionmove.DirName))
	admission, err := store.Admit(requestRaw, digest)
	if err != nil {
		return result, fmt.Errorf("persist exact session move request: %w", err)
	}
	if admission.Replay {
		return result, fmt.Errorf("new source checkpoint unexpectedly replayed handoff %s", handoffID)
	}
	// From this durability boundary onward every handled error must carry the
	// exact admitted identity so the CLI can direct the operator to --resume.
	// Work Log offer/owner evidence is deliberately filled only after repair.
	result = SessionCheckpointResult{
		Request: request, Digest: digest,
		RequestBytes: append([]byte(nil), requestRaw...), HandoverBytes: append([]byte(nil), document...),
	}
	if options.afterAdmission != nil {
		if err := options.afterAdmission(); err != nil {
			return result, err
		}
	}
	lock, err := store.AcquireExecutionLock(ctx, handoffID, digest)
	if err != nil {
		return result, fmt.Errorf("retain exact source checkpoint aggregate: %w", err)
	}
	defer func() { _ = lock.Close() }()
	offerEvidence, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: store, ExecutionLock: lock, ProjectsRoot: options.ProjectsRoot,
		Request: request, RequestDigest: digest, SourceSession: options.SourceSession,
	})
	if err != nil {
		return result, fmt.Errorf("record exact source Work Log offer evidence: %w", err)
	}

	result.WorkLogEvent = offerEvidence.OfferEvent
	return result, nil
}

func validateSessionHandover(handover SessionHandover) error {
	if strings.TrimSpace(handover.Summary) == "" && strings.TrimSpace(handover.ValidationEvidence) == "" &&
		strings.TrimSpace(handover.RemainingWork) == "" && len(bytes.TrimSpace(handover.Body)) == 0 {
		return fmt.Errorf("handover must not be empty")
	}
	return nil
}

func validateSourceSession(source session.Record) error {
	if source.PID <= 0 || strings.TrimSpace(source.WBSessionID) == "" {
		return fmt.Errorf("a live registered source session with a stable WB session ID is required")
	}
	if strings.TrimSpace(source.Machine) == "" || strings.TrimSpace(source.Runtime) == "" || source.StartedAt.IsZero() {
		return fmt.Errorf("registered source session is missing machine, runtime, or start identity")
	}
	return nil
}

func preflightSessionCheckpoint(ctx context.Context, options SessionCheckpointOptions, handoffID, successorID string) (*sessionCheckpointPreflight, error) {
	root, err := RepositoryRootFor(ctx, options.Worktree)
	if err != nil {
		return nil, err
	}
	branch, err := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("session move requires a named branch; detached HEAD is not movable")
	}
	branch = strings.TrimSpace(branch)
	status, err := git(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("inspect source worktree status: %w", err)
	}
	if status != "" {
		return nil, fmt.Errorf("source worktree is dirty; commit or remove all changes before moving the session: %s", status)
	}
	head, err := git(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !isGitObjectID(head) {
		return nil, fmt.Errorf("resolve exact source work commit: %w", err)
	}
	guard, err := Guard(ctx, root, GuardOptions{ProjectsRoot: options.ProjectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		return nil, fmt.Errorf("session move requires a managed Git worktree: %w", err)
	}
	if guard.Branch != branch {
		return nil, fmt.Errorf("managed worktree branch %q does not match live named branch %q", guard.Branch, branch)
	}

	claim, reference, err := inspectSessionMoveWorkLog(options.ProjectsRoot, root, options.SourceSession)
	if err != nil {
		return nil, err
	}
	if claim.Branch != branch {
		return nil, fmt.Errorf("active Work Log branch %q does not match source branch %q", claim.Branch, branch)
	}

	canonical, err := openCanonicalRepository(guard.CanonicalDir)
	if err != nil {
		return nil, fmt.Errorf("open managed canonical repository: %w", err)
	}
	worktree, err := openAdoptedCleanupWorktree(root)
	if err != nil {
		canonical.close()
		return nil, fmt.Errorf("hold source worktree for checkpoint: %w", err)
	}
	fetchRemote, err := readCanonicalOriginRemote(ctx, canonical, false)
	if err != nil {
		worktree.close()
		canonical.close()
		return nil, fmt.Errorf("source branch has no usable origin fetch remote: %w", err)
	}
	pushRemote, err := readCanonicalOriginRemote(ctx, canonical, true)
	if err != nil {
		worktree.close()
		canonical.close()
		return nil, fmt.Errorf("source branch has no usable origin push remote: %w", err)
	}
	parsedFetch, err := gitremote.Parse(fetchRemote)
	if err != nil {
		worktree.close()
		canonical.close()
		return nil, fmt.Errorf("origin fetch remote is unsafe: %w", err)
	}
	parsedPush, err := gitremote.Parse(pushRemote)
	if err != nil {
		worktree.close()
		canonical.close()
		return nil, fmt.Errorf("origin push remote is unsafe: %w", err)
	}
	if !parsedFetch.Identity.Equal(parsedPush.Identity) {
		worktree.close()
		canonical.close()
		return nil, fmt.Errorf("origin fetch and push remotes identify different repositories")
	}
	preflight := &sessionCheckpointPreflight{
		root: root, branch: branch, sourceCommit: head, repositoryRemote: parsedFetch.Raw, pushRemote: parsedPush.Raw,
		workLogReference: reference, canonicalDir: guard.CanonicalDir, worktreesRoot: guard.WorktreesRoot,
		canonical: canonical, worktree: worktree,
	}

	// The dry run asks the actual configured push route whether this exact
	// named branch can advance without force. No generated file or local ref
	// exists yet, so a rejection is a zero-mutation refusal.
	if err := runSessionPushGit(ctx, preflight, true); err != nil {
		preflight.close()
		return nil, fmt.Errorf("source branch %s cannot be pushed without force: %w", branch, err)
	}
	if err := verifySessionCheckpointUnchanged(ctx, preflight); err != nil {
		preflight.close()
		return nil, err
	}
	// Validate IDs and request-owned identity before mutation using the same
	// protocol encoder as the final request.
	probe := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion,
		HandoffID:     handoffID, SuccessorWBSessionID: successorID,
		PredecessorWBSessionID: options.SourceSession.WBSessionID,
		SourceMachine:          options.SourceSession.Machine, TargetMachine: options.TargetMachine,
		RepositoryRemote: parsedFetch.Raw, Branch: branch,
		SourceWorkCommit: head, BundleCommit: head,
		HandoverContent: "probe", HandoverDigest: sessionmove.DigestBytes([]byte("probe")),
		SourceRuntime:      options.SourceSession.Runtime,
		WorkLogReference:   preflight.workLogReference,
		SourceOfferMessage: "Session handoff offered", SourceOfferNextAction: "Continue from the retained WB session handover for this handoff.",
		CreatedAt: options.Now,
	}
	probe.SourceOfferDigest = sessionmove.DigestSourceOffer(probe.SourceOfferMessage, probe.SourceOfferNextAction)
	if _, err := sessionmove.EncodeRequest(probe); err != nil {
		preflight.close()
		return nil, fmt.Errorf("validate source checkpoint identity: %w", err)
	}
	return preflight, nil
}

func inspectSessionMoveWorkLog(projectsRoot, worktree string, source session.Record) (workLogClaim, string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return workLogClaim{}, "", err
	}
	projection, err := readWorkLogProjection(worktree)
	if errors.Is(err, os.ErrNotExist) {
		return workLogClaim{}, "", fmt.Errorf("session move requires an active managed Work Log")
	}
	if err != nil {
		return workLogClaim{}, "", fmt.Errorf("inspect managed Work Log projection: %w", err)
	}
	if projection.Lifecycle != "active" {
		return workLogClaim{}, "", fmt.Errorf("managed Work Log is %s, not active", projection.Lifecycle)
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
		return workLogClaim{}, "", fmt.Errorf("corroborate managed Work Log: %w", err)
	}
	runDir, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return workLogClaim{}, "", err
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", false)
	if err != nil {
		return workLogClaim{}, "", err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return workLogClaim{}, "", err
	}
	owners, err := ownerViews(worktree)
	if err != nil {
		return workLogClaim{}, "", fmt.Errorf("inspect Work Log owners: %w", err)
	}
	var owner *OwnerView
	for index := len(owners) - 1; index >= 0; index-- {
		if owners[index].PID > 0 {
			owner = &owners[index]
			break
		}
	}
	if owner == nil || owner.PID != source.PID || owner.PIDStatus != "active" || owner.At.Before(source.StartedAt) {
		return workLogClaim{}, "", fmt.Errorf("registered source session %s (PID %d) does not own the active Work Log", source.WBSessionID, source.PID)
	}
	reference := fmt.Sprintf("worklog:%s/%s/%s", projection.EffortID, projection.RunID, projection.ClaimID)
	return claim, reference, nil
}

// ParkedSessionWorkLogReference returns the exact active Work Log claim only
// when it is owned by source. Session parking uses this at its immutable
// snapshot boundary; it must never adopt another session's latest owner.
func ParkedSessionWorkLogReference(projectsRoot, worktree string, source session.Record) (string, error) {
	_, reference, err := inspectSessionMoveWorkLog(projectsRoot, worktree, source)
	return reference, err
}

// ParkedSessionWorkLogSnapshot returns both the canonical source claim and the
// exact latest owner event that proved source custody at park time. Local
// resume later compares the event ID while holding every member journal lock,
// so a sequential newer session cannot be silently overwritten.
func ParkedSessionWorkLogSnapshot(projectsRoot, worktree string, source session.Record) (string, string, error) {
	_, reference, err := inspectSessionMoveWorkLog(projectsRoot, worktree, source)
	if err != nil {
		return "", "", err
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		return "", "", fmt.Errorf("inspect exact parked owner event: %w", err)
	}
	ownerID := ""
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil {
			if event.Owner.PID != source.PID {
				ownerID = ""
				continue
			}
			ownerID = event.ID
		}
	}
	if ownerID == "" {
		return "", "", fmt.Errorf("registered source session %s is not the exact latest parked owner", source.WBSessionID)
	}
	return reference, ownerID, nil
}

func verifySessionCheckpointUnchanged(ctx context.Context, preflight *sessionCheckpointPreflight) error {
	if err := preflight.canonical.validate(); err != nil {
		return fmt.Errorf("canonical repository changed during source preflight: %w", err)
	}
	if err := preflight.worktree.validate(); err != nil {
		return fmt.Errorf("source worktree changed during preflight: %w", err)
	}
	branch, err := git(ctx, preflight.root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != preflight.branch {
		return fmt.Errorf("source named branch changed during preflight")
	}
	head, err := git(ctx, preflight.root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != preflight.sourceCommit {
		return fmt.Errorf("source HEAD changed during preflight")
	}
	status, err := git(ctx, preflight.root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return fmt.Errorf("source worktree changed during preflight")
	}
	return verifySessionCheckpointRemotesUnchanged(ctx, preflight)
}

func verifySessionCheckpointRemotesUnchanged(ctx context.Context, preflight *sessionCheckpointPreflight) error {
	fetchRemote, err := readCanonicalOriginRemote(ctx, preflight.canonical, false)
	if err != nil || fetchRemote != preflight.repositoryRemote {
		return fmt.Errorf("origin fetch remote changed during preflight")
	}
	pushRemote, err := readCanonicalOriginRemote(ctx, preflight.canonical, true)
	if err != nil || pushRemote != preflight.pushRemote {
		return fmt.Errorf("origin push remote changed during preflight")
	}
	return nil
}

func readCanonicalOriginRemote(ctx context.Context, canonical *canonicalRepository, push bool) (string, error) {
	arguments := []string{"remote", "get-url", "--all"}
	if push {
		arguments = append(arguments, "--push")
	}
	arguments = append(arguments, "origin")
	raw, err := gitCanonicalBytes(ctx, canonical, arguments...)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return "", fmt.Errorf("origin remote output was not one terminated value")
	}
	value := string(raw[:len(raw)-1])
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("origin remote output was not one safe value")
	}
	return value, nil
}

func runSessionCanonicalGit(ctx context.Context, preflight *sessionCheckpointPreflight, args ...string) error {
	return runSecureCleanupGitHelper(ctx, preflight.canonical, preflight.worktree.parent, preflight.worktree.worktree,
		preflight.worktree.parentPath, preflight.worktree.worktreePath, args...)
}

func runSessionWorktreeGit(ctx context.Context, preflight *sessionCheckpointPreflight, args ...string) error {
	return runSecureRenameGitWithHeldWorktree(ctx, preflight.canonicalDir, preflight.worktreesRoot,
		preflight.root, preflight.worktree.worktree, args...)
}

func runSessionPushGit(ctx context.Context, preflight *sessionCheckpointPreflight, dryRun bool) error {
	arguments := []string{"push"}
	if dryRun {
		arguments = append(arguments, "--dry-run")
	}
	arguments = append(arguments, "--porcelain", "--", preflight.pushRemote,
		"refs/heads/"+preflight.branch+":refs/heads/"+preflight.branch)
	return runSessionCanonicalGit(ctx, preflight, arguments...)
}

func sessionRemoteBranchTip(ctx context.Context, preflight *sessionCheckpointPreflight, branch string) (string, error) {
	output, err := gitCanonical(ctx, preflight.canonical, "ls-remote", "--heads", "--", preflight.repositoryRemote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !isGitObjectID(fields[0]) {
		return "", fmt.Errorf("unexpected remote branch response %q", output)
	}
	return fields[0], nil
}

func renderSessionHandover(options SessionCheckpointOptions, preflight *sessionCheckpointPreflight, handoffID, successorID string) []byte {
	source := options.SourceSession
	var out strings.Builder
	out.WriteString("# WB session handover\n\n")
	out.WriteString("This immutable handover was generated by WB from deliberately supplied source-agent context.\n\n")
	out.WriteString("## Identity and checkpoint\n\n")
	fields := [][2]string{
		{"Schema version", strconv.Itoa(sessionHandoverSchemaVersion)},
		{"Handoff ID", handoffID}, {"Successor WB session ID", successorID},
		{"Predecessor WB session ID", source.WBSessionID},
		{"Source machine", source.Machine}, {"Target machine", options.TargetMachine},
		{"Source runtime", source.Runtime}, {"Source model", source.Model},
		{"Source native harness ID", sourceNativeHarnessID(source)},
		{"Repository remote", preflight.repositoryRemote}, {"Branch", preflight.branch},
		{"Source work commit", preflight.sourceCommit},
		{"Requested harness", options.RequestedHarness},
		{"Work Log reference", preflight.workLogReference},
		{"Created at", options.Now.Format(time.RFC3339Nano)},
	}
	for _, field := range fields {
		_, _ = fmt.Fprintf(&out, "- %s: %s\n", field[0], strconv.Quote(field[1]))
	}
	writeHandoverSection(&out, "Summary", options.Handover.Summary)
	writeHandoverSection(&out, "Validation evidence", options.Handover.ValidationEvidence)
	writeHandoverSection(&out, "Remaining work", options.Handover.RemainingWork)
	writeHandoverSection(&out, "Agent-authored handover", string(options.Handover.Body))
	return []byte(out.String())
}

func writeHandoverSection(out *strings.Builder, heading, body string) {
	out.WriteString("\n## ")
	out.WriteString(heading)
	out.WriteString("\n\n")
	body = strings.TrimSpace(body)
	if body == "" {
		out.WriteString("_Not supplied._\n")
		return
	}
	out.WriteString(body)
	out.WriteByte('\n')
}

func normalizedHandoverSummary(handover SessionHandover) string {
	if summary := strings.TrimSpace(handover.Summary); summary != "" {
		return summary
	}
	if body := strings.TrimSpace(string(handover.Body)); body != "" {
		if line, _, found := strings.Cut(body, "\n"); found {
			return strings.TrimSpace(line)
		}
		return body
	}
	return "Session handoff offered"
}

func normalizedRemainingWork(handover SessionHandover) string {
	if remaining := strings.TrimSpace(handover.RemainingWork); remaining != "" {
		return remaining
	}
	return "Continue from the retained WB session handover for this handoff."
}

func sourceNativeHarnessID(source session.Record) string {
	if value := strings.TrimSpace(source.NativeHarnessID); value != "" {
		return value
	}
	return strings.TrimSpace(source.AgentID)
}
