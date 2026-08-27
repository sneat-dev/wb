package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// LogVerbResult is the public receipt returned by mutating log verbs.
type LogVerbResult struct {
	Worktree   string                  `json:"worktree"`
	Verb       string                  `json:"verb"`
	Event      *LocalWorkLogEvent      `json:"event,omitempty"`
	Projection *LocalWorkLogProjection `json:"projection,omitempty"`
	Prompt     string                  `json:"prompt,omitempty"`
	Applied    bool                    `json:"applied"`
	Offline    bool                    `json:"offline,omitempty"`
	Outbox     int                     `json:"outbox,omitempty"`
	Notes      []string                `json:"notes,omitempty"`
	Diagnosis  []string                `json:"diagnosis,omitempty"`
}

type claimFence struct {
	unlock func()
	home   string
	claim  workLogClaim
}

func resolveWorktreeRoot(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	return RepositoryRootFor(ctx, path)
}

func withOptionalClaimFence(projectsRoot, worktree string, require bool) (claimFence, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return claimFence{}, err
	}
	claim, projection, _, err := activeWorkLogClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) || (err == nil && projection.Lifecycle == "terminal") {
		if require {
			return claimFence{}, fmt.Errorf("active Work Log claim required; run wb worktree log init or create first")
		}
		return claimFence{home: home}, nil
	}
	if err != nil {
		return claimFence{}, err
	}
	runDir, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return claimFence{}, err
	}
	unlockOuter := func() { _ = runDir.Close() }
	lock, err := lockClaim(runDir, claim.ClaimID)
	if err != nil {
		unlockOuter()
		return claimFence{}, err
	}
	return claimFence{
		home:  home,
		claim: claim,
		unlock: func() {
			lock()
			unlockOuter()
		},
	}, nil
}

func observeUsage(discriminator string, input, output *int64, cost *float64, currency, providerRef string) (*LocalUsageEvidence, error) {
	discriminator = strings.TrimSpace(discriminator)
	if discriminator == "" {
		if input == nil && output == nil && cost == nil && currency == "" && providerRef == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("--usage-discriminator is required when usage fields are supplied")
	}
	switch discriminator {
	case "provider_reported", "estimated", "unavailable":
	default:
		return nil, fmt.Errorf("usage discriminator must be provider_reported, estimated, or unavailable")
	}
	usage := &LocalUsageEvidence{
		Discriminator: discriminator,
		InputTokens:   input,
		OutputTokens:  output,
		EstimatedCost: cost,
		Currency:      strings.TrimSpace(currency),
		ProviderRef:   strings.TrimSpace(providerRef),
	}
	if input != nil || output != nil {
		total := int64(0)
		if input != nil {
			total += *input
		}
		if output != nil {
			total += *output
		}
		usage.TotalTokens = &total
	}
	return usage, nil
}

// LogInitOptions configures wb worktree log init.
type LogInitOptions struct {
	ProjectsRoot string
	Worktree     string
	Prompt       []byte
	Source       string
	Model        string
	CLI          string
	Provider     string
	Runtime      string
	AgentID      string
}

