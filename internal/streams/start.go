package streams

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Refusal is a guard that fired. It carries the stable code a caller branches
// on and the exact command that satisfies the guard, because
// `every-refusal-names-the-sanctioned-command` makes a refusal without a next
// step a hand-written workaround waiting to happen.
type Refusal struct {
	Code       string
	Message    string
	Sanctioned []string
}

// Error renders the refusal for stderr. It redacts, because a refusal message
// routinely quotes a child process's output and that output routinely carries
// a remote URL with an embedded credential.
func (refusal *Refusal) Error() string {
	if len(refusal.Sanctioned) == 0 {
		return RedactString(refusal.Message)
	}
	return RedactString(refusal.Message + "; run: " + strings.Join(refusal.Sanctioned, " or "))
}

// Refused reports whether err is a guard refusal rather than a failure, and
// returns it. A caller uses this to choose exit code 2 over 1.
func Refused(err error) (*Refusal, bool) {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal, true
	}
	return nil, false
}

// Refusal codes are contract: they appear in the JSON envelope and skills
// branch on them.
const (
	RefusalStreamExists       = "stream-exists"
	RefusalRepositoryInStream = "repository-in-stream"
	RefusalPreflight          = "preflight-failed"
	// RefusalNoLibrary fires when a stream has no library member at all.
	RefusalNoLibrary = "no-library"
	// RefusalLibraryExists fires when a second library is proposed for a
	// stream that already has one. It used to share RefusalNoLibrary's code,
	// which said the opposite of what happened — and refusal codes are
	// contract that skills branch on.
	RefusalLibraryExists = "library-exists"
	// RefusalUsage marks an ambiguous invocation. It exists so a usage error
	// carries the same envelope and exit code as any other guard.
	RefusalUsage          = "usage"
	RefusalLiveLink       = "live-link"
	RefusalUnabsorbedWork = "unabsorbed-work"
	RefusalStreamEnded    = "stream-ended"
)

// CreatedWorktree is one checkout the worktree creation path published.
type CreatedWorktree struct {
	Repository string
	Worktree   string
	Canonical  string
	Branch     string
	Base       string
}

// WorktreeCreator is the existing worktree creation path.
//
// `stream-is-a-named-set-of-worktrees` forbids inventing a second creation
// path, so this port exists to delegate to `wb worktree create` — with its
// branch policy, prompt archival and fleet-wide claim — rather than to
// reimplement any of it.
type WorktreeCreator interface {
	// PlannedWorktree is where Create will publish one repository's checkout.
	// It performs no side effect, so the stream record can carry each
	// member's intended coordinates BEFORE anything is created — which is
	// what makes an interrupted start recoverable.
	PlannedWorktree(task, repository string) string
	Create(ctx context.Context, task, branch string, repositories []string) ([]CreatedWorktree, error)
	// Remove retires one member's worktree through the existing cleanup
	// path. `stream end` delegates removal rather than deleting directories.
	Remove(ctx context.Context, task, repository, worktree string) error
}

// Engine runs the stream verbs against injected ports.
type Engine struct {
	Store     *Store
	Git       Git
	GitHub    GitHub
	Worktrees WorktreeCreator
	// ProjectsRoot locates the canonical clones the preflight checks read.
	ProjectsRoot string
	// HooksCheck answers the readiness preflight's hooks question. Nil uses
	// the installed `wb hooks check`.
	HooksCheck HooksChecker
	// Login, Machine and Session identify the lease holder.
	Login   string
	Machine string
	Session string
	Now     func() time.Time
}

func (engine *Engine) now() time.Time {
	if engine.Now == nil {
		return time.Now().UTC()
	}
	return engine.Now().UTC()
}

// StartOptions is one `wb stream start` invocation.
type StartOptions struct {
	Name string
	// Repositories are the members in declaration order. The first is the
	// library unless Library names another.
	Repositories []string
	Library      string
	// Base overrides the branch the draft pull requests target. Empty
	// resolves each repository's own default branch.
	Base string
}

