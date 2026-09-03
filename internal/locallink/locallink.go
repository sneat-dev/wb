// Package locallink builds a consumer against a library's *working tree*
// instead of a published version, so a change is proven across every affected
// repository before anything is published.
//
// The link is untracked by construction. A Go consumer gains a Git-excluded
// `go.work`; an npm consumer gains a `node_modules` entry pointing at a built
// dist cached by the library's content hash. No tracked file changes, no
// `replace` directive, no pnpm override, alias, or `workspace:` entry — an
// override is exactly the artefact that survives the stream, reaches CI, and
// makes a consumer build against something the registry never published.
//
// Every link is recorded in stream state at the moment it is created, with
// enough detail to reverse it exactly, so `--undo` depends on the record rather
// than on the library worktree still existing.
//
// Implements: dependency-streams#req:local-link-discovers-what-the-library-publishes,
// dependency-streams#req:go-consumers-link-through-an-untracked-go-work,
// dependency-streams#req:npm-consumers-link-through-a-built-dist,
// dependency-streams#req:links-are-recorded-and-undoable,
// dependency-streams#req:no-module-graph-mutation-under-a-live-link,
// dependency-streams#req:the-local-gate-states-what-it-verified-against,
// dependency-streams#req:verify-runs-single-worker-against-the-linked-copy,
// dependency-streams#req:verification-prints-its-active-links,
// dependency-streams#req:npm-link-preserves-a-frozen-lockfile-baseline.
package locallink

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

// Options is one `wb deps propagate local` invocation.
type Options struct {
	// Library is the library worktree whose working tree the consumers build
	// against. It may be empty for --undo, where the record is the source of
	// truth.
	Library string
	// Consumers are the consumer worktrees to link or unlink.
	Consumers []string
	// Undo restores published versions and removes the links.
	Undo bool
	// Verify runs each consumer's lint and tests against the linked copy,
	// single-worker, plus the GOWORK=off build and vet the pre-landing gate
	// requires.
	Verify bool
	// Timeout bounds every child process. Zero uses a sensible default.
	Timeout time.Duration
	// Stream names the stream whose state records the links. Empty resolves
	// the stream that holds the first consumer worktree.
	Stream string
}

// ConsumerResult is one consumer's outcome.
type ConsumerResult struct {
	Consumer string `json:"consumer"`
	// Repository is the consumer's owner/repository when stream state knows
	// it.
	Repository string `json:"repository,omitempty"`
	// Skipped is set when the consumer declares none of the library's
	// published identities. Such a consumer is reported and skipped rather
	// than linked to something it does not use.
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Links are the links created (or removed, under --undo).
	Links []streams.Link `json:"links,omitempty"`
	// Verification is the single-worker run against the linked copy.
	Verification *Verification `json:"verification,omitempty"`
	// Errors are per-consumer failures. One consumer's failure never stops
	// the pass: the point of a stream is to learn about every consumer at
	// once.
	Errors []string `json:"errors,omitempty"`
}

// Result is the whole invocation's report.
type Result struct {
	Library string `json:"library,omitempty"`
	// LibraryRepository is the library's owner/repository, which survives the
	// worktree being removed.
	LibraryRepository string `json:"library_repository,omitempty"`
	// ContentHash identifies the library working tree the links exposed,
	// including modified and untracked files. The library is uncommitted by
	// construction, so it has no SHA.
	ContentHash string `json:"content_hash,omitempty"`
	// Dirty reports whether the library working tree differs from its HEAD.
	Dirty bool `json:"dirty,omitempty"`
	// Identities are the published identities discovered from the library
	// worktree itself.
	Identities []streams.Identity `json:"identities,omitempty"`
	Stream     string             `json:"stream,omitempty"`
	Consumers  []ConsumerResult   `json:"consumers"`
	// Plan states the checks this invocation will run, before it runs them.
	Plan []string `json:"plan,omitempty"`
}

// Failed reports whether any consumer failed to link or verify.
func (result Result) Failed() bool {
	for _, consumer := range result.Consumers {
		if len(consumer.Errors) > 0 {
			return true
		}
		if consumer.Verification != nil && !consumer.Verification.Passed {
			return true
		}
	}
	return false
}

// Engine performs local propagation against injected ports.
type Engine struct {
	Store *streams.Store
	Git   Git
	// Node builds and links npm packages.
	Node Node
	// Verifier runs the consumer's own lint and tests.
	Verifier Verifier
	// CacheRoot is where built library dists are cached by content hash.
	CacheRoot string
	Now       func() time.Time
}

func (engine *Engine) now() time.Time {
	if engine.Now == nil {
		return time.Now().UTC()
	}
	return engine.Now().UTC()
}