// LogInit ensures the local journal exists, reconstructs a missing manifest,
// records prompt 0000 when supplied and absent, and appends an init event.
func LogInit(ctx context.Context, options LogInitOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	fence, err := withOptionalClaimFence(options.ProjectsRoot, root, false)
	if err != nil {
		return LogVerbResult{}, err
	}
	if fence.unlock != nil {
		defer fence.unlock()
	}

	manifest, err := ReconstructManifest(ctx, root)
	if err != nil {
		return LogVerbResult{}, err
	}
	notes := []string{}
	if manifest.Provenance == ProvenanceReconstructed {
		notes = append(notes, fmt.Sprintf("reconstructed manifest for effort %s", manifest.EffortID))
	}
	if _, err := openLocalWorkLogDir(root, true); err != nil {
		return LogVerbResult{}, err
	}

	promptName := ""
	source := strings.TrimSpace(options.Source)
	if len(options.Prompt) > 0 {
		if source == "" {
			source = PromptSourceHuman
		}
		existing, err := ListPrompts(root)
		if err != nil {
			return LogVerbResult{}, err
		}
		if len(existing) == 0 {
			promptName, err = AppendPrompt(root, PromptHeader{
				Source:   source,
				Runtime:  options.Runtime,
				Model:    options.Model,
				CLI:      options.CLI,
				Provider: options.Provider,
				Slug:     "initial",
			}, options.Prompt)
			if err != nil {
				return LogVerbResult{}, err
			}
		} else {
			notes = append(notes, "prompt sequence already present; left unchanged")
		}
	}

	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type:    LocalEventInit,
		Message: "work log initialized",
		Prompt:  promptName,
		Git:     ptrLocalGit(observeLocalGit(ctx, root)),
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	if _, err := recordOwner(root, manifest.EffortID, ownerAgent(options.Runtime, options.AgentID), strings.TrimSpace(options.Model), CurrentIdentity().PID); err != nil {
		return LogVerbResult{}, err
	}
	return LogVerbResult{
		Worktree: root, Verb: "init", Event: &event, Projection: &projection,
		Prompt: promptName, Applied: true, Notes: notes,
	}, nil
}

// LogSteerOptions configures wb worktree log steer.
type LogSteerOptions struct {
	ProjectsRoot string
	Worktree     string
	Body         []byte
	Source       string
	Runtime      string
	Model        string
	CLI          string
	Provider     string
}

// LogSteer appends the next prompt ordinal and a steer journal event.
func LogSteer(ctx context.Context, options LogSteerOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	source := strings.TrimSpace(options.Source)
	if source == "" {
		source = PromptSourceAgent
	}
	if _, err := ReconstructManifest(ctx, root); err != nil {
		return LogVerbResult{}, err
	}
	name, err := AppendPrompt(root, PromptHeader{
		Source:   source,
		Runtime:  options.Runtime,
		Model:    options.Model,
		CLI:      options.CLI,
		Provider: options.Provider,
	}, options.Body)
	if err != nil {
		return LogVerbResult{}, err
	}
	digest := sha256.Sum256(options.Body)
	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type:      LocalEventSteer,
		Message:   "steering prompt recorded",
		Prompt:    name,
		PromptSHA: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	return LogVerbResult{
		Worktree: root, Verb: "steer", Event: &event, Projection: &projection,
		Prompt: name, Applied: true,
	}, nil
}

// LogShow returns the redacted work-log view (no prompt bodies).
func LogShow(ctx context.Context, projectsRoot, worktree string) (WorkLogView, LocalWorkLogProjection, error) {
	root, err := resolveWorktreeRoot(ctx, worktree)
	if err != nil {
		return WorkLogView{}, LocalWorkLogProjection{}, err
	}
	view, err := LoadWorkLogView(ctx, LoadWorkLogOptions{
		ProjectsRoot: projectsRoot, Worktree: root, IncludePromptBodies: false,
	})
	if err != nil {
		return WorkLogView{}, LocalWorkLogProjection{}, err
	}
	projection, err := readLocalProjection(root)
	if errors.Is(err, os.ErrNotExist) {
		return view, LocalWorkLogProjection{}, nil
	}
	return view, projection, err
}

// LogCheckpointOptions configures wb worktree log checkpoint.
type LogCheckpointOptions struct {
	ProjectsRoot  string
	Worktree      string
	Message       string
	NextAction    string
	UsageDisc     string
	InputTokens   *int64
	OutputTokens  *int64
	EstimatedCost *float64
	Currency      string
	ProviderRef   string
}