// StartResult is what `stream start` produced.
type StartResult struct {
	Stream    Stream    `json:"stream"`
	Preflight Preflight `json:"preflight"`
	// Reported are the preflight findings that did not refuse the start but
	// that the operator must see. `stream-start-proves-the-fleet-is-ready`
	// allows a member to be reported instead of refused; nothing may be
	// silently passed.
	Reported []PreflightFinding `json:"reported,omitempty"`
	// TransitiveOmissions names consumers the dependency graph reaches that
	// the operator did not include. Leaving one out is legal; leaving one
	// out silently is not, because remote propagation bumps only members.
	TransitiveOmissions []string `json:"transitive_omissions,omitempty"`
}

// refusingChecks are the preflight checks whose failure refuses the start.
// They are the ones an operator can fix locally, and whose refusal names the
// command that fixes them. The remaining checks are reported: a red default
// branch and a missing CI concurrency group are real findings, but they are
// not this operator's to fix before the stream can exist, and inventing a
// bypass flag for them would create exactly the guard-with-an-escape-hatch
// `every-refusal-names-the-sanctioned-command` rules out.
func refusingChecks() map[string]bool {
	return map[string]bool{CheckHooks: true, CheckNpmProviderIdentity: true}
}

// Start creates a stream. Every fence runs before the first side effect: name
// validation, the one-open-stream-per-repository claim, and the fleet
// readiness checks all complete before any worktree is created.
//
// Implements: dependency-streams#req:stream-is-a-named-set-of-worktrees,
// dependency-streams#req:stream-state-is-untracked-and-local,
// dependency-streams#req:stream-branch-with-draft-pr,
// dependency-streams#req:stream-start-proves-the-fleet-is-ready,
// dependency-streams#req:stream-pushes-use-a-lease-and-a-stream-claim.
func (engine *Engine) Start(ctx context.Context, options StartOptions, transitive []string) (StartResult, error) {
	if err := ValidateName(options.Name); err != nil {
		return StartResult{}, &Refusal{Code: RefusalUsage, Message: err.Error()}
	}
	if len(options.Repositories) == 0 {
		return StartResult{}, &Refusal{Code: RefusalUsage, Message: "stream start needs at least one owner/repository"}
	}
	repositories, library, err := assignRoles(options)
	if err != nil {
		return StartResult{}, err
	}

	// Cheap fences first, so a repository that is already in another stream
	// never pays for a hook check, a red-base check and a fetch it cannot
	// use. These are advisory: the authoritative decision is re-taken under
	// the store lock below, where it is indivisible with reserving the name.
	if existing, loadErr := engine.Store.Load(options.Name); loadErr == nil && existing.Open() {
		return StartResult{}, streamExistsRefusal(options.Name)
	} else if loadErr != nil && !errors.Is(loadErr, ErrNotFound) {
		return StartResult{}, loadErr
	}
	var stateFindings []PreflightFinding
	for _, repository := range repositories {
		unreadable, refuseErr := engine.refuseSecondStream(repository, options.Name)
		if refuseErr != nil {
			return StartResult{}, refuseErr
		}
		stateFindings = unreadableFindings(unreadable)
	}

	inputs, err := engine.preflightInputs(ctx, repositories, options.Base)
	if err != nil {
		return StartResult{}, err
	}
	// The readiness checks read origin, so origin is re-read first: a stale
	// clone would answer the red-base question from a snapshot. A fetch that
	// fails is REPORTED rather than discarded — the checks below then ran
	// against a possibly stale clone, and the operator has to know that.
	fetchFindings := engine.refreshOrigins(ctx, inputs)
	preflight := RunPreflight(ctx, engine.GitHub, engine.HooksCheck, inputs)
	refused, reported := splitPreflight(preflight)
	reported = append(reported, fetchFindings...)
	reported = append(reported, stateFindings...)
	if len(refused) > 0 {
		return StartResult{Preflight: preflight}, &Refusal{
			Code:       RefusalPreflight,
			Message:    "the fleet is not ready for a stream: " + renderFindings(refused),
			Sanctioned: []string{"wb hooks repair --fleet", "wb stream start " + options.Name + " …"},
		}
	}

	// STATE FIRST. The record — with every member's intended worktree,
	// branch and base — is written before the first side effect, under the
	// store-wide lock that also decides the one-open-stream question. Two
	// concurrent starts therefore arbitrate before either pushes anything,
	// and a crash at any point leaves a `creating` stream that
	// `wb stream end` can retire from the record.
	var reserved Stream
	var archived string
	err = engine.Store.WithStoreLock(func() error {
		existing, loadErr := engine.Store.Load(options.Name)
		switch {
		case loadErr == nil && existing.Open():
			return streamExistsRefusal(options.Name)
		case loadErr == nil:
			// An ended stream keeps its record — the event log is evidence —
			// but it must not burn its name forever. Archiving frees the name
			// and keeps everything.
			name, archiveErr := engine.Store.ArchiveLocked(options.Name)
			if archiveErr != nil {
				return archiveErr
			}
			archived = name
		case !errors.Is(loadErr, ErrNotFound):
			return loadErr
		}
		for _, repository := range repositories {
			if _, refuseErr := engine.refuseSecondStream(repository, options.Name); refuseErr != nil {
				return refuseErr
			}
		}
		planned := Stream{Name: options.Name, CreatedAt: engine.now(), Phase: PhaseCreating}
		for _, repository := range repositories {
			role := RoleConsumer
			if strings.EqualFold(repository, library) {
				role = RoleLibrary
			}
			planned.Members = append(planned.Members, Member{
				Repository: repository,
				Role:       role,
				Worktree:   engine.Worktrees.PlannedWorktree(options.Name, repository),
				Branch:     Branch(options.Name),
				Base:       memberBase(options.Base, inputs, repository),
				JoinedAt:   engine.now(),
				Lease: Lease{
					Login: engine.Login, Machine: engine.Machine, Session: engine.Session,
					HeldSince: engine.now(),
				},
			})
		}
		created, createErr := engine.Store.CreateLocked(planned)
		if createErr != nil {
			return createErr
		}
		reserved = created
		return nil
	})
	if err != nil {
		return StartResult{Preflight: preflight}, err
	}
	engine.record(options.Name, Event{
		Stream: options.Name, Verb: "stream start", Phase: "reserve", Outcome: "success",
		Detail:   fmt.Sprintf("reserved %d member(s) before any side effect", len(reserved.Members)),
		Evidence: map[string]string{"phase": string(PhaseCreating), "archived_previous": archived},
	})

	// Side effects now, each recorded as it lands.
	checkouts, err := engine.Worktrees.Create(ctx, options.Name, Branch(options.Name), repositories)
	if err != nil {
		return StartResult{Stream: reserved, Preflight: preflight}, fmt.Errorf(
			"%w — stream %q is recorded as %s; retire it with `wb stream end %s --apply`",
			err, options.Name, PhaseCreating, options.Name)
	}
	for _, checkout := range checkouts {
		if _, updateErr := engine.recordCheckout(options.Name, checkout); updateErr != nil {
			return StartResult{Stream: reserved, Preflight: preflight}, updateErr
		}
	}
	for _, checkout := range checkouts {
		if err := engine.publishMember(ctx, options.Name, checkout); err != nil {
			return StartResult{Stream: reserved, Preflight: preflight}, err
		}
	}
	saved, err := engine.Store.Update(options.Name, func(current *Stream) error {
		current.Phase = PhaseOpen
		return nil
	})
	if err != nil {
		return StartResult{Stream: reserved, Preflight: preflight}, err
	}
	engine.record(options.Name, Event{
		Stream: options.Name, Verb: "stream start", Phase: "complete", Outcome: "success",
		Detail:   fmt.Sprintf("%d members on %s", len(saved.Members), Branch(options.Name)),
		Evidence: map[string]string{"library": library, "branch": Branch(options.Name)},
	})
	if archived != "" {
		reported = append(reported, PreflightFinding{
			Check: "stream-name-reused", Status: PreflightPass,
			Detail: "an ended stream of this name was archived as " + archived,
		})
	}
	// A member whose branch or draft pull request never landed is a finding,
	// not a silent field in the record. Without this the verb exits 0 while a
	// member has no pull request and therefore no CI — exactly the state the
	// draft-PR requirement exists to prevent.
	reported = append(reported, incompleteMemberFindings(saved)...)
	return StartResult{
		Stream:              saved,
		Preflight:           preflight,
		Reported:            reported,
		TransitiveOmissions: omittedConsumers(repositories, transitive),
	}, nil
}

