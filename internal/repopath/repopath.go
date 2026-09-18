// Package repopath models the canonical clone layout below a projects root:
// <root>/<host>/<org>/<repository>. The first level under the root is the
// literal forge hostname — never an alias — so a canonical clone's local path
// is invertible to its remote URL without reading any configuration or any
// repository remote.
//
// It is the single source of truth for three questions every part of WB has to
// answer the same way: what may be a literal forge hostname, where a clone
// belongs (from its coordinate or its clone URL), and which {owner}
// directories a projects root holds. Nothing here shells out to Git or opens a
// repository; Owners reads directory names only, never a remote.
package repopath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
)

// Address is one canonical clone address below a projects root. Host is the
// literal forge hostname (with an optional explicit port, e.g.
// "github.com:8443"); it is never an alias for a forge.
type Address struct {
	Host string
	Org  string
	Repo string
}

// RemoteURL is the remote URL the address inverts to. It is pure path
// arithmetic: WB derives it without reading any configuration or repository
// remote.
func (address Address) RemoteURL() string {
	return "https://" + address.Host + "/" + address.Org + "/" + address.Repo
}

// Relative is the address below the projects root: <host>/<org>/<repo>. A
// local-only origin names no host, and the relative form degrades to the
// legacy <org>/<repo> placement rather than inventing a forge level.
func (address Address) Relative() string {
	if address.Host == "" {
		return address.Org + "/" + address.Repo
	}
	return address.Host + "/" + address.Org + "/" + address.Repo
}

// Slug is the owner/repository identity without the host level. It is the
// identifier WB workflows carry in claims, work logs and filters.
func (address Address) Slug() string {
	return address.Org + "/" + address.Repo
}

// Path is the address's absolute location below root.
func (address Address) Path(root string) string {
	return filepath.Join(root, address.Host, address.Org, address.Repo)
}

// String renders the address the way it is written on disk.
func (address Address) String() string { return address.Relative() }

// EqualFold reports whether two addresses name the same clone, ignoring case
// (forges and GitHub owners are case-insensitive).
func (address Address) EqualFold(other Address) bool {
	return strings.EqualFold(address.Host, other.Host) &&
		strings.EqualFold(address.Org, other.Org) &&
		strings.EqualFold(address.Repo, other.Repo)
}

// ParseRelative parses a root-relative canonical clone path
// (<host>/<org>/<repo>) into an Address. A path whose first level is not a
// literal forge hostname is rejected: a first-level entry that is not a valid
// hostname must be reported as a layout finding, never silently treated as a
// forge.
func ParseRelative(relative string) (Address, error) {
	cleaned := strings.Trim(filepath.ToSlash(strings.TrimSpace(relative)), "/")
	parts := strings.Split(cleaned, "/")
	if len(parts) != 3 {
		return Address{}, fmt.Errorf("canonical clone path %q must be {host}/{org}/{repository}", relative)
	}
	address := Address{Host: parts[0], Org: parts[1], Repo: parts[2]}
	if !IsForgeHost(address.Host) {
		return Address{}, fmt.Errorf("canonical clone path %q does not start with a literal forge hostname", relative)
	}
	if !SafeSegment(address.Org, false) || !SafeSegment(address.Repo, true) {
		return Address{}, fmt.Errorf("canonical clone path %q has an unsafe org or repository segment", relative)
	}
	return address, nil
}

// FromLocalPath inverts an absolute local path below root into its Address.
func FromLocalPath(root, path string) (Address, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return Address{}, err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Address{}, fmt.Errorf("path %s is not below the projects root %s", path, root)
	}
	return ParseRelative(relative)
}

// RemoteURLForLocalPath inverts one canonical clone path below root to the
// remote URL it corresponds to. It confirms the shape of the path and the
// literal hostname, and reads nothing else.
func RemoteURLForLocalPath(root, path string) (string, error) {
	address, err := FromLocalPath(root, path)
	if err != nil {
		return "", err
	}
	return address.RemoteURL(), nil
}

// FromCloneURL returns the canonical clone address a repository belongs at,
// given the URL it is cloned from. A clone URL whose host is a literal forge
// hostname places the clone at <host>/<org>/<repo>; anything else — a local
// path remote, an unparseable URL, or a host that cannot be a directory name —
// keeps the legacy two-level <org>/<repo> placement.
//
// The host is never invented: it is read from the same URL the clone would be
// taken from, so a repository is never placed on a forge nobody named.
func FromCloneURL(cloneURL, org, repo string) Address {
	address := Address{Org: org, Repo: repo}
	parsed, err := gitremote.Parse(strings.TrimSpace(cloneURL))
	if err != nil {
		return address
	}
	host := parsed.Identity.Host()
	if !IsForgeHost(host) {
		return address
	}
	address.Host = host
	return address
}

// Owner is one {owner} level under a projects root, together with the literal
// forge host level it was reached through. Host is empty for the legacy
// two-level placement.
type Owner struct {
	Host string
	Name string
	Path string
}

// Relative is the owner's path below the projects root: {host}/{owner}, or
// {owner} alone for the legacy two-level placement.
func (owner Owner) Relative() string {
	if owner.Host == "" {
		return owner.Name
	}
	return owner.Host + "/" + owner.Name
}

