package deps

import (
	"fmt"
	"sort"
	"strings"
)

// unreadableCloneFailureFragments are git fatal-error fragments indicating
// that a local clone cannot be used as a git repository at all — no origin
// remote configured, or the directory is not a git repository — as distinct
// from a well-formed clone whose configured ref simply is not present yet
// (a rename or an unpublished branch, which a Go- or npm-manifest-bearing
// repository must still fail loudly for; see classifyGoGraphDiscoveryFailure
// and classifyNpmGraphDiscoveryFailure).
//
// Production hit this exactly once with a 132-repository `wb deps bump go
// --fleet` run: one local clone (sneat-co/sneat-payments) had no 'origin'
// remote configured, `git fetch --quiet origin` failed with "fatal: 'origin'
// does not appear to be a git repository", and the whole campaign aborted
// instead of the other 131 repositories being able to proceed. A clone in
// this state needs manual repair (reclone, fix the remote) regardless of
// what it contains locally, so continuing to hard-fail the entire fleet over
// it is not actionable from inside the campaign — it is skipped and reported
// instead, exactly like a repository proven irrelevant by a local scan.
var unreadableCloneFailureFragments = []string{
	"does not appear to be a git repository",
	"not a git repository",
	"no such remote",
}

// looksLikeUnreadableClone reports whether cause is the class of failure
// where the local clone itself cannot be treated as a usable git repository,
// as opposed to a healthy clone whose configured ref is merely unavailable.
func looksLikeUnreadableClone(cause error) bool {
	if cause == nil {
		return false
	}
	message := strings.ToLower(cause.Error())
	for _, fragment := range unreadableCloneFailureFragments {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

// fleetRequirement is an ecosystem-neutral projection of one manifest-owned
// dependency requirement. It is the narrow slice of evidence the bump wave
// engine needs when it has to traverse an already-published consumer without
// depending on either ecosystem's own richer requirement type.
type fleetRequirement struct {
	ConsumerModule string
	Repository     string
	Version        string
}

// coalescedCampaignTargets chooses the first provider-first layer after
// excluding repositories that own explicit campaign seeds. A seed records a
// release that has already happened: its owning repository is a fixed root of
// this campaign, not a consumer to update again when a later carrier release
// happens to flow back into it. Edges into a fixed root therefore stop this
// campaign's traversal; every non-root consumer remains eligible.
func coalescedCampaignTargets(allTargets map[string][]Target, adjacency map[string][]string, consumerRoots, fixedRoots map[string]bool) (map[string][]Target, []string) {
	for repository := range fixedRoots {
		delete(allTargets, repository)
	}
	if len(allTargets) == 0 {
		return nil, nil
	}
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(repository string) {
		if fixedRoots[repository] || reachable[repository] {
			return
		}
		reachable[repository] = true
		for _, consumer := range adjacency[repository] {
			visit(consumer)
		}
	}
	for repository := range consumerRoots {
		visit(repository)
	}
	restricted := map[string][]string{}
	for repository := range reachable {
		for _, consumer := range adjacency[repository] {
			if reachable[consumer] {
				restricted[repository] = append(restricted[repository], consumer)
			}
		}
		if _, exists := restricted[repository]; !exists {
			restricted[repository] = nil
		}
	}
	levels := map[string]int{}
	for _, component := range topologicalLayers(restricted) {
		for _, repository := range component.nodes {
			levels[repository] = component.level
		}
	}
	firstLevel := int(^uint(0) >> 1)
	for repository := range allTargets {
		level, exists := levels[repository]
		if !exists {
			level = 0
		}
		if level < firstLevel {
			firstLevel = level
		}
	}
	selected := map[string][]Target{}
	var deferred []string
	for repository, targets := range allTargets {
		level, exists := levels[repository]
		if !exists {
			level = 0
		}
		if level == firstLevel {
			selected[repository] = targets
		} else {
			deferred = append(deferred, repository)
		}
	}
	sort.Strings(deferred)
	return selected, deferred
}

// validateAcyclicCampaignPropagation rejects a cycle that this campaign must
// order. An edge that returns to a fixed seed owner is deliberately not such a
// cycle: the owner has already published the seed and is never scheduled as a
// consumer in this campaign. Traversal still starts at fixed roots so a real
// cycle entirely among their downstream consumers continues to fail closed.
func validateAcyclicCampaignPropagation(adjacency map[string][]string, roots, fixedRoots map[string]bool) error {
	reachable := map[string]bool{}
	var visitReachable func(string)
	visitReachable = func(repository string) {
		if reachable[repository] {
			return
		}
		reachable[repository] = true
		for _, consumer := range adjacency[repository] {
			if !fixedRoots[consumer] {
				visitReachable(consumer)
			}
		}
	}
	for repository := range roots {
		visitReachable(repository)
	}
	state := map[string]uint8{}
	stack := make([]string, 0, len(reachable))
	stackIndex := map[string]int{}
	var cycle []string
	var visitCycle func(string) bool
	visitCycle = func(repository string) bool {
		state[repository] = 1
		stackIndex[repository] = len(stack)
		stack = append(stack, repository)
		consumers := make([]string, 0, len(adjacency[repository]))
		for _, consumer := range adjacency[repository] {
			if reachable[consumer] && !fixedRoots[consumer] {
				consumers = append(consumers, consumer)
			}
		}
		sort.Strings(consumers)
		for _, consumer := range consumers {
			switch state[consumer] {
			case 0:
				if visitCycle(consumer) {
					return true
				}
			case 1:
				cycle = append(cycle, stack[stackIndex[consumer]:]...)
				cycle = append(cycle, consumer)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackIndex, repository)
		state[repository] = 2
		return false
	}
	repositories := make([]string, 0, len(reachable))
	for repository := range reachable {
		repositories = append(repositories, repository)
	}
	sort.Strings(repositories)
	for _, repository := range repositories {
		if state[repository] == 0 && visitCycle(repository) {
			return fmt.Errorf("dependency propagation cycle requires a coordinated release protocol: %s", strings.Join(cycle, " -> "))
		}
	}
	return nil
}

// bumpFleetGraph is the fleet dependency evidence the bump wave engine needs
// from one scan, regardless of which ecosystem produced it. goFleetGraph
// (internal/deps/go_graph.go) and npmFleetGraph (internal/deps/npm_graph.go)
// both satisfy it structurally; neither type depends on the other, and
// go_graph.go is otherwise unmodified by npm support — every method below
// that goFleetGraph already had keeps its exact original behavior.
type bumpFleetGraph interface {
	// Skips lists repositories excluded from the walk rather than inspected.
	Skips() []GraphDiscoverySkip
	// BaseRefFallbacks lists repositories that were fully discovered using
	// their actual default branch because the operation's configured base
	// ref did not exist for them (see orchestrate.EnsureCanonical).
	BaseRefFallbacks() []GraphDefaultBranchFallback
	// ManifestWarnings lists manifest files that could not be parsed but did
	// not abort discovery because they are not a repository's root manifest.
	ManifestWarnings() []GraphManifestWarning
	// AmbiguousModules lists modules declared by more than one repository
	// whose conflict was deterministically resolved instead of aborting the
	// fleet.
	AmbiguousModules() []GraphAmbiguousModuleWarning
	// validateUniqueModuleDeclarations rejects a graph where the same
	// published identity is declared by more than one repository, since a
	// mutation could otherwise land on the wrong one.
	validateUniqueModuleDeclarations() error
	// validateAcyclicPropagation rejects a relevant cross-repository
	// dependency cycle before any worktree is created.
	validateAcyclicPropagation(events []ReleaseEvent) error
	// coalescedRepositoriesForEvents selects only the earliest pending
	// provider-first layer for a set of release events.
	coalescedRepositoriesForEvents(seedEvents, events []ReleaseEvent) (map[string][]Target, []string)
	// pendingCarriersBlockTargets reports whether an unpublished, already-
	// current provider is upstream of the selected targets.
	pendingCarriersBlockTargets(carriers []ReleaseObservation, targets map[string][]Target) bool
	// affectedModules maps each repository whose targets changed to the
	// modules it declares that a downstream repository might consume.
	affectedModules(targetsByRepository map[string][]Target) map[string]map[string]bool
	// hasExternalConsumers reports whether any requirement of modulePath
	// comes from a repository other than repository itself.
	hasExternalConsumers(modulePath, repository string) bool
	// requirementsForDependency lists every requirement of one dependency
	// identity across the fleet, in the shared ecosystem-neutral shape.
	requirementsForDependency(dependency string) []fleetRequirement
}