// refreshOrigins re-reads origin for every member and reports each failure.
//
// A fetch WB could not complete does not stop the start — the checks can still
// run, and refusing on an unreachable network would make the verb unusable
// offline — but it changes what those checks mean, so it is never discarded.
func (engine *Engine) refreshOrigins(ctx context.Context, inputs []PreflightInput) []PreflightFinding {
	var findings []PreflightFinding
	for _, input := range inputs {
		if err := engine.Git.Fetch(ctx, input.Path); err != nil {
			findings = append(findings, PreflightFinding{
				Repository: input.Repository,
				Check:      "origin-refresh",
				Status:     PreflightUnknown,
				Detail: "could not re-read origin, so the readiness checks below ran against a possibly stale clone: " +
					RedactString(err.Error()),
			})
		}
	}
	return findings
}

// incompleteMemberFindings names every member left without a pushed branch or
// an open draft pull request.
func incompleteMemberFindings(stream Stream) []PreflightFinding {
	var findings []PreflightFinding
	for _, member := range stream.Members {
		if member.PullRequest != 0 {
			continue
		}
		detail := "no draft pull request was opened, so pushes to " + member.Branch + " run no CI"
		if member.PullRequestError != "" {
			detail += ": " + member.PullRequestError
		}
		findings = append(findings, PreflightFinding{
			Repository: member.Repository,
			Check:      "draft-pull-request",
			Status:     PreflightFail,
			Detail:     detail + " — retry with `wb stream join " + stream.Name + " " + member.Repository + "`",
		})
	}
	return findings
}