// Owners lists the {owner} directories under root. A first-level entry that is
// a literal forge hostname is read through to its {org} level; every other
// first-level directory is the legacy {owner} level itself. Both shapes are
// returned together, so a fleet that has not adopted the host level stays fully
// discoverable and a migrated one is not read as empty.
//
// Directories that could not be read are returned in unreadable as
// "<path>: <error>" diagnostics rather than failing the walk, because one
// unreadable directory must never hide all the others.
func Owners(root string) (owners []Owner, unreadable []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("%s: %v", root, err)}
	}
	for _, entry := range entries {
		if !entry.IsDir() || !SafeOwnerSegment(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if !IsForgeHost(entry.Name()) {
			owners = append(owners, Owner{Name: entry.Name(), Path: path})
			continue
		}
		organizations, orgErr := os.ReadDir(path)
		if orgErr != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", path, orgErr))
			continue
		}
		for _, organization := range organizations {
			if !organization.IsDir() || !SafeOwnerSegment(organization.Name()) || strings.HasPrefix(organization.Name(), ".") {
				continue
			}
			owners = append(owners, Owner{
				Host: entry.Name(), Name: organization.Name(),
				Path: filepath.Join(path, organization.Name()),
			})
		}
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].Path < owners[j].Path })
	return owners, unreadable
}

// Locate returns the canonical clone address for one {owner}/{repository}
// coordinate below root.
//
// An existing clone is preferred: the literal host level first, then the legacy
// two-level placement, so a fleet that has not moved yet resolves to the clone
// it actually has rather than to a path nothing is at. When neither exists the
// legacy placement is predicted, because an unqualified coordinate carries no
// host to place it under — a host is knowable only from an existing clone or
// from a clone URL.
//
// The same owner/repository on more than one forge is genuinely ambiguous — it
// is much of why the host level exists — so it is refused rather than resolved
// by an arbitrary pick.
func Locate(root, org, repo string) (Address, error) {
	legacy := Address{Org: org, Repo: repo}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return legacy, nil
		}
		return Address{}, err
	}
	var matches []Address
	for _, entry := range entries {
		if !entry.IsDir() || !IsForgeHost(entry.Name()) {
			continue
		}
		candidate := Address{Host: entry.Name(), Org: org, Repo: repo}
		if info, statErr := os.Stat(filepath.Join(candidate.Path(root), ".git")); statErr == nil && info.IsDir() {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 0:
		return legacy, nil
	case 1:
		return matches[0], nil
	default:
		hosts := make([]string, 0, len(matches))
		for _, match := range matches {
			hosts = append(hosts, match.Host)
		}
		sort.Strings(hosts)
		return Address{}, fmt.Errorf("repository %s exists on more than one host (%s); pass the host-qualified {host}/%s",
			legacy.Slug(), strings.Join(hosts, ", "), legacy.Slug())
	}
}

// ClonePathForURL resolves where one repository's canonical clone is, or where
// it belongs when it does not exist yet.
//
// An existing clone is used where it is — the literal host level first, then
// the legacy two-level placement — so a caller never creates a second copy
// beside a clone the machine already has. When no clone exists the destination
// is the literal host level the clone URL names. This is the single place that
// decision is made; every package that has both a coordinate and a clone URL
// resolves through it.
func ClonePathForURL(root, org, repo, cloneURL string) (string, error) {
	existing, err := Locate(root, org, repo)
	if err != nil {
		return "", err
	}
	path := existing.Path(root)
	if info, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil && info.IsDir() {
		return path, nil
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	return FromCloneURL(cloneURL, org, repo).Path(root), nil
}

// SafeOwnerSegment reports whether name can be one first or owner level
// segment of a canonical clone below a projects root: an ordinary safe name
// with no leading dot, or a literal forge hostname — which may carry an
// explicit port, so ":" is accepted in this position and nowhere else.
//
// The two predicates must stay in step. A host level the placement rules
// accept but discovery filters out would make a clone that was deliberately
// created at <root>/github.com:8443/{org}/{repo} invisible to inventory,
// orphans and residue sweeps.
func SafeOwnerSegment(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	return SafeSegment(name, false) || IsForgeHost(name)
}

// IsForgeHost reports whether name is a literal forge hostname usable as the
// first path level under a projects root: a dotted DNS name with at least two
// labels whose final label is an alphabetic top-level domain, optionally
// followed by an explicit port.
//
// A bare owner name such as "dal-go" or "sneat-dev" is deliberately rejected.
// It is a syntactically valid single DNS label, so accepting it would let a
// legacy {owner}/{repository} directory masquerade as a forge — exactly the
// silent misread this requirement forbids.
func IsForgeHost(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 253 {
		return false
	}
	host := name
	if colon := strings.LastIndex(name, ":"); colon >= 0 {
		if !validPort(name[colon+1:]) {
			return false
		}
		host = name[:colon]
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for index, label := range labels {
		if !validHostLabel(label) {
			return false
		}
		if index == len(labels)-1 {
			// A top-level label is alphabetic and at least two characters
			// long; that is what separates "github.com" from a repository
			// directory that merely happens to contain a dot.
			for _, character := range label {
				if !isLetter(character) {
					return false
				}
			}
		}
	}
	return true
}

// SafeSegment mirrors the segment rules WB applies to the org and repository
// levels of a clone path. Repository segments may start with a dot so a
// dot-named canonical repository (for example "acme/.github") keeps working.
func SafeSegment(segment string, repository bool) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for index, character := range segment {
		letter := isLetter(character)
		digit := character >= '0' && character <= '9'
		if !letter && !digit && character != '.' && character != '_' && character != '-' {
			return false
		}
		leadable := letter || digit || repository && character == '.'
		if index == 0 && !leadable {
			return false
		}
	}
	return true
}

func validHostLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, character := range label {
		letter := isLetter(character)
		digit := character >= '0' && character <= '9'
		if !letter && !digit && character != '-' {
			return false
		}
	}
	return true
}

func validPort(port string) bool {
	if port == "" || len(port) > 5 {
		return false
	}
	for _, character := range port {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func isLetter(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}
