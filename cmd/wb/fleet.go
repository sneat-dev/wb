package main

import (
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/discover"
)

// fleetOwners returns the authenticated user's login plus every org they
// belong to, plus any extraOrgs, as an owner set for discovery.
func fleetOwners(extraOrgs []string) []string {
	set := map[string]bool{}
	if user, err := discover.AuthUser(); err == nil && user != "" {
		set[user] = true
	}
	if orgs, err := discover.MemberOrgs(); err == nil {
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
func fleet(projectsRoot, filter string, resolveOwners func() []string) ([]discover.Repo, error) {
	var (
		wg       sync.WaitGroup
		local    []discover.Repo
		localErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		local, localErr = discover.ScanLocal(projectsRoot)
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
			repos, err := discover.ListRemote(owner)
			if err != nil {
				return // owner may not be accessible; local data still used
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

	all := discover.Reconcile(local, remote)
	var out []discover.Repo
	for _, r := range all {
		if filter != "" && !strings.Contains(r.Slug(), filter) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