// splitPreflight separates findings that refuse from findings that are
// reported. Nothing is ever silently passed: an unknown is reported too.
func splitPreflight(preflight Preflight) (refused, reported []PreflightFinding) {
	refusing := refusingChecks()
	for _, finding := range preflight.Failed() {
		if refusing[finding.Check] {
			refused = append(refused, finding)
			continue
		}
		reported = append(reported, finding)
	}
	for _, finding := range preflight.Findings {
		if finding.Status == PreflightUnknown {
			reported = append(reported, finding)
		}
	}
	return refused, reported
}

func memberBase(explicit string, inputs []PreflightInput, repository string) string {
	if explicit != "" {
		return explicit
	}
	for _, input := range inputs {
		if strings.EqualFold(input.Repository, repository) {
			return input.DefaultBranch
		}
	}
	return ""
}

// recordCheckout replaces a member's planned coordinates with the exact ones
// the worktree path published.
func (engine *Engine) recordCheckout(name string, checkout CreatedWorktree) (Stream, error) {
	return engine.Store.Update(name, func(current *Stream) error {
		for index := range current.Members {
			if !strings.EqualFold(current.Members[index].Repository, checkout.Repository) {
				continue
			}
			current.Members[index].Worktree = checkout.Worktree
			current.Members[index].Canonical = checkout.Canonical
			current.Members[index].Branch = checkout.Branch
			if current.Members[index].Base == "" {
				current.Members[index].Base = checkout.Base
			}
		}
		return nil
	})
}

// JoinOptions is one `wb stream join` invocation.
type JoinOptions struct {
	Name       string
	Repository string
	Role       Role
	Base       string
}