// LogCheckpoint appends a typed checkpoint with observed Git evidence.
func LogCheckpoint(ctx context.Context, options LogCheckpointOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	fence, err := withOptionalClaimFence(options.ProjectsRoot, root, true)
	if err != nil {
		return LogVerbResult{}, err
	}
	if fence.unlock != nil {
		defer fence.unlock()
	}
	usage, err := observeUsage(options.UsageDisc, options.InputTokens, options.OutputTokens, options.EstimatedCost, options.Currency, options.ProviderRef)
	if err != nil {
		return LogVerbResult{}, err
	}
	gitEvidence := observeLocalGit(ctx, root)
	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type:       LocalEventCheckpoint,
		Message:    strings.TrimSpace(options.Message),
		NextAction: strings.TrimSpace(options.NextAction),
		Git:        &gitEvidence,
		Usage:      usage,
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	return LogVerbResult{Worktree: root, Verb: "checkpoint", Event: &event, Projection: &projection, Applied: true}, nil
}

// LogRefreshOptions configures wb worktree log refresh.
type LogRefreshOptions struct {
	ProjectsRoot string
	Worktree     string
	Base         string
}

// LogRefresh fetches the target ref and measures divergence without mutating the worktree.
func LogRefresh(ctx context.Context, options LogRefreshOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	base := strings.TrimSpace(options.Base)
	if base == "" {
		if manifest, err := ReadManifest(root); err == nil && strings.TrimSpace(manifest.Base) != "" {
			base = manifest.Base
		} else {
			base = "main"
		}
	}
	gitEvidence := observeLocalGit(ctx, root)
	targetSHA, err := fetchRemoteTargetHead(ctx, root, base)
	notes := []string{}
	eventType := LocalEventRefresh
	conflict := ""
	target := &LocalTargetEvidence{Ref: "origin/" + base, FetchedAt: time.Now().UTC()}
	if err != nil {
		eventType = LocalEventRefreshNeed
		notes = append(notes, err.Error())
		conflict = "fetch_failed"
	} else {
		target.SHA = targetSHA
		ahead, behind, countErr := aheadBehind(ctx, root, targetSHA)
		if countErr != nil {
			notes = append(notes, countErr.Error())
		} else {
			target.Ahead, target.Behind = ahead, behind
		}
		if gitEvidence.Dirty {
			eventType = LocalEventRefreshNeed
			notes = append(notes, "worktree is dirty; refresh measured without changing Git state")
		}
	}
	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type:     eventType,
		Message:  "target refresh observed",
		Git:      &gitEvidence,
		Target:   target,
		Conflict: conflict,
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	return LogVerbResult{Worktree: root, Verb: "refresh", Event: &event, Projection: &projection, Applied: true, Notes: notes}, nil
}

func aheadBehind(ctx context.Context, worktree, targetSHA string) (int, int, error) {
	out, err := git(ctx, worktree, "rev-list", "--left-right", "--count", "HEAD..."+targetSHA)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected ahead/behind output %q", out)
	}
	ahead, err1 := strconv.Atoi(fields[0])
	behind, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("parse ahead/behind %q", out)
	}
	return ahead, behind, nil
}

// LogIntegrateOptions configures wb worktree log integrate.
type LogIntegrateOptions struct {
	ProjectsRoot string
	Worktree     string
	Base         string
	Strategy     string // rebase|merge|auto
}

