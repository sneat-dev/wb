// Package fleetdiscovery reconciles local scans with best-effort remote inventory.
package fleetdiscovery

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/discover"
)

// fleetOwners returns the authenticated user's login plus every org they
// belong to, plus any extraOrgs, as an owner set for discovery.
func (resolver Resolver) Owners(extraOrgs []string) []string {
	set := map[string]bool{}
	if user, err := resolver.AuthUser(); err == nil && user != "" {
		set[user] = true
	}
	if orgs, err := resolver.MemberOrgs(); err == nil {
		for _, o := range orgs {
			set[o] = true
		}
	}
	for _, o := range extraOrgs {
		set[o] = true
	}
	owners := make([]string, 0, len(set))
	for o := range set {
		owners = append(owners, o)
	}
	return owners
}

// fleet discovers and reconciles repos for the owners resolveOwners
// returns, filtered by a substring match on org/name. resolveOwners is
// called after the local scan starts, so remote owner resolution overlaps
// the local disk walk.
func (resolver Resolver) Discover(projectsRoot, filter string, resolveOwners func() []string) ([]discover.Repo, error) {
	var (
		wg       sync.WaitGroup
		local    []discover.Repo
		localErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		local, localErr = resolver.ScanLocal(projectsRoot)
	}()
	owners := resolveOwners()
	wg.Wait()
	if localErr != nil {
		return nil, localErr
	}

	var (
		rwg         sync.WaitGroup
		mu          sync.Mutex
		remoteByOrg = map[string][]discover.Repo{}
	)
	for _, owner := range owners {
		rwg.Add(1)
		go func(owner string) {
			defer rwg.Done()
			repos, err := resolver.ListRemote(owner)
			if err != nil {
				// owner may not be accessible; local data still used, but say
				// so — otherwise repos that only exist remotely for this
				// owner (new repos included) silently vanish from the run.
				_, _ = fmt.Fprintf(resolver.Diagnostics, "wb: could not list repos for %s, using local data only: %v\n", owner, err)
				return
			}
			mu.Lock()
			remoteByOrg[owner] = repos
			mu.Unlock()
		}(owner)
	}
	rwg.Wait()

	var remote []discover.Repo
	for _, repos := range remoteByOrg {
		remote = append(remote, repos...)
	}

	all := resolver.Reconcile(local, remote)
	var out []discover.Repo
	for _, r := range all {
		if filter != "" && !strings.Contains(r.Slug(), filter) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Resolver holds immutable function bindings and one diagnostic stream.
type Resolver struct {
	AuthUser    func() (string, error)
	MemberOrgs  func() ([]string, error)
	ScanLocal   func(string) ([]discover.Repo, error)
	ListRemote  func(string) ([]discover.Repo, error)
	Reconcile   func([]discover.Repo, []discover.Repo) []discover.Repo
	Diagnostics io.Writer
}

func New(diagnostics io.Writer) Resolver {
	return Resolver{AuthUser: discover.AuthUser, MemberOrgs: discover.MemberOrgs, ScanLocal: discover.ScanLocal, ListRemote: discover.ListRemote, Reconcile: discover.Reconcile, Diagnostics: diagnostics}
}
