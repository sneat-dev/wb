package streams

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Status is the whole-stream report.
//
// The three gaps are reported separately and named per repository, because a
// merged-but-untagged library is not the same problem as a consumer left
// behind, and collapsing them would hide which one is blocking the stream.
//
// Implements: dependency-streams#req:stream-status-reports-the-three-gaps,
// dependency-streams#req:stream-backlog-is-counted-by-patch-identity.
type Status struct {
	Stream string `json:"stream"`
	Open   bool   `json:"open"`
	// Phase distinguishes a stream still being created from a usable one, so
	// an interrupted start is visible rather than looking like a healthy
	// stream with missing pull requests.
	Phase   Phase          `json:"phase"`
	Library string         `json:"library,omitempty"`
	Branch  string         `json:"branch"`
	Members []MemberStatus `json:"members"`
	// LinkedConsumers is gap one: consumers holding a live local link, and
	// the library worktree each is linked to.
	LinkedConsumers []LinkedConsumer `json:"linked_consumers"`
	// MergedUntagged is gap two: library changes that are merged into the
	// base but carry no tag yet.
	MergedUntagged *MergedUntagged `json:"merged_untagged,omitempty"`
	// ConsumersBehind is gap three: consumers still declaring a version
	// older than the library's newest published version.
	ConsumersBehind []ConsumerBehind `json:"consumers_behind"`
	// OpenAgentPullRequests are the pull requests targeting the stream
	// branch. They are the ones GitHub would silently retarget at the base
	// if the stream branch were deleted with them still open.
	OpenAgentPullRequests []AgentPullRequest `json:"open_agent_pull_requests"`
	// Unknowns record every question this report could not answer, so an
	// empty gap list never has to be read as "nothing is wrong".
	Unknowns []string `json:"unknowns,omitempty"`
}

// MemberStatus is one member's own row.
type MemberStatus struct {
	Repository     string `json:"repository"`
	Role           Role   `json:"role"`
	Worktree       string `json:"worktree"`
	Branch         string `json:"branch"`
	Base           string `json:"base"`
	PullRequest    int    `json:"pull_request,omitempty"`
	PullRequestURL string `json:"pull_request_url,omitempty"`
	// PullRequestMissing carries the reason no draft pull request exists.
	PullRequestMissing string `json:"pull_request_missing,omitempty"`
	// Unabsorbed is the number of commits on the stream branch that the base
	// does not carry by patch identity.
	Unabsorbed int `json:"unabsorbed"`
	// UnabsorbedClusters names each cluster of patch-identical work, so N
	// branches carrying one body of work read as one item rather than N. The
	// label is the commit subject; the IDENTITY is the patch id, so two
	// commits sharing a message are not collapsed and one change re-applied
	// with an edited message still is.
	UnabsorbedClusters []string `json:"unabsorbed_clusters,omitempty"`
	LeaseHolder        string   `json:"lease_holder,omitempty"`
	RecordedHead       string   `json:"recorded_head,omitempty"`
	LiveLinks          int      `json:"live_links"`
}