// LogIntegrate integrates the fetched target at a clean checkpoint.
func LogIntegrate(ctx context.Context, options LogIntegrateOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	fence, err := withOptionalClaimFence(options.ProjectsRoot, root, true)
	if err != nil {
		return LogVerbResult{}, err
	}
	if fence.unlock != nil {
		defer fence.unlock()
	}
	gitEvidence := observeLocalGit(ctx, root)
	if gitEvidence.Dirty {
		return LogVerbResult{}, fmt.Errorf("integrate requires a clean worktree; checkpoint and resolve dirty state first")
	}
	projection, err := readLocalProjection(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return LogVerbResult{}, err
	}
	if projection.LastCheckpoint == nil || projection.LastCheckpoint.Dirty {
		return LogVerbResult{}, fmt.Errorf("integrate requires a prior clean checkpoint")
	}
	if projection.Conflict != "" && projection.Conflict != "resolved" {
		return LogVerbResult{}, fmt.Errorf("integrate blocked by unresolved conflict %q", projection.Conflict)
	}

	base := strings.TrimSpace(options.Base)
	if base == "" {
		if manifest, err := ReadManifest(root); err == nil && strings.TrimSpace(manifest.Base) != "" {
			base = manifest.Base
		} else {
			base = "main"
		}
	}
	targetSHA, err := fetchRemoteTargetHead(ctx, root, base)
	if err != nil {
		return LogVerbResult{}, err
	}
	strategy := strings.TrimSpace(options.Strategy)
	if strategy == "" || strategy == "auto" {
		strategy = "rebase"
		if published, _ := branchPublished(ctx, root); published {
			strategy = "merge"
		}
	}
	var integrateErr error
	switch strategy {
	case "rebase":
		_, integrateErr = git(ctx, root, "rebase", targetSHA)
	case "merge":
		_, integrateErr = git(ctx, root, "merge", "--no-edit", targetSHA)
	default:
		return LogVerbResult{}, fmt.Errorf("strategy must be rebase, merge, or auto")
	}
	after := observeLocalGit(ctx, root)
	target := &LocalTargetEvidence{
		Ref: "origin/" + base, SHA: targetSHA, FetchedAt: time.Now().UTC(), Strategy: strategy,
	}
	if ahead, behind, countErr := aheadBehind(ctx, root, targetSHA); countErr == nil {
		target.Ahead, target.Behind = ahead, behind
	}
	conflict := ""
	result := "ok"
	message := "integrated target into claim"
	if integrateErr != nil {
		conflict = "integrate_conflict"
		result = "conflict"
		message = integrateErr.Error()
		_, _ = git(ctx, root, "rebase", "--abort")
		_, _ = git(ctx, root, "merge", "--abort")
		after = observeLocalGit(ctx, root)
	}
	event, localProjection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type: LocalEventIntegrate, Message: message, Git: &after, Target: target,
		Result: result, Conflict: conflict,
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	notes := []string{}
	if integrateErr != nil {
		notes = append(notes, integrateErr.Error())
	}
	return LogVerbResult{
		Worktree: root, Verb: "integrate", Event: &event, Projection: &localProjection,
		Applied: integrateErr == nil, Notes: notes,
	}, nil
}

func branchPublished(ctx context.Context, worktree string) (bool, error) {
	branch, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return false, err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false, nil
	}
	_, err = git(ctx, worktree, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	return err == nil, nil
}

// LogHandoffOptions configures wb worktree log handoff.
type LogHandoffOptions struct {
	ProjectsRoot  string
	Worktree      string
	HandoffID     string
	TargetMachine string
	BundleCommit  string
	Summary       string
	NextAction    string
	Successor     string
	Model         string
	CLI           string
	Provider      string
	Apply         bool
	// EventID/At and lineage fields are optional for ordinary manual verbs.
	// Session checkpoint supplies them to make its offer immutable evidence
	// derivable from the exact request digest.
	EventID                string
	At                     time.Time
	RequestDigest          sessionmove.Digest
	SourceWorkLogReference string
	PredecessorWBSessionID string
	SourceMachine          string
}

