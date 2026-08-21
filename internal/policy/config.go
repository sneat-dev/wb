package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigFileName is the per-repository policy declaration. It is deliberately
// tiny: which policy governs this repository, and — only where the module path
// cannot say — what kind of repository it is.
const ConfigFileName = ".wb-deps-policy.yaml"

// RepoConfig is a repository's declaration.
type RepoConfig struct {
	// Found is false when the repository has no config file at all.
	Found bool
	// Path is where the config was read from.
	Path string

	Policy string
	Type   string
	// Strict promotes report-mode rules to errors for this repository only.
	// It is the single permitted local change, and it can only tighten.
	Strict bool
}

// loosening maps a key a repository might reach for onto why it is refused.
// Each of these is a way to end up bound by weaker rules than the fleet, which
// is the failure this whole mechanism exists to prevent.
var loosening = map[string]string{
	"groups": "a repository cannot declare or redefine groups; classification belongs to the central policy",
	"types":  "a repository cannot declare types; it may name which type applies to it with \"type:\"",
	"allow":  "a repository cannot extend an allow list; permitting a new kind of dependency is a central decision",
	"deny":   "there is no deny list to write: rules are allow lists, so anything not permitted is already forbidden",
	"mode":   "rule modes are set fleet-wide so that no repository can demote a rule that binds everyone else",
	"layers": "layer rules and their mode come from the central policy",
	"scopes": "scopes are declared centrally, so source and test rules stay the same everywhere",
	"expect": "policy assertions belong beside the policy they describe",
}

// LoadRepoConfig reads the config file in root. A missing file is not an
// error: detection from the module path is the normal case, and a repository
// with nothing to say should not need to say it.
func LoadRepoConfig(root string) (RepoConfig, error) {
	path := filepath.Join(root, ConfigFileName)
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RepoConfig{}, nil
	}
	if err != nil {
		return RepoConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	var fields map[string]any
	if err := yaml.Unmarshal(contents, &fields); err != nil {
		return RepoConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, key := range sortedKeys(fields) {
		switch key {
		case "policy", "type", "strict":
			continue
		}
		reason, known := loosening[key]
		if !known {
			reason = "it is not a key a repository may set"
		}
		return RepoConfig{}, fmt.Errorf(
			"%s: %q is not allowed here — %s.\nA repository may tighten its own rules and never loosen them; the only local key beyond \"policy\" and \"type\" is \"strict: true\"",
			path, key, reason)
	}

	config := RepoConfig{Found: true, Path: path}
	if raw, ok := fields["policy"]; ok {
		text, ok := raw.(string)
		if !ok {
			return RepoConfig{}, fmt.Errorf("%s: \"policy\" must be a string", path)
		}
		config.Policy = text
	}
	if raw, ok := fields["type"]; ok {
		text, ok := raw.(string)
		if !ok {
			return RepoConfig{}, fmt.Errorf("%s: \"type\" must be a string", path)
		}
		config.Type = text
	}
	if raw, ok := fields["strict"]; ok {
		enabled, ok := raw.(bool)
		if !ok {
			return RepoConfig{}, fmt.Errorf("%s: \"strict\" must be true or false", path)
		}
		if !enabled {
			return RepoConfig{}, fmt.Errorf(
				"%s: \"strict: false\" has no meaning — strict only ever tightens, so switching it off would be a request to be held to weaker rules than the fleet. Remove the key",
				path)
		}
		config.Strict = true
	}
	if config.Policy != "" {
		if _, err := ParseSource(config.Policy); err != nil {
			return RepoConfig{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return config, nil
}

// SourceKind is how a policy reference is resolved.
type SourceKind string

const (
	// SourcePath is a file beside the repository.
	SourcePath SourceKind = "path"
	// SourceFleet is owner/repo//path, resolved against local checkouts.
	SourceFleet SourceKind = "fleet"
	// SourceURL is an https document.
	SourceURL SourceKind = "url"
)

// Source is a parsed policy reference.
type Source struct {
	Raw  string
	Kind SourceKind

	Owner string
	Repo  string
	Path  string
	URL   string
}

// ParseSource reads a policy reference.
//
// A reference names which policy applies and never which release of it. A
// repository frozen on an old policy would be carrying an exception without
// anyone having written one down, so a pinned version is refused here rather
// than honoured.
func ParseSource(raw string) (Source, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Source{}, fmt.Errorf("policy reference is empty")
	}
	if index := strings.LastIndex(trimmed, "@"); index >= 0 {
		return Source{}, fmt.Errorf(
			"policy reference %q pins a release (%q); a repository names which policy governs it, never which release — the release is resolved by the caller so a tightened rule reaches every repository at once",
			trimmed, trimmed[index+1:])
	}
	switch {
	case strings.HasPrefix(trimmed, "https://"):
		return Source{Raw: trimmed, Kind: SourceURL, URL: trimmed}, nil
	case strings.HasPrefix(trimmed, "http://"):
		return Source{}, fmt.Errorf("policy reference %q must use https", trimmed)
	}
	if index := strings.Index(trimmed, "//"); index >= 0 {
		slug := strings.Trim(trimmed[:index], "/")
		path := strings.Trim(trimmed[index+2:], "/")
		owner, repo, found := strings.Cut(slug, "/")
		if !found || owner == "" || repo == "" || path == "" {
			return Source{}, fmt.Errorf("policy reference %q should read owner/repo//path/to/policy.yaml", trimmed)
		}
		return Source{Raw: trimmed, Kind: SourceFleet, Owner: owner, Repo: repo, Path: path}, nil
	}
	return Source{Raw: trimmed, Kind: SourcePath, Path: trimmed}, nil
}

// Locate resolves a path or fleet reference to a file on disk. URL references
// are the caller's to fetch, because caching and network policy belong to the
// command layer rather than to the rule engine.
func (s Source) Locate(repoRoot string, searchRoots []string) (string, error) {
	switch s.Kind {
	case SourcePath:
		candidate := s.Path
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(repoRoot, filepath.FromSlash(candidate))
		}
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("policy file %s not found", candidate)
		}
		return candidate, nil
	case SourceFleet:
		var tried []string
		for _, root := range searchRoots {
			candidate := filepath.Join(root, s.Owner, s.Repo, filepath.FromSlash(s.Path))
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			tried = append(tried, candidate)
		}
		sort.Strings(tried)
		return "", fmt.Errorf(
			"policy %s is not available locally (looked in: %s).\nClone %s/%s, or pass --policy with a path or https URL",
			s.Raw, strings.Join(tried, ", "), s.Owner, s.Repo)
	default:
		return "", fmt.Errorf("policy reference %s must be fetched, not located", s.Raw)
	}
}