// LinkedConsumer is gap one.
type LinkedConsumer struct {
	Repository      string    `json:"repository"`
	Worktree        string    `json:"worktree"`
	Library         string    `json:"library"`
	Mechanism       Mechanism `json:"mechanism"`
	Identity        string    `json:"identity"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	ContentHash     string    `json:"content_hash,omitempty"`
}

// MergedUntagged is gap two.
type MergedUntagged struct {
	Repository string   `json:"repository"`
	Base       string   `json:"base"`
	LatestTag  string   `json:"latest_tag,omitempty"`
	Commits    []string `json:"commits"`
}

// ConsumerBehind is gap three.
type ConsumerBehind struct {
	Repository string `json:"repository"`
	Identity   string `json:"identity"`
	Manifest   string `json:"manifest"`
	Declared   string `json:"declared"`
	Published  string `json:"published"`
}

// AgentPullRequest is one open pull request against the stream branch.
type AgentPullRequest struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Head       string `json:"head"`
}

// Status reconstructs the stream from WB-owned state rather than from
// repository contents, so it answers after an interrupted session.
func (engine *Engine) Status(ctx context.Context, name string) (Status, error) {
	stream, err := engine.Store.Load(name)
	if err != nil {
		return Status{}, err
	}
	status := Status{Stream: stream.Name, Open: stream.Open(), Phase: stream.Lifecycle(), Branch: Branch(stream.Name)}
	if library, ok := stream.Library(); ok {
		status.Library = library.Repository
	}
	for _, member := range stream.Members {
		// `stream-verbs-re-read-state-before-mutating` covers origin as well
		// as WB state: a day-old clone would make gap 2 and gap 3 report
		// against a stale published version, and would show work as
		// unabsorbed that the base already carries.
		if member.Worktree != "" {
			if fetchErr := engine.Git.Fetch(ctx, member.Worktree); fetchErr != nil {
				status.Unknowns = append(status.Unknowns, fmt.Sprintf(
					"%s: could not re-read origin, so everything below is from a possibly stale local clone: %v",
					member.Repository, RedactString(fetchErr.Error())))
			}
		}
		status.Members = append(status.Members, engine.memberStatus(ctx, &status, member))
		for _, link := range member.Links {
			status.LinkedConsumers = append(status.LinkedConsumers, LinkedConsumer{
				Repository:      member.Repository,
				Worktree:        member.Worktree,
				Library:         link.Library,
				Mechanism:       link.Mechanism,
				Identity:        link.Identity,
				PreviousVersion: link.PreviousVersion,
				ContentHash:     link.ContentHash,
			})
		}
		pullRequests, err := engine.GitHub.OpenPullRequestsTargeting(ctx, member.Worktree, member.Branch)
		if err != nil {
			status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s: open agent pull requests: %v", member.Repository, err))
		}
		for _, pullRequest := range pullRequests {
			status.OpenAgentPullRequests = append(status.OpenAgentPullRequests, AgentPullRequest{
				Repository: member.Repository, Number: pullRequest.Number,
				URL: pullRequest.URL, Title: pullRequest.Title, Head: pullRequest.Head,
			})
		}
	}
	engine.libraryGaps(ctx, stream, &status)
	return status, nil
}

func (engine *Engine) memberStatus(ctx context.Context, status *Status, member Member) MemberStatus {
	row := MemberStatus{
		Repository: member.Repository, Role: member.Role, Worktree: member.Worktree,
		Branch: member.Branch, Base: member.Base,
		PullRequest: member.PullRequest, PullRequestURL: member.PullRequestURL,
		PullRequestMissing: member.PullRequestError,
		LeaseHolder:        member.Lease.Holder(),
		RecordedHead:       member.Lease.RecordedHead,
		LiveLinks:          len(member.Links),
	}
	commits, err := engine.Git.CommitsNotIn(ctx, member.Worktree, member.Branch, "origin/"+member.Base)
	if err != nil {
		status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s: unabsorbed commits: %v", member.Repository, RedactString(err.Error())))
		return row
	}
	row.UnabsorbedClusters = collapsePatchIdentical(commits)
	row.Unabsorbed = len(commits)
	return row
}

// collapsePatchIdentical names a cluster of commits carrying one body of work
// once, with its cardinality, rather than once per commit.
//
// Clustering is on Git's own `patch-id --stable`, never on subject text: two
// unrelated commits can share a message, and one change re-applied on another
// branch can carry an edited one. A commit with no patch id falls back to its
// SHA, so two commits WB could not compare never collapse by accident.
func collapsePatchIdentical(commits []Commit) []string {
	counts := map[string]int{}
	labels := map[string]string{}
	var order []string
	for _, commit := range commits {
		identity := commit.Identity()
		if counts[identity] == 0 {
			order = append(order, identity)
			label := commit.Subject
			if label == "" {
				label = commit.SHA
			}
			labels[identity] = label
		}
		counts[identity]++
	}
	collapsed := make([]string, 0, len(order))
	for _, identity := range order {
		if counts[identity] == 1 {
			collapsed = append(collapsed, labels[identity])
			continue
		}
		collapsed = append(collapsed, fmt.Sprintf("%s (×%d patch-identical)", labels[identity], counts[identity]))
	}
	return collapsed
}

func (engine *Engine) libraryGaps(ctx context.Context, stream Stream, status *Status) {
	library, ok := stream.Library()
	if !ok {
		status.Unknowns = append(status.Unknowns, "stream has no library member; merged-untagged and behind-consumer gaps cannot be computed")
		return
	}
	identities, err := DiscoverPublished(library.Worktree)
	if err != nil {
		status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s: published identities: %v", library.Repository, err))
		return
	}
	if len(identities) == 0 {
		status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s publishes no discoverable Go module or npm package", library.Repository))
		return
	}
	// A repository carrying tags for several modules (`backend/v…` alongside
	// `cli/v…`) would otherwise report "the library's newest published
	// version" from an unrelated module and mis-report gap 3.
	pattern := libraryTagPattern(identities)
	tags, err := engine.Git.Tags(ctx, library.Worktree, pattern)
	if err != nil {
		status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s: tags: %v", library.Repository, RedactString(err.Error())))
		return
	}
	latest := newestTag(tags)
	commits, err := engine.Git.LogSubjects(ctx, library.Worktree, latest, "origin/"+library.Base)
	if err != nil {
		status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s: merged-but-untagged commits: %v", library.Repository, err))
	} else if len(commits) > 0 {
		status.MergedUntagged = &MergedUntagged{
			Repository: library.Repository, Base: library.Base, LatestTag: latest, Commits: commits,
		}
	}
	published := tagVersion(latest)
	if published == "" {
		status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s carries no version tag; consumers cannot be compared against a published version", library.Repository))
		return
	}
	for _, member := range stream.Consumers() {
		declarations, err := DiscoverDeclarations(member.Worktree, identities)
		if err != nil {
			status.Unknowns = append(status.Unknowns, fmt.Sprintf("%s: declared versions: %v", member.Repository, err))
			continue
		}
		for _, declaration := range declarations {
			if versionAtLeast(declaration.Version, published) {
				continue
			}
			status.ConsumersBehind = append(status.ConsumersBehind, ConsumerBehind{
				Repository: member.Repository,
				Identity:   declaration.Identity.Name,
				Manifest:   declaration.Manifest,
				Declared:   declaration.Version,
				Published:  published,
			})
		}
	}
	sort.Slice(status.ConsumersBehind, func(i, j int) bool {
		if status.ConsumersBehind[i].Repository != status.ConsumersBehind[j].Repository {
			return status.ConsumersBehind[i].Repository < status.ConsumersBehind[j].Repository
		}
		return status.ConsumersBehind[i].Identity < status.ConsumersBehind[j].Identity
	})
}

// libraryTagPattern narrows the tag list to the library's own module. A Go
// module under `backend/` is tagged `backend/vX.Y.Z`; a module at the root is
// tagged `vX.Y.Z`.
func libraryTagPattern(identities []Identity) string {
	for _, identity := range identities {
		if identity.Ecosystem != EcosystemGo {
			continue
		}
		if identity.Directory == "." || identity.Directory == "" {
			return "v*"
		}
		return identity.Directory + "/v*"
	}
	return "v*"
}

// newestTag picks the first tag Git's version ordering returned. Tags are
// already sorted newest-first by the port, so this only guards an empty list.
func newestTag(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return tags[0]
}

// tagVersion strips a module-path prefix from a tag: `backend/v0.5.0` and
// `v0.5.0` both describe version v0.5.0.
func tagVersion(tag string) string {
	if tag == "" {
		return ""
	}
	version := path.Base(tag)
	if !strings.HasPrefix(version, "v") {
		return ""
	}
	return version
}

// versionAtLeast compares a declared dependency version against a published
// one. Declared versions carry range prefixes (`^`, `~`, `>=`), which are
// stripped before comparison; anything that cannot be compared is treated as
// not behind, because reporting a consumer as behind on a version WB could not
// read would be a false finding.
func versionAtLeast(declared, published string) bool {
	declaredParts, ok := semverParts(declared)
	if !ok {
		return true
	}
	publishedParts, ok := semverParts(published)
	if !ok {
		return true
	}
	for index := 0; index < 3; index++ {
		if declaredParts[index] == publishedParts[index] {
			continue
		}
		return declaredParts[index] > publishedParts[index]
	}
	return true
}

func semverParts(value string) ([3]int, bool) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimLeft(trimmed, "^~>=< ")
	trimmed = strings.TrimPrefix(trimmed, "v")
	if index := strings.IndexAny(trimmed, "-+"); index >= 0 {
		trimmed = trimmed[:index]
	}
	fields := strings.Split(trimmed, ".")
	if len(fields) < 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for index := 0; index < 3; index++ {
		// strconv.Atoi rejects trailing junk that fmt.Sscanf("%d") would
		// silently accept, so "1.2.3junk" is unreadable rather than 1.2.3.
		number, err := strconv.Atoi(fields[index])
		if err != nil || number < 0 {
			return [3]int{}, false
		}
		parts[index] = number
	}
	return parts, true
}