// Run performs one invocation.
func (engine *Engine) Run(ctx context.Context, options Options) (Result, error) {
	if options.Undo {
		return engine.undo(ctx, options)
	}
	return engine.link(ctx, options)
}

func (engine *Engine) link(ctx context.Context, options Options) (Result, error) {
	if strings.TrimSpace(options.Library) == "" {
		return Result{}, fmt.Errorf("a library worktree is required; pass the path of the library's checkout")
	}
	if len(options.Consumers) == 0 {
		return Result{}, fmt.Errorf("at least one --to <consumer-worktree> is required")
	}
	library, err := filepath.Abs(options.Library)
	if err != nil {
		return Result{}, err
	}
	identities, err := streams.DiscoverPublished(library)
	if err != nil {
		return Result{}, err
	}
	if len(identities) == 0 {
		return Result{}, fmt.Errorf("%s publishes no discoverable Go module or npm package; discovery reads backend/go.mod (or the module root) and libs/**/package.json, and never accepts a supplied package name as a substitute", library)
	}
	hash, dirty, err := engine.Git.ContentHash(ctx, library)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Library: library, ContentHash: hash, Dirty: dirty, Identities: identities,
		Plan: linkPlan(identities, options.Verify),
	}
	stream, member, found, err := engine.resolveStream(options)
	if err != nil {
		return result, err
	}
	if found {
		result.Stream = stream
		result.LibraryRepository = member
	}

	for _, consumerPath := range options.Consumers {
		consumer, err := filepath.Abs(consumerPath)
		if err != nil {
			result.Consumers = append(result.Consumers, ConsumerResult{Consumer: consumerPath, Errors: []string{err.Error()}})
			continue
		}
		result.Consumers = append(result.Consumers, engine.linkConsumer(ctx, options, result, library, consumer, identities, hash))
	}
	if err := engine.recordLinks(result); err != nil {
		return result, err
	}
	if options.Verify {
		engine.verifyConsumers(ctx, options, &result)
	}
	return result, nil
}

