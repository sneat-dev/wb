package main

import (
	"os"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetdiscovery"
)

// These temporary adapters retain actual run, sync and dependency consumers.
func fleetOwners(extra []string) []string { return fleetdiscovery.New(os.Stderr).Owners(extra) }
func fleet(root, filter string, owners func() []string) ([]discover.Repo, error) {
	return fleetdiscovery.New(os.Stderr).Discover(root, filter, owners)
}