// Join adds a repository to an existing stream, creating its stream worktree,
// branch and draft pull request exactly as Start does, so every later verb
// treats it as a member from that point on.
//
// Implements: dependency-streams#req:stream-pushes-use-a-lease-and-a-stream-claim
// (the join half of the one-stream-per-repository refusal).
func (engine *Engine) Join(ctx context.Context, options JoinOptions) (StartResult, error) {
	if err := ValidateName(options.Name); err != nil {
		return StartResult{}, &Refusal{Code: RefusalUsage, Message: err.Error()}
	}
	if err := ValidateRepository(options.Repository); err != nil {
		return StartResult{}, &Refusal{Code: RefusalUsage, Message: err.Error()}
	}
	stream, err := engine.Store.Load(options.Name)
	if err != nil {
		return StartResult{}, err
	}
	if !stream.Open() {
		return StartResult{}, &Refusal{
			Code:    RefusalStreamEnded,
			Message: fmt.Sprintf("stream %q has ended", options.Name),
			Sanctioned: []string{
				"wb stream start " + options.Name + " " + options.Repository,
				"wb stream delete " + options.Name,
			},
		}
	}
	// An existing member with no draft pull request is the recovery path
	// publishMember's failure mode documents: re-running join retries exactly
	// the effect that did not land, rather than no-opping and leaving the
	// promise unkept.
	if member, ok := stream.Member(options.Repository); ok {
		if member.PullRequest != 0 || member.Worktree == "" {
			return StartResult{Stream: stream}, nil
		}
		checkout := CreatedWorktree{
			Repository: member.Repository, Worktree: member.Worktree,
			Canonical: member.Canonical, Branch: member.Branch, Base: member.Base,
		}
		if err := engine.publishMember(ctx, options.Name, checkout); err != nil {
			return StartResult{Stream: stream}, err
		}
		retried, loadErr := engine.Store.Load(options.Name)
		if loadErr != nil {
			return StartResult{}, loadErr
		}
		return StartResult{Stream: retried}, nil
	}

	role := options.Role
	if role == "" {
		role = RoleConsumer
	}
	if role == RoleLibrary {
		if existing, ok := stream.Library(); ok {
			return StartResult{}, &Refusal{
				Code:       RefusalLibraryExists,
				Message:    fmt.Sprintf("stream %q already has library %s", options.Name, existing.Repository),
				Sanctioned: []string{"wb stream join " + options.Name + " " + options.Repository + " --role consumer"},
			}
		}
	}
	inputs, err := engine.preflightInputs(ctx, []string{options.Repository}, options.Base)
	if err != nil {
		return StartResult{}, err
	}
	fetchFindings := engine.refreshOrigins(ctx, inputs)
	preflight := RunPreflight(ctx, engine.GitHub, engine.HooksCheck, inputs)
	refused, reported := splitPreflight(preflight)
	reported = append(reported, fetchFindings...)
	if len(refused) > 0 {
		return StartResult{Preflight: preflight}, &Refusal{
			Code:       RefusalPreflight,
			Message:    "the joining repository is not ready for a stream: " + renderFindings(refused),
			Sanctioned: []string{"wb hooks repair " + inputs[0].Path},
		}
	}

	// STATE FIRST here too: the member is recorded with its intended
	// coordinates under the store lock, before any worktree is created, so a
	// crash mid-join leaves a member `status` and `end` can both see.
	if err := engine.Store.WithStoreLock(func() error {
		if _, refuseErr := engine.refuseSecondStream(options.Repository, options.Name); refuseErr != nil {
			return refuseErr
		}
		_, updateErr := engine.Store.Update(options.Name, func(current *Stream) error {
			if _, ok := current.Member(options.Repository); ok {
				return nil
			}
			current.Members = append(current.Members, Member{
				Repository: options.Repository,
				Role:       role,
				Worktree:   engine.Worktrees.PlannedWorktree(options.Name, options.Repository),
				Branch:     Branch(options.Name),
				Base:       memberBase(options.Base, inputs, options.Repository),
				JoinedAt:   engine.now(),
				Lease: Lease{
					Login: engine.Login, Machine: engine.Machine, Session: engine.Session,
					HeldSince: engine.now(),
				},
			})
			return nil
		})
		return updateErr
	}); err != nil {
		return StartResult{Preflight: preflight}, err
	}

	checkouts, err := engine.Worktrees.Create(ctx, options.Name, Branch(options.Name), []string{options.Repository})
	if err != nil {
		return StartResult{Preflight: preflight}, err
	}
	for _, checkout := range checkouts {
		if _, updateErr := engine.recordCheckout(options.Name, checkout); updateErr != nil {
			return StartResult{Preflight: preflight}, updateErr
		}
		if publishErr := engine.publishMember(ctx, options.Name, checkout); publishErr != nil {
			return StartResult{Preflight: preflight}, publishErr
		}
	}
	updated, err := engine.Store.Load(options.Name)
	if err != nil {
		return StartResult{Preflight: preflight}, err
	}
	engine.record(options.Name, Event{
		Stream: options.Name, Verb: "stream join", Phase: "complete", Outcome: "success",
		Repository: options.Repository,
		Evidence:   map[string]string{"role": string(role), "branch": Branch(options.Name)},
	})
	return StartResult{Stream: updated, Preflight: preflight, Reported: reported}, nil
}

