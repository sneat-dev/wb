package repopath

import "strings"

// Helpers kept for tests only: no production caller remains.

// EqualFold reports whether two addresses name the same clone, ignoring case
// (forges and GitHub owners are case-insensitive).
func (address Address) EqualFold(other Address) bool {
	return strings.EqualFold(address.Host, other.Host) &&
		strings.EqualFold(address.Org, other.Org) &&
		strings.EqualFold(address.Repo, other.Repo)
}