// LogHandoff records a durable handoff offer and optionally transfers the Hybrid claim.
func LogHandoff(ctx context.Context, options LogHandoffOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	if strings.TrimSpace(options.Summary) == "" {
		return LogVerbResult{}, fmt.Errorf("--summary is required for handoff")
	}
	if strings.TrimSpace(options.Successor) == "" {
		return LogVerbResult{}, fmt.Errorf("--successor is required for handoff")
	}
	fence, err := withOptionalClaimFence(options.ProjectsRoot, root, true)
	if err != nil {
		return LogVerbResult{}, err
	}
	home := fence.home
	gitEvidence := observeLocalGit(ctx, root)
	extra := map[string]any{
		"successor": strings.TrimSpace(options.Successor),
		"apply":     options.Apply,
	}
	if value := strings.TrimSpace(options.HandoffID); value != "" {
		extra["handoff_id"] = value
	}
	if value := strings.TrimSpace(options.TargetMachine); value != "" {
		extra["target_machine"] = value
	}
	if value := strings.TrimSpace(options.BundleCommit); value != "" {
		extra["bundle_commit"] = value
	}
	if options.RequestDigest != "" {
		extra["request_digest"] = string(options.RequestDigest)
	}
	if value := strings.TrimSpace(options.SourceWorkLogReference); value != "" {
		extra["source_work_log_reference"] = value
	}
	if value := strings.TrimSpace(options.PredecessorWBSessionID); value != "" {
		extra["predecessor_wb_session_id"] = value
	}
	if value := strings.TrimSpace(options.SourceMachine); value != "" {
		extra["source_machine"] = value
	}
	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		ID: strings.TrimSpace(options.EventID), At: options.At,
		Type: LocalEventHandoff, Message: strings.TrimSpace(options.Summary),
		NextAction: strings.TrimSpace(options.NextAction), Git: &gitEvidence,
		Result: "offered", Extra: extra,
	})
	if fence.unlock != nil {
		fence.unlock()
		fence.unlock = nil
	}
	if err != nil {
		return LogVerbResult{}, err
	}
	notes := []string{"handoff offer recorded in local journal"}
	applied := false
	if options.Apply {
		identity := ClaimExecutionIdentity{Model: options.Model, CLI: options.CLI, Provider: options.Provider}
		if err := transferWorkLogClaim(home, root, gitEvidence.Head, string(AbortHandoff), options.Successor, identity); err != nil {
			return LogVerbResult{}, err
		}
		applied = true
		notes = append(notes, "hybrid claim transferred to successor")
	} else {
		notes = append(notes, "pass --apply to transfer the hybrid claim")
	}
	return LogVerbResult{
		Worktree: root, Verb: "handoff", Event: &event, Projection: &projection,
		Applied: applied, Notes: notes,
	}, nil
}

// LogRecoverOptions configures wb worktree log recover.
type LogRecoverOptions struct {
	ProjectsRoot string
	Worktree     string
	Apply        bool
	Takeover     bool
	Actor        string
}