// publishMember pushes the stream branch and opens its draft pull request,
// recording each effect in stream state as it lands.
//
// Every write to the record happens OUTSIDE the store lock's network window:
// the push and the `gh pr create` run first, then a short Update persists the
// result. One stalled `gh` therefore cannot freeze every other stream verb.
//
// A pull request that cannot be opened is recorded as a per-member error
// rather than failing the whole start: the worktree and branch already exist,
// and stranding a member whose checkout is provably complete is what
// `every-stream-verb-has-a-terminal-recovery` forbids. `wb stream status`
// reports the missing pull request, and `wb stream join` for that member
// retries it.
func (engine *Engine) publishMember(ctx context.Context, name string, checkout CreatedWorktree) error {
	head, pushErr := engine.Git.PushBranch(ctx, checkout.Worktree, checkout.Branch)
	if pushErr != nil {
		detail := RedactString(pushErr.Error())
		engine.record(name, Event{
			Stream: name, Verb: "stream start", Phase: "push", Outcome: "findings",
			Repository: checkout.Repository, Detail: detail,
		})
		_, err := engine.setMember(name, checkout.Repository, func(member *Member) {
			member.PullRequestError = detail
		})
		return err
	}
	if _, err := engine.setMember(name, checkout.Repository, func(member *Member) {
		member.Lease.RecordedHead = head
		member.PullRequestError = ""
	}); err != nil {
		return err
	}

	role := RoleConsumer
	if stream, loadErr := engine.Store.Load(name); loadErr == nil {
		if member, ok := stream.Member(checkout.Repository); ok {
			role = member.Role
		}
	}
	base := checkout.Base
	if stream, loadErr := engine.Store.Load(name); loadErr == nil {
		if member, ok := stream.Member(checkout.Repository); ok && member.Base != "" {
			base = member.Base
		}
	}
	title := fmt.Sprintf("stream(%s): %s", name, checkout.Repository)
	pullRequest, prErr := engine.GitHub.CreateDraftPullRequest(ctx, checkout.Worktree, base, checkout.Branch, title, streamPullRequestBody(name, role))
	if prErr != nil {
		detail := RedactString(prErr.Error())
		engine.record(name, Event{
			Stream: name, Verb: "stream start", Phase: "pull-request", Outcome: "findings",
			Repository: checkout.Repository, Detail: detail,
		})
		_, err := engine.setMember(name, checkout.Repository, func(member *Member) {
			member.PullRequestError = detail
		})
		return err
	}
	engine.record(name, Event{
		Stream: name, Verb: "stream start", Phase: "pull-request", Outcome: "success",
		Repository: checkout.Repository,
		Evidence:   map[string]string{"pull_request": pullRequest.URL, "head": head},
	})
	_, err := engine.setMember(name, checkout.Repository, func(member *Member) {
		member.PullRequest = pullRequest.Number
		member.PullRequestURL = pullRequest.URL
		member.PullRequestError = ""
	})
	return err
}

// setMember applies a change to exactly one member under the per-stream lock.
func (engine *Engine) setMember(name, repository string, mutate func(*Member)) (Stream, error) {
	return engine.Store.Update(name, func(current *Stream) error {
		for index := range current.Members {
			if strings.EqualFold(current.Members[index].Repository, repository) {
				mutate(&current.Members[index])
			}
		}
		return nil
	})
}

func streamPullRequestBody(name string, role Role) string {
	return strings.Join([]string{
		"Draft stream pull request for `" + Branch(name) + "` (" + string(role) + ").",
		"",
		"It stays a draft until the stream lands: it exists so CI runs on every push to the",
		"stream branch and the stream's true state is always visible. Agents branch from",
		"`" + Branch(name) + "` and open their pull requests against it, never against the base.",
		"",
		"Consume the library through `wb deps propagate local`; the orchestrator runs",
		"`wb deps propagate remote` at the end. End with `wb worktree end`.",
	}, "\n")
}

