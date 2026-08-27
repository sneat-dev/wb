// Package gitremote validates the portable, credential-free identity of Git
// remotes carried across WB session-move boundaries.
package gitremote

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
)

// Remote is one safe Git remote spelling and its normalized logical identity.
// Raw is safe to persist because Parse rejects credentials, query strings,
// fragments, encoded paths, whitespace, and option-like arguments.
type Remote struct {
	Raw      string
	Identity Identity
}

// Identity binds a repository slug to either a hosted Git service or one
// exact clean local path. Fields other than Repository remain private so
// diagnostics cannot accidentally expose a local path as a remote URL.
type Identity struct {
	Repository string
	host       string
	localPath  string
}

// Host reports the lowercase host of a hosted remote, including any explicit
// port, and returns an empty string for a local remote. Only the host is
// exposed: a local remote's path stays private so diagnostics can never
// present it as a remote URL.
func (identity Identity) Host() string { return identity.host }

// Equal reports whether two safe remote spellings identify the same logical
// repository. SSH and HTTPS spellings on the same host compare equal; local
// remotes compare only when their clean absolute paths are exactly equal.
func (identity Identity) Equal(other Identity) bool {
	return identity.Repository == other.Repository && identity.host == other.host && identity.localPath == other.localPath
}

// Parse validates one fixed, credential-free Git remote argument.
func Parse(raw string) (Remote, error) {
	if raw != strings.TrimSpace(raw) || raw == "" || strings.HasPrefix(raw, "-") ||
		strings.IndexFunc(raw, unicode.IsSpace) >= 0 || strings.ContainsRune(raw, '\x00') {
		return Remote{}, fmt.Errorf("repository remote is not one fixed safe argument")
	}
	if filepath.IsAbs(raw) {
		identity, err := identityFromAbsolutePath(raw)
		return Remote{Raw: raw, Identity: identity}, err
	}
	if !strings.Contains(raw, "://") && strings.Count(raw, ":") == 1 {
		identity, err := identityFromSCP(raw)
		return Remote{Raw: raw, Identity: identity}, err
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return Remote{}, fmt.Errorf("repository remote must be an absolute local path or a supported URL")
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if parsed.Scheme == "ssh" && parsed.User.Username() == "git" && !hasPassword {
			// `git` is the fixed transport account in the canonical SSH URL
			// spelling, not request-carried authentication material.
		} else {
			return Remote{}, fmt.Errorf("repository remote must not contain user information")
		}
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.Contains(raw, "%") {
		return Remote{}, fmt.Errorf("repository remote must not contain query, fragment, or encoded path data")
	}
	switch parsed.Scheme {
	case "https", "ssh":
		if parsed.Host == "" || parsed.Path == "" || !validHost(parsed.Hostname()) {
			return Remote{}, fmt.Errorf("repository remote is missing a valid host or path")
		}
		repository, err := repositoryFromTwoSegments(strings.TrimPrefix(parsed.Path, "/"))
		if err != nil {
			return Remote{}, err
		}
		host := strings.ToLower(parsed.Hostname())
		if port := parsed.Port(); port != "" {
			host += ":" + port
		}
		return Remote{Raw: raw, Identity: Identity{Repository: repository, host: host}}, nil
	case "file":
		if parsed.Host != "" && parsed.Host != "localhost" || !filepath.IsAbs(parsed.Path) {
			return Remote{}, fmt.Errorf("file repository remote must be a local absolute path")
		}
		identity, err := identityFromAbsolutePath(parsed.Path)
		return Remote{Raw: raw, Identity: identity}, err
	default:
		return Remote{}, fmt.Errorf("repository remote uses an unsupported scheme")
	}
}

func identityFromSCP(raw string) (Identity, error) {
	hostSpec, remotePath, found := strings.Cut(raw, ":")
	if !found || hostSpec == "" || remotePath == "" || strings.ContainsAny(hostSpec, "/[]") {
		return Identity{}, fmt.Errorf("repository remote is not a strict scp-style remote")
	}
	host := hostSpec
	if strings.Contains(hostSpec, "@") {
		user, parsedHost, ok := strings.Cut(hostSpec, "@")
		if !ok || user != "git" || parsedHost == "" || strings.Contains(parsedHost, "@") {
			return Identity{}, fmt.Errorf("repository remote contains unsupported user information")
		}
		host = parsedHost
	}
	if !validHost(host) {
		return Identity{}, fmt.Errorf("repository remote host is invalid")
	}
	repository, err := repositoryFromTwoSegments(remotePath)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Repository: repository, host: strings.ToLower(host)}, nil
}

func identityFromAbsolutePath(path string) (Identity, error) {
	clean := filepath.Clean(path)
	if clean != path {
		return Identity{}, fmt.Errorf("local repository remote must be an absolute clean path")
	}
	segments := strings.Split(filepath.ToSlash(strings.TrimSuffix(clean, ".git")), "/")
	if len(segments) < 3 {
		return Identity{}, fmt.Errorf("local repository remote does not carry owner/repository identity")
	}
	repository, err := repositoryFromTwoSegments(strings.Join(segments[len(segments)-2:], "/"))
	if err != nil {
		return Identity{}, err
	}
	return Identity{Repository: repository, localPath: clean}, nil
}

func repositoryFromTwoSegments(path string) (string, error) {
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if strings.Count(path, "/") != 1 {
		return "", fmt.Errorf("repository remote must identify exactly owner/repository")
	}
	owner, name, found := strings.Cut(path, "/")
	if !found || !safeSegment(owner, false) || !safeSegment(name, true) {
		return "", fmt.Errorf("repository remote owner/repository is invalid")
	}
	return path, nil
}

func safeSegment(segment string, repository bool) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for index, character := range segment {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
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

func validHost(host string) bool {
	if host == "" || host != strings.TrimSpace(host) || strings.ContainsAny(host, "@/\\") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
			digit := character >= '0' && character <= '9'
			if !letter && !digit && character != '-' {
				return false
			}
		}
	}
	return true
}