// LogRecover rebuilds derived state and diagnoses claim/journal disagreement.
func LogRecover(ctx context.Context, options LogRecoverOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	diagnosis := []string{}
	events, err := readLocalEvents(root)
	if err != nil {
		diagnosis = append(diagnosis, "local events: "+err.Error())
	} else {
		diagnosis = append(diagnosis, fmt.Sprintf("local events: %d", len(events)))
	}
	projection, projErr := rebuildLocalProjection(events)
	if projErr != nil {
		diagnosis = append(diagnosis, "projection rebuild: "+projErr.Error())
	}
	if manifest, err := ReadManifest(root); err != nil {
		diagnosis = append(diagnosis, "manifest: "+err.Error())
	} else {
		diagnosis = append(diagnosis, "manifest effort "+manifest.EffortID)
		projection.EffortID = manifest.EffortID
		projection.RunID = manifest.RunID
		projection.ClaimID = manifest.ClaimID
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return LogVerbResult{}, err
	}
	if claim, hybrid, _, err := activeWorkLogClaim(home, root); err != nil {
		diagnosis = append(diagnosis, "hybrid claim: "+err.Error())
	} else {
		diagnosis = append(diagnosis, "hybrid claim "+claim.ClaimID+" lifecycle="+hybrid.Lifecycle)
		projection.EffortID = claim.EffortID
		projection.RunID = claim.RunID
		projection.ClaimID = claim.ClaimID
		projection.Lifecycle = hybrid.Lifecycle
	}
	gitEvidence := observeLocalGit(ctx, root)
	diagnosis = append(diagnosis, fmt.Sprintf("git head=%s dirty=%t", gitEvidence.Head, gitEvidence.Dirty))

	result := LogVerbResult{
		Worktree: root, Verb: "recover", Diagnosis: diagnosis, Projection: &projection,
	}
	if !options.Apply {
		result.Notes = []string{"dry-run only; pass --apply to rewrite projection.json", "pass --takeover with --apply to record an explicit takeover event"}
		return result, nil
	}
	fence, err := withOptionalClaimFence(options.ProjectsRoot, root, false)
	if err != nil {
		return LogVerbResult{}, err
	}
	if fence.unlock != nil {
		defer fence.unlock()
	}
	directory, err := openLocalWorkLogDir(root, true)
	if err != nil {
		return LogVerbResult{}, err
	}
	defer func() { _ = directory.Close() }()
	if err := writeJSONAtomicAt(directory, localWorkLogProjectionName, projection, 0o600); err != nil {
		return LogVerbResult{}, err
	}
	result.Applied = true
	if options.Takeover {
		if strings.TrimSpace(options.Actor) == "" {
			return LogVerbResult{}, fmt.Errorf("--actor is required with --takeover")
		}
		event, updated, err := appendLocalEvent(root, LocalWorkLogEvent{
			Type: LocalEventRecover, Message: "explicit takeover after recover diagnosis",
			Git: &gitEvidence, Extra: map[string]any{"actor": strings.TrimSpace(options.Actor)},
		})
		if err != nil {
			return LogVerbResult{}, err
		}
		result.Event = &event
		result.Projection = &updated
	}
	return result, nil
}

// LogFinalizeOptions configures wb worktree log finalize.
type LogFinalizeOptions struct {
	ProjectsRoot string
	Worktree     string
	Result       string // success|failure
	Message      string
	Apply        bool
}

// LogFinalize records a terminal result and optionally seals the Hybrid claim.
func LogFinalize(ctx context.Context, options LogFinalizeOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	result := strings.TrimSpace(options.Result)
	if result == "" {
		result = "success"
	}
	switch result {
	case "success", "failure":
	default:
		return LogVerbResult{}, fmt.Errorf("--result must be success or failure")
	}
	fence, err := withOptionalClaimFence(options.ProjectsRoot, root, true)
	if err != nil {
		return LogVerbResult{}, err
	}
	home := fence.home
	gitEvidence := observeLocalGit(ctx, root)
	if gitEvidence.Dirty && result == "success" {
		if fence.unlock != nil {
			fence.unlock()
		}
		return LogVerbResult{}, fmt.Errorf("cannot finalize success on a dirty worktree")
	}
	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type: LocalEventFinalize, Message: strings.TrimSpace(options.Message),
		Git: &gitEvidence, Result: result,
	})
	if fence.unlock != nil {
		fence.unlock()
		fence.unlock = nil
	}
	if err != nil {
		return LogVerbResult{}, err
	}
	notes := []string{"terminal event recorded; local outbox still holds sync envelopes"}
	applied := false
	if options.Apply {
		disposition := "landed"
		if result == "failure" {
			disposition = string(AbortNotLanded)
		}
		if err := sealWorkLogForRecycle(home, root, gitEvidence.Head, disposition); err != nil {
			return LogVerbResult{}, err
		}
		applied = true
		notes = append(notes, "hybrid claim sealed terminal")
	} else {
		notes = append(notes, "pass --apply to seal the hybrid claim")
	}
	return LogVerbResult{
		Worktree: root, Verb: "finalize", Event: &event, Projection: &projection,
		Applied: applied, Notes: notes, Outbox: mustCountOutbox(root),
	}, nil
}