func streamExistsRefusal(name string) *Refusal {
	return &Refusal{
		Code:    RefusalStreamExists,
		Message: fmt.Sprintf("stream %q already exists", name),
		Sanctioned: []string{
			"wb stream status " + name,
			"wb stream join " + name + " <owner/repository>",
			"wb stream end " + name,
		},
	}
}

// refuseSecondStream enforces one open stream per repository.
//
// It returns the unreadable records alongside its decision: a "no stream holds
// this repository" answer is only as good as the records WB could read, and a
// truncated file could be the very stream that holds it. The caller reports
// them so the guard never looks more certain than it is.
func (engine *Engine) refuseSecondStream(repository, joining string) ([]Unreadable, error) {
	holder, held, unreadable, err := engine.Store.RepositoryStream(repository)
	if err != nil {
		return nil, err
	}
	if !held || holder.Name == joining {
		return unreadable, nil
	}
	return unreadable, &Refusal{
		Code:    RefusalRepositoryInStream,
		Message: fmt.Sprintf("%s already carries open stream %q; a repository carries at most one open stream, because landing one stream rewrites the base under the other", repository, holder.Name),
		Sanctioned: []string{
			"wb stream join " + holder.Name + " " + repository,
			"wb stream end " + holder.Name,
		},
	}
}

// unreadableFindings turns unreadable stream records into reported findings.
// They are never fatal — one truncated file must not refuse every start on the
// machine — but they are never silent either.
func unreadableFindings(unreadable []Unreadable) []PreflightFinding {
	findings := make([]PreflightFinding, 0, len(unreadable))
	for _, broken := range unreadable {
		findings = append(findings, PreflightFinding{
			Check:  "stream-state-readable",
			Status: PreflightUnknown,
			Detail: fmt.Sprintf("stream record %s could not be read (%s), so the one-open-stream check could not consider it", broken.Name, broken.Reason),
		})
	}
	return findings
}

func (engine *Engine) preflightInputs(ctx context.Context, repositories []string, base string) ([]PreflightInput, error) {
	inputs := make([]PreflightInput, 0, len(repositories))
	for _, repository := range repositories {
		path := canonicalPath(engine.ProjectsRoot, repository)
		branch := base
		if branch == "" {
			resolved, err := engine.Git.DefaultBranch(ctx, path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", repository, err)
			}
			branch = resolved
		}
		inputs = append(inputs, PreflightInput{Repository: repository, Path: path, DefaultBranch: branch})
	}
	return inputs, nil
}

func (engine *Engine) record(name string, event Event) {
	log := engine.Store.EventLog(name)
	_ = log.Append(event)
}

func assignRoles(options StartOptions) (repositories []string, library string, err error) {
	seen := map[string]bool{}
	for _, repository := range options.Repositories {
		if err := ValidateRepository(repository); err != nil {
			return nil, "", &Refusal{Code: RefusalUsage, Message: err.Error()}
		}
		if seen[strings.ToLower(repository)] {
			return nil, "", &Refusal{Code: RefusalUsage, Message: fmt.Sprintf("repository %s is listed twice", repository)}
		}
		seen[strings.ToLower(repository)] = true
		repositories = append(repositories, repository)
	}
	library = options.Library
	if library == "" {
		library = repositories[0]
		return repositories, library, nil
	}
	if !seen[strings.ToLower(library)] {
		return nil, "", &Refusal{
			Code:       RefusalUsage,
			Message:    fmt.Sprintf("--library %s is not one of the stream repositories", library),
			Sanctioned: []string{"wb stream start <name> " + library + " …"},
		}
	}
	return repositories, library, nil
}

func omittedConsumers(members, transitive []string) []string {
	included := map[string]bool{}
	for _, member := range members {
		included[strings.ToLower(member)] = true
	}
	var omitted []string
	for _, candidate := range transitive {
		if !included[strings.ToLower(candidate)] {
			omitted = append(omitted, candidate)
		}
	}
	return omitted
}

func renderFindings(findings []PreflightFinding) string {
	rendered := make([]string, 0, len(findings))
	for _, finding := range findings {
		rendered = append(rendered, fmt.Sprintf("%s %s: %s", finding.Repository, finding.Check, finding.Detail))
	}
	return strings.Join(rendered, "; ")
}
