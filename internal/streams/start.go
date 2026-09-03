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

func (refusal *Refusal) Error() string {
	if len(refusal.Sanctioned) == 0 {
		return refusal.Message
	}
	return refusal.Message + "; run: " + strings.Join(refusal.Sanctioned, " or ")
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
	RefusalNoLibrary          = "no-library"
	RefusalLiveLink           = "live-link"
	RefusalUnabsorbedWork     = "unabsorbed-work"
	RefusalNotAMember         = "not-a-member"
	RefusalStreamEnded        = "stream-ended"
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
		return StartResult{}, err
	}
	if len(options.Repositories) == 0 {
		return StartResult{}, fmt.Errorf("stream start needs at least one owner/repository")
	}
	repositories, library, err := assignRoles(options)
	if err != nil {
		return StartResult{}, err
	}
	if _, err := engine.Store.Load(options.Name); err == nil {
		return StartResult{}, &Refusal{
			Code:    RefusalStreamExists,
			Message: fmt.Sprintf("stream %q already exists", options.Name),
			Sanctioned: []string{
				"wb stream status " + options.Name,
				"wb stream join " + options.Name + " <owner/repository>",
			},
		}
	} else if !errors.Is(err, ErrNotFound) {
		return StartResult{}, err
	}
	for _, repository := range repositories {
		if err := engine.refuseSecondStream(repository, options.Name); err != nil {
			return StartResult{}, err
		}
	}

	inputs, err := engine.preflightInputs(ctx, repositories, options.Base)
	if err != nil {
		return StartResult{}, err
	}
	preflight := RunPreflight(ctx, engine.GitHub, engine.HooksCheck, inputs)
	refusing := refusingChecks()
	var refused, reported []PreflightFinding
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
	if len(refused) > 0 {
		return StartResult{Preflight: preflight}, &Refusal{
			Code:       RefusalPreflight,
			Message:    "the fleet is not ready for a stream: " + renderFindings(refused),
			Sanctioned: []string{"wb hooks repair --fleet", "wb stream start " + options.Name + " …"},
		}
	}

	created, err := engine.Worktrees.Create(ctx, options.Name, Branch(options.Name), repositories)
	if err != nil {
		return StartResult{Preflight: preflight}, err
	}
	stream := Stream{Name: options.Name, CreatedAt: engine.now()}
	for _, checkout := range created {
		role := RoleConsumer
		if strings.EqualFold(checkout.Repository, library) {
			role = RoleLibrary
		}
		stream.Members = append(stream.Members, engine.publishMember(ctx, options.Name, checkout, role, options.Base))
	}
	saved, err := engine.Store.Create(stream)
	if err != nil {
		return StartResult{Preflight: preflight}, err
	}
	engine.record(options.Name, Event{
		Stream: options.Name, Verb: "stream start", Phase: "complete", Outcome: "success",
		Detail:   fmt.Sprintf("%d members on %s", len(saved.Members), Branch(options.Name)),
		Evidence: map[string]string{"library": library, "branch": Branch(options.Name)},
	})
	return StartResult{
		Stream:              saved,
		Preflight:           preflight,
		Reported:            reported,
		TransitiveOmissions: omittedConsumers(repositories, transitive),
	}, nil
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
		return StartResult{}, err
	}
	if err := ValidateRepository(options.Repository); err != nil {
		return StartResult{}, err
	}
	stream, err := engine.Store.Load(options.Name)
	if err != nil {
		return StartResult{}, err
	}
	if !stream.Open() {
		return StartResult{}, &Refusal{
			Code:       RefusalStreamEnded,
			Message:    fmt.Sprintf("stream %q has ended", options.Name),
			Sanctioned: []string{"wb stream start <name> " + options.Repository},
		}
	}
	if _, ok := stream.Member(options.Repository); ok {
		return StartResult{Stream: stream}, nil
	}
	if err := engine.refuseSecondStream(options.Repository, options.Name); err != nil {
		return StartResult{}, err
	}
	role := options.Role
	if role == "" {
		role = RoleConsumer
	}
	if role == RoleLibrary {
		if existing, ok := stream.Library(); ok {
			return StartResult{}, &Refusal{
				Code:       RefusalNoLibrary,
				Message:    fmt.Sprintf("stream %q already has library %s", options.Name, existing.Repository),
				Sanctioned: []string{"wb stream join " + options.Name + " " + options.Repository + " --role consumer"},
			}
		}
	}
	inputs, err := engine.preflightInputs(ctx, []string{options.Repository}, options.Base)
	if err != nil {
		return StartResult{}, err
	}
	preflight := RunPreflight(ctx, engine.GitHub, engine.HooksCheck, inputs)
	refusing := refusingChecks()
	var refused, reported []PreflightFinding
	for _, finding := range preflight.Failed() {
		if refusing[finding.Check] {
			refused = append(refused, finding)
			continue
		}
		reported = append(reported, finding)
	}
	if len(refused) > 0 {
		return StartResult{Preflight: preflight}, &Refusal{
			Code:       RefusalPreflight,
			Message:    "the joining repository is not ready for a stream: " + renderFindings(refused),
			Sanctioned: []string{"wb hooks repair " + inputs[0].Path},
		}
	}
	created, err := engine.Worktrees.Create(ctx, options.Name, Branch(options.Name), []string{options.Repository})
	if err != nil {
		return StartResult{Preflight: preflight}, err
	}
	updated, err := engine.Store.Update(options.Name, func(current *Stream) error {
		if _, ok := current.Member(options.Repository); ok {
			return nil
		}
		for _, checkout := range created {
			current.Members = append(current.Members, engine.publishMember(ctx, options.Name, checkout, role, options.Base))
		}
		return nil
	})
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