func linkPlan(identities []streams.Identity, verify bool) []string {
	plan := []string{"discover the library's published identities from its own worktree"}
	for _, identity := range identities {
		switch identity.Ecosystem {
		case streams.EcosystemGo:
			plan = append(plan, "write an excluded go.work naming every module in the consumer worktree plus "+identity.Name)
		case streams.EcosystemNpm:
			plan = append(plan,
				"prove a clean frozen install of the unlinked consumer tree",
				"build "+identity.Name+" once with the repository's own build target, cached by the library content hash",
				"link the built dist into the consumer's node_modules without touching a tracked file")
		}
	}
	plan = append(plan, "record every link in stream state so --undo can reverse it exactly")
	if verify {
		plan = append(plan,
			"run each consumer's lint and tests against the linked copy, single-worker",
			"run a GOWORK=off build and vet as the pre-landing check")
	}
	return dedupe(plan)
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func (engine *Engine) linkConsumer(
	ctx context.Context,
	options Options,
	result Result,
	library, consumer string,
	identities []streams.Identity,
	hash string,
) ConsumerResult {
	outcome := ConsumerResult{Consumer: consumer}
	declarations, err := streams.DiscoverDeclarations(consumer, identities)
	if err != nil {
		outcome.Errors = append(outcome.Errors, err.Error())
		return outcome
	}
	if len(declarations) == 0 {
		outcome.Skipped = true
		outcome.Reason = "declares none of the library's published identities: " + renderIdentities(identities)
		return outcome
	}
	before, err := engine.Git.TrackedChanges(ctx, consumer)
	if err != nil {
		outcome.Errors = append(outcome.Errors, err.Error())
		return outcome
	}

	goDeclarations, npmDeclarations := splitDeclarations(declarations)
	if len(goDeclarations) > 0 {
		links, err := engine.linkGo(ctx, library, consumer, goDeclarations, result.LibraryRepository, hash)
		if err != nil {
			outcome.Errors = append(outcome.Errors, err.Error())
		}
		outcome.Links = append(outcome.Links, links...)
	}
	for _, declaration := range npmDeclarations {
		link, err := engine.linkNpm(ctx, options, library, consumer, declaration, result.LibraryRepository, hash)
		if err != nil {
			outcome.Errors = append(outcome.Errors, err.Error())
			continue
		}
		outcome.Links = append(outcome.Links, link)
	}

	after, err := engine.Git.TrackedChanges(ctx, consumer)
	if err != nil {
		outcome.Errors = append(outcome.Errors, err.Error())
		return outcome
	}
	if introduced := newPaths(before, after); len(introduced) > 0 {
		outcome.Errors = append(outcome.Errors, fmt.Sprintf(
			"linking changed tracked files, which a local link must never do: %s", strings.Join(introduced, ", ")))
	}
	return outcome
}

func splitDeclarations(declarations []streams.Declaration) (goDeclarations, npmDeclarations []streams.Declaration) {
	for _, declaration := range declarations {
		if declaration.Identity.Ecosystem == streams.EcosystemGo {
			goDeclarations = append(goDeclarations, declaration)
			continue
		}
		npmDeclarations = append(npmDeclarations, declaration)
	}
	return goDeclarations, npmDeclarations
}

func renderIdentities(identities []streams.Identity) string {
	names := make([]string, 0, len(identities))
	for _, identity := range identities {
		names = append(names, string(identity.Ecosystem)+" "+identity.Name)
	}
	return strings.Join(names, ", ")
}

func newPaths(before, after []string) []string {
	existing := map[string]bool{}
	for _, path := range before {
		existing[path] = true
	}
	var introduced []string
	for _, path := range after {
		if !existing[path] {
			introduced = append(introduced, path)
		}
	}
	sort.Strings(introduced)
	return introduced
}

// resolveStream finds the stream that owns the first consumer worktree, so the
// links are recorded where `status`, `end` and the merge refusal already look.
func (engine *Engine) resolveStream(options Options) (stream, libraryRepository string, found bool, err error) {
	if engine.Store == nil {
		return "", "", false, nil
	}
	all, unreadable, err := engine.Store.List()
	if err != nil {
		return "", "", false, err
	}
	if len(unreadable) > 0 {
		// A stream WB could not read may hold the link this call is about to
		// create, so resolving "which stream records this" against a partial
		// view would silently record the link in the wrong place.
		names := make([]string, 0, len(unreadable))
		for _, broken := range unreadable {
			names = append(names, broken.Name+" ("+broken.Reason+")")
		}
		return "", "", false, fmt.Errorf(
			"stream state is unreadable for %s; fix or remove it before linking, so the link is recorded against the right stream",
			strings.Join(names, ", "))
	}
	library, err := filepath.Abs(options.Library)
	if err != nil {
		return "", "", false, err
	}
	for _, candidate := range all {
		if !candidate.Open() {
			continue
		}
		if options.Stream != "" && candidate.Name != options.Stream {
			continue
		}
		for _, member := range candidate.Members {
			if sameWorktree(member.Worktree, library) {
				return candidate.Name, member.Repository, true, nil
			}
		}
	}
	// The library may not be a member — a link into a repository outside the
	// stream is legal — so fall back to the stream holding a consumer.
	for _, candidate := range all {
		if !candidate.Open() {
			continue
		}
		if options.Stream != "" && candidate.Name != options.Stream {
			continue
		}
		for _, consumer := range options.Consumers {
			absolute, absErr := filepath.Abs(consumer)
			if absErr != nil {
				continue
			}
			for _, member := range candidate.Members {
				if sameWorktree(member.Worktree, absolute) {
					return candidate.Name, "", true, nil
				}
			}
		}
	}
	return "", "", false, nil
}

// recordLinks writes every link into stream state at the moment it is created.
// A link that cannot be recorded is a failure, not a warning: an unrecorded
// link is one `--undo` can never reverse and one the merge refusal will never
// see.
func (engine *Engine) recordLinks(result Result) error {
	if engine.Store == nil || result.Stream == "" {
		return nil
	}
	_, err := engine.Store.Update(result.Stream, func(stream *streams.Stream) error {
		for _, consumer := range result.Consumers {
			if len(consumer.Links) == 0 {
				continue
			}
			for index := range stream.Members {
				if !sameWorktree(stream.Members[index].Worktree, consumer.Consumer) {
					continue
				}
				stream.Members[index].Links = mergeLinks(stream.Members[index].Links, consumer.Links)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record links in stream %s: %w", result.Stream, err)
	}
	return nil
}

// mergeLinks replaces a link on the same identity and mechanism rather than
// appending a second one. Re-linking after the library moved must not leave the
// first record behind, because `--undo` would then restore a version that was
// already superseded.
func mergeLinks(existing, fresh []streams.Link) []streams.Link {
	merged := append([]streams.Link(nil), existing...)
	for _, link := range fresh {
		replaced := false
		for index := range merged {
			if merged[index].Identity == link.Identity && merged[index].Mechanism == link.Mechanism {
				// Keep the version recorded first: it is the published version
				// the consumer had before any link existed, and that is what
				// --undo must restore.
				link.PreviousVersion = merged[index].PreviousVersion
				merged[index] = link
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, link)
		}
	}
	return merged
}

func sameWorktree(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(path)
	}
	return resolve(left) == resolve(right)
}