// LogSyncOptions configures wb worktree log sync.
type LogSyncOptions struct {
	ProjectsRoot string
	Worktree     string
	Apply        bool
}

// LogSync reports local outbox state. Without a configured Synchestra endpoint
// it stays offline and only optionally acknowledges a local dry drain marker.
func LogSync(ctx context.Context, options LogSyncOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	count, err := countLocalOutbox(root)
	if err != nil {
		return LogVerbResult{}, err
	}
	notes := []string{"no authoritative Synchestra endpoint configured; local outbox retained"}
	event, projection, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type: LocalEventSyncAttempt, Message: "sync attempted",
		Result: "offline",
		Extra:  map[string]any{"outbox": count, "apply": options.Apply},
		Git:    ptrLocalGit(observeLocalGit(ctx, root)),
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	applied := false
	if options.Apply {
		notes = append(notes, "apply retained outbox because Synchestra transport is not implemented")
	}
	return LogVerbResult{
		Worktree: root, Verb: "sync", Event: &event, Projection: &projection,
		Applied: applied, Offline: true, Outbox: count, Notes: notes,
	}, nil
}

// LogArchiveOptions configures wb worktree log archive.
type LogArchiveOptions struct {
	ProjectsRoot string
	Worktree     string
	Apply        bool
	Force        bool
}

// LogArchive moves a finalized local journal into WB_HOME after the recent window.
func LogArchive(ctx context.Context, options LogArchiveOptions) (LogVerbResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	projection, err := readLocalProjection(root)
	if err != nil {
		return LogVerbResult{}, fmt.Errorf("archive requires a local projection: %w", err)
	}
	if projection.Lifecycle != "terminal" && !options.Force {
		return LogVerbResult{}, fmt.Errorf("archive requires a terminal local projection; pass --force only for explicit operator override")
	}
	events, err := readLocalEvents(root)
	if err != nil {
		return LogVerbResult{}, err
	}
	if len(events) == 0 {
		return LogVerbResult{}, fmt.Errorf("archive requires local events")
	}
	last := events[len(events)-1]
	if !options.Force && time.Since(last.At) < 7*24*time.Hour {
		return LogVerbResult{}, fmt.Errorf("archive waits seven days after the latest event (%s); pass --force to override", last.At.Format(time.RFC3339))
	}
	result := LogVerbResult{
		Worktree: root, Verb: "archive", Projection: &projection,
		Notes: []string{"dry-run; pass --apply to move .wb/local into WB_HOME/worklogs"},
	}
	if !options.Apply {
		return result, nil
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return LogVerbResult{}, err
	}
	effort := projection.EffortID
	if effort == "" {
		if manifest, err := ReadManifest(root); err == nil {
			effort = manifest.EffortID
		}
	}
	if effort == "" {
		effort = "unknown-effort"
	}
	dest := filepath.Join(home, "worklogs", effort, "archived-local", time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return LogVerbResult{}, err
	}
	src := filepath.Join(root, journalRootDirectory, journalLocalDirectory)
	if err := copyDir(src, dest); err != nil {
		return LogVerbResult{}, err
	}
	event, updated, err := appendLocalEvent(root, LocalWorkLogEvent{
		Type: LocalEventArchive, Message: "local journal archived",
		Result: "archived", Extra: map[string]any{"archive_path": dest},
	})
	if err != nil {
		return LogVerbResult{}, err
	}
	result.Event = &event
	result.Projection = &updated
	result.Applied = true
	result.Notes = []string{"copied .wb/local to " + dest + "; live journal retained until worktree cleanup"}
	return result, nil
}

func mustCountOutbox(worktree string) int {
	count, _ := countLocalOutbox(worktree)
	return count
}

func ptrLocalGit(evidence LocalGitEvidence) *LocalGitEvidence {
	copyEvidence := evidence
	return &copyEvidence
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		return os.WriteFile(target, content, mode)
	})
}