// publishMember pushes the stream branch and opens its draft pull request.
//
// A pull request that cannot be opened is recorded as a per-member error
// rather than failing the whole start: the worktree and branch already exist,
// and stranding a member whose checkout is provably complete is what
// `every-stream-verb-has-a-terminal-recovery` forbids. `wb stream status`
// reports the missing pull request, and re-running `join` for that member
// opens it.
func (engine *Engine) publishMember(ctx context.Context, name string, checkout CreatedWorktree, role Role, base string) Member {
	member := Member{
		Repository: checkout.Repository,
		Role:       role,
		Worktree:   checkout.Worktree,
		Canonical:  checkout.Canonical,
		Branch:     checkout.Branch,
		Base:       firstNonEmpty(base, checkout.Base),
		JoinedAt:   engine.now(),
		Lease: Lease{
			Login: engine.Login, Machine: engine.Machine, Session: engine.Session,
			HeldSince: engine.now(),
		},
	}
	head, err := engine.Git.PushBranch(ctx, checkout.Worktree, checkout.Branch)
	if err != nil {
		member.PullRequestError = err.Error()
		engine.record(name, Event{
			Stream: name, Verb: "stream start", Phase: "push", Outcome: "findings",
			Repository: checkout.Repository, Detail: err.Error(),
		})
		return member
	}
	member.Lease.RecordedHead = head
	title := fmt.Sprintf("stream(%s): %s", name, checkout.Repository)
	body := streamPullRequestBody(name, role)
	pullRequest, err := engine.GitHub.CreateDraftPullRequest(ctx, checkout.Worktree, member.Base, checkout.Branch, title, body)
	if err != nil {
		member.PullRequestError = err.Error()
		engine.record(name, Event{
			Stream: name, Verb: "stream start", Phase: "pull-request", Outcome: "findings",
			Repository: checkout.Repository, Detail: err.Error(),
		})
		return member
	}
	member.PullRequest = pullRequest.Number
	member.PullRequestURL = pullRequest.URL
	engine.record(name, Event{
		Stream: name, Verb: "stream start", Phase: "pull-request", Outcome: "success",
		Repository: checkout.Repository,
		Evidence:   map[string]string{"pull_request": pullRequest.URL, "head": head},
	})
	return member
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

func (engine *Engine) refuseSecondStream(repository, joining string) error {
	holder, held, err := engine.Store.RepositoryStream(repository)
	if err != nil {
		return err
	}
	if !held || holder.Name == joining {
		return nil
	}
	return &Refusal{
		Code:    RefusalRepositoryInStream,
		Message: fmt.Sprintf("%s already carries open stream %q; a repository carries at most one open stream, because landing one stream rewrites the base under the other", repository, holder.Name),
		Sanctioned: []string{
			"wb stream join " + holder.Name + " " + repository,
			"wb stream end " + holder.Name,
		},
	}
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
			return nil, "", err
		}
		if seen[strings.ToLower(repository)] {
			return nil, "", fmt.Errorf("repository %s is listed twice", repository)
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
		return nil, "", fmt.Errorf("--library %s is not one of the stream repositories", library)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
