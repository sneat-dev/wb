package worktreeclaims

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const PullRequestBindingSuffix = ".pull_request.json"
const UmbrellaBindingSuffix = ".umbrella.json"

type ClaimPullRequestBinding struct {
	Repository  string    `json:"repository"`
	PullRequest int       `json:"pull_request,omitempty"`
	URL         string    `json:"url"`
	LandedSHA   string    `json:"landed_sha,omitempty"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// ClaimUmbrellaBinding records an umbrella reference attached to an existing
// immutable claim (e.g. via `wb worktree set --umbrella`).
type ClaimUmbrellaBinding struct {
	Umbrella   string    `json:"umbrella"`
	RecordedAt time.Time `json:"recorded_at"`
}
type RegisteredPullRequestBinding struct {
	Task        string
	ClaimID     string
	Repository  string
	PullRequest int
	URL         string
	RecordedAt  time.Time
}
type ActiveClaim struct {
	Task, ClaimID, Path string
}
type BindingPorts struct {
	Root       func(string) (string, error)
	Active     func(string, string) (ActiveClaim, error)
	Homes      func(string) ([]string, error)
	Walk       func(string, func(*os.File, string, string)) error
	ReadJSONAt func(*os.File, string, any) error
}

func (p BindingPorts) RecordClaimPullRequestBinding(projectsRoot, worktree string, binding ClaimPullRequestBinding) (task, claimID string, err error) {
	home, err := p.Root(projectsRoot)
	if err != nil {
		return "", "", err
	}
	claim, err := p.Active(home, worktree)
	if err != nil {
		return "", "", fmt.Errorf("resolve active work-log claim for %s: %w", worktree, err)
	}
	if err := ValidatePullRequestBindingFields(binding); err != nil {
		return "", "", err
	}
	if binding.RecordedAt.IsZero() {
		binding.RecordedAt = time.Now().UTC()
	} else {
		binding.RecordedAt = binding.RecordedAt.UTC()
	}
	encoded, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode task-to-pull-request binding: %w", err)
	}
	sidecar := strings.TrimSuffix(claim.Path, ".json") + PullRequestBindingSuffix
	if err := os.WriteFile(sidecar, append(encoded, '\n'), 0o600); err != nil {
		return "", "", fmt.Errorf("record task-to-pull-request binding: %w", err)
	}
	return claim.Task, claim.ClaimID, nil
}
func (p BindingPorts) ListRegisteredPullRequestBindings(projectsRoot string) ([]RegisteredPullRequestBinding, error) {
	homes, err := p.Homes(projectsRoot)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(homes))
	result := make([]RegisteredPullRequestBinding, 0)
	for _, path := range homes {
		home := filepath.Clean(path)
		if seen[home] {
			continue
		}
		seen[home] = true
		bindings, err := p.ListRegisteredPullRequestBindingsInHome(home)
		if err != nil {
			return nil, err
		}
		result = append(result, bindings...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Repository != result[j].Repository {
			return result[i].Repository < result[j].Repository
		}
		if result[i].Task != result[j].Task {
			return result[i].Task < result[j].Task
		}
		return result[i].ClaimID < result[j].ClaimID
	})
	return result, nil
}
func (p BindingPorts) ListRegisteredPullRequestBindingsInHome(home string) ([]RegisteredPullRequestBinding, error) {
	result := make([]RegisteredPullRequestBinding, 0)
	err := p.Walk(home, func(claims *os.File, claimID, task string) {
		var binding ClaimPullRequestBinding
		if p.ReadJSONAt(claims, claimID+PullRequestBindingSuffix, &binding) != nil {
			return
		}
		result = append(result, RegisteredPullRequestBinding{
			Task: task, ClaimID: claimID, Repository: binding.Repository,
			PullRequest: binding.PullRequest, URL: binding.URL, RecordedAt: binding.RecordedAt,
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ValidatePullRequestBindingFields refuses a binding no reader could accept:
// one without a repository, a positive pull-request number, and a URL that
// ends in that pull request's number. The writer checks it before persisting so a
// corrupt sidecar is never written (sneat-dev/wb#1007).
func ValidatePullRequestBindingFields(binding ClaimPullRequestBinding) error {
	if strings.TrimSpace(binding.Repository) == "" || binding.PullRequest <= 0 ||
		!strings.HasSuffix(strings.TrimSpace(binding.URL), "/pull/"+strconv.Itoa(binding.PullRequest)) {
		return fmt.Errorf("refusing to record pull request binding with repository %q, number %d, URL %q", binding.Repository, binding.PullRequest, binding.URL)
	}
	return nil
}

// ValidatePullRequestBindingEntry proves that name, listed in a claims
// directory whose entries are claimFiles, is the registered pull request
// sidecar of a listed sibling claim, read below the held descriptor.
func ValidatePullRequestBindingEntry(claims *os.File, name string, claimFiles map[string]bool, readJSON func(*os.File, string, any) error) error {
	claimID := strings.TrimSuffix(name, PullRequestBindingSuffix)
	if name != claimID+PullRequestBindingSuffix || !ValidClaimID(claimID) || !claimFiles[claimID+".json"] || readJSON == nil {
		return fmt.Errorf("orphaned or unsafe pull request binding %q", name)
	}
	return readValidatedClaimPullRequestBinding(claims, claimID, readJSON)
}

// readValidatedClaimPullRequestBinding reads only below the caller's held
// claims descriptor and proves that owned metadata belongs to its sibling.
func readValidatedClaimPullRequestBinding(claims *os.File, claimID string, readJSON func(*os.File, string, any) error) error {
	var binding ClaimPullRequestBinding
	if err := readJSON(claims, claimID+PullRequestBindingSuffix, &binding); err != nil {
		return err
	}
	var boundClaim Claim
	if err := readJSON(claims, claimID+".json", &boundClaim); err != nil {
		return err
	}
	if boundClaim.ClaimID != claimID || binding.Repository != boundClaim.Repository || ValidatePullRequestBindingFields(binding) != nil || binding.RecordedAt.IsZero() {
		return fmt.Errorf("invalid pull request binding for claim %s", claimID)
	}
	return nil
}

// ValidateUmbrellaBindingFields verifies that an umbrella sidecar carries a
// non-empty typed reference "<registry>:<id>" without control characters.
func ValidateUmbrellaBindingFields(binding ClaimUmbrellaBinding) error {
	ref := strings.TrimSpace(binding.Umbrella)
	if ref == "" || !strings.Contains(ref, ":") || strings.ContainsAny(ref, " \t\r\n\x00") {
		return fmt.Errorf("refusing to record umbrella binding with reference %q", binding.Umbrella)
	}
	return nil
}

// ValidateUmbrellaBindingEntry proves that name, listed in a claims directory
// whose entries are claimFiles, is the registered umbrella sidecar of a listed
// sibling claim, read below the held descriptor.
func ValidateUmbrellaBindingEntry(claims *os.File, name string, claimFiles map[string]bool, readJSON func(*os.File, string, any) error) error {
	claimID := strings.TrimSuffix(name, UmbrellaBindingSuffix)
	if name != claimID+UmbrellaBindingSuffix || !ValidClaimID(claimID) || !claimFiles[claimID+".json"] || readJSON == nil {
		return fmt.Errorf("orphaned or unsafe umbrella binding %q", name)
	}
	return readValidatedClaimUmbrellaBinding(claims, claimID, readJSON)
}

func readValidatedClaimUmbrellaBinding(claims *os.File, claimID string, readJSON func(*os.File, string, any) error) error {
	var binding ClaimUmbrellaBinding
	if err := readJSON(claims, claimID+UmbrellaBindingSuffix, &binding); err != nil {
		return err
	}
	var boundClaim Claim
	if err := readJSON(claims, claimID+".json", &boundClaim); err != nil {
		return err
	}
	if boundClaim.ClaimID != claimID || ValidateUmbrellaBindingFields(binding) != nil || binding.RecordedAt.IsZero() {
		return fmt.Errorf("invalid umbrella binding for claim %s", claimID)
	}
	return nil
}
