package deps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Peer verdicts. Every published peer requirement lands in exactly one of
// them, and the set is deliberately five rather than two: "the target does not
// have this package at all", "the target has it at a version the peer range
// rejects", and "WB cannot evaluate this range shape" are three different
// answers to "can I reuse this package here", and collapsing them into a
// single "unsatisfied" would hide which one an operator is actually looking
// at.
const (
	// PeerSatisfied means the target's version is admitted by the peer range.
	PeerSatisfied = "satisfied"
	// PeerUnsatisfied means the target has the package at a version the peer
	// range rejects. This is the finding that blocks reuse.
	PeerUnsatisfied = "unsatisfied"
	// PeerMissing means the target does not have the package at all.
	PeerMissing = "missing"
	// PeerOptionalMissing means the target does not have a package the
	// publisher marked optional in peerDependenciesMeta. Not a finding.
	PeerOptionalMissing = "optional_missing"
	// PeerUnevaluated means WB will not guess: either the peer range or the
	// installed version is a shape outside the evaluated subset. It is
	// reported with its reason and never silently counted as satisfied.
	PeerUnevaluated = "unevaluated"
)

// PeerOptions asks one question: can this published package be used in that
// checkout?
type PeerOptions struct {
	// Package is the published package, optionally pinned: "@sneat/core" or
	// "@sneat/core@0.31.0". Unpinned means the registry's "latest".
	Package string
	// Against is the path of the checkout whose installed versions the peers
	// are judged against.
	Against string
	Timeout time.Duration
	Retry   int
	// PublishedPeers is injectable so the whole verdict table can be tested
	// without a network round trip.
	PublishedPeers func(ctx context.Context, pkg string) (PublishedPeerSet, error)
	// Now is injectable for deterministic timestamps.
	Now func() time.Time
}

// PublishedPeerSet is the registry's own statement about a published version's
// peer requirements.
type PublishedPeerSet struct {
	Version  string
	Peers    map[string]string
	Optional map[string]bool
	Source   string
}

// PeerReport is the deterministic answer, one row per published peer.
type PeerReport struct {
	SchemaVersion int         `json:"schema_version" yaml:"schema_version"`
	Package       string      `json:"package" yaml:"package"`
	Version       string      `json:"version,omitempty" yaml:"version,omitempty"`
	Source        string      `json:"source,omitempty" yaml:"source,omitempty"`
	Against       string      `json:"against" yaml:"against"`
	AgainstName   string      `json:"against_name,omitempty" yaml:"against_name,omitempty"`
	ObservedAt    time.Time   `json:"observed_at" yaml:"observed_at"`
	Peers         []PeerRow   `json:"peers" yaml:"peers"`
	Summary       PeerSummary `json:"summary" yaml:"summary"`
}

// PeerRow is one published peer requirement judged against the target.
type PeerRow struct {
	Peer     string `json:"peer" yaml:"peer"`
	Required string `json:"required" yaml:"required"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
	// Installed is what the target actually resolves, and InstalledSource is
	// the lockfile or manifest field that says so. An answer with no evidence
	// trail is not an answer.
	Installed       string `json:"installed,omitempty" yaml:"installed,omitempty"`
	InstalledSource string `json:"installed_source,omitempty" yaml:"installed_source,omitempty"`
	Verdict         string `json:"verdict" yaml:"verdict"`
	Reason          string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// PeerSummary counts the verdicts so a caller can gate on them.
type PeerSummary struct {
	Total           int `json:"total" yaml:"total"`
	Satisfied       int `json:"satisfied" yaml:"satisfied"`
	Unsatisfied     int `json:"unsatisfied" yaml:"unsatisfied"`
	Missing         int `json:"missing" yaml:"missing"`
	OptionalMissing int `json:"optional_missing" yaml:"optional_missing"`
	Unevaluated     int `json:"unevaluated" yaml:"unevaluated"`
}

// InspectPeers answers "can I reuse this package here" with evidence instead
// of an install attempt.
//
// The question is asked constantly and answered badly: run the install, read
// whatever npm prints about peer conflicts, and hope the error names the real
// culprit. That mutates the checkout to find out, and a pnpm workspace's peer
// warnings do not distinguish "you are two majors behind" from "the publisher
// marked this optional". So WB reads the published package's own
// peerDependencies, reads what the target checkout actually resolves, and
// prints one row per peer with a verdict and the evidence behind it. Nothing
// is installed and nothing is written.
func InspectPeers(ctx context.Context, options PeerOptions) (PeerReport, error) {
	pkg := strings.TrimSpace(options.Package)
	if pkg == "" {
		return PeerReport{}, fmt.Errorf("deps peers requires a published package name, e.g. @sneat/core or @sneat/core@0.31.0")
	}
	against := strings.TrimSpace(options.Against)
	if against == "" {
		return PeerReport{}, fmt.Errorf("deps peers requires --against <repository-path>: the checkout whose installed versions the peers are judged against")
	}
	absolute, err := filepath.Abs(against)
	if err != nil {
		return PeerReport{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return PeerReport{}, err
	}
	if !info.IsDir() {
		return PeerReport{}, fmt.Errorf("--against %s is not a directory", absolute)
	}
	if err := ValidateNpmPackageName(peerPackageName(pkg)); err != nil {
		return PeerReport{}, err
	}
	published, err := readPublishedPeers(ctx, pkg, options)
	if err != nil {
		return PeerReport{}, err
	}
	installed, targetName, err := installedNpmVersions(absolute)
	if err != nil {
		return PeerReport{}, err
	}
	report := PeerReport{
		SchemaVersion: 1, Package: peerPackageName(pkg), Version: published.Version,
		Source: published.Source, Against: absolute, AgainstName: targetName,
		ObservedAt: peerNow(options),
	}
	names := make([]string, 0, len(published.Peers))
	for name := range published.Peers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		report.Peers = append(report.Peers, judgePeer(name, published.Peers[name], published.Optional[name], installed[name]))
	}
	report.Summary = summarizePeers(report.Peers)
	return report, nil
}

// PeersFailed reports whether the run found something that blocks reuse. An
// unevaluated range is not a failure: WB declined to judge it, which is a
// different statement from judging it and finding a conflict.
func PeersFailed(report PeerReport) bool {
	return report.Summary.Unsatisfied > 0 || report.Summary.Missing > 0
}

// judgePeer turns one published requirement plus one piece of local evidence
// into a verdict.
func judgePeer(name, required string, optional bool, evidence peerEvidence) PeerRow {
	row := PeerRow{Peer: name, Required: required, Optional: optional}
	if evidence.Version == "" {
		row.Verdict = PeerMissing
		row.Reason = "the target checkout neither declares nor resolves this package"
		if evidence.Reason != "" {
			row.Reason = evidence.Reason
		}
		if optional {
			row.Verdict = PeerOptionalMissing
			row.Reason = "the publisher marks this peer optional in peerDependenciesMeta, and the target does not provide it"
		}
		return row
	}
	row.Installed = evidence.Version
	row.InstalledSource = evidence.Source
	verdict := npmRangeAdmits(required, evidence.Version)
	switch {
	case !verdict.Evaluated:
		row.Verdict = PeerUnevaluated
		row.Reason = verdict.Reason
	case verdict.Admits:
		row.Verdict = PeerSatisfied
	default:
		row.Verdict = PeerUnsatisfied
		row.Reason = "the target resolves " + evidence.Version + ", which " + required + " does not admit"
	}
	return row
}

func summarizePeers(rows []PeerRow) PeerSummary {
	summary := PeerSummary{Total: len(rows)}
	for _, row := range rows {
		switch row.Verdict {
		case PeerSatisfied:
			summary.Satisfied++
		case PeerUnsatisfied:
			summary.Unsatisfied++
		case PeerMissing:
			summary.Missing++
		case PeerOptionalMissing:
			summary.OptionalMissing++
		case PeerUnevaluated:
			summary.Unevaluated++
		}
	}
	return summary
}

// peerEvidence is what the target checkout says about one package.
type peerEvidence struct {
	Version string
	Source  string
	Reason  string
}

// installedNpmVersions indexes what the target checkout actually resolves for
// every package it declares, plus every package its lockfiles resolve.
//
// A declared reference wins over a bare lockfile entry, because a package the
// checkout declares is one it owns; a lockfile-only entry is a transitive
// install that a later dependency change can move without warning. Both are
// reported, and the source column says which is which.
func installedNpmVersions(root string) (map[string]peerEvidence, string, error) {
	packageManifests, _, err := npmManifestFiles(root)
	if err != nil {
		return nil, "", err
	}
	lockScopes, err := readNpmLockScopes(root)
	if err != nil {
		return nil, "", err
	}
	lockDirs := make([]string, 0, len(lockScopes))
	for directory := range lockScopes {
		lockDirs = append(lockDirs, directory)
	}
	sort.Strings(lockDirs)

	installed := map[string]peerEvidence{}
	// Lockfile-only entries first, so a declared reference below overwrites
	// them rather than the other way round.
	for _, directory := range lockDirs {
		scope := lockScopes[directory]
		for name, locked := range scope.Versions {
			if conflict := locked.Conflict(); conflict != "" {
				installed[name] = peerEvidence{Source: locked.Source, Reason: conflict}
				continue
			}
			installed[name] = peerEvidence{Version: locked.Version(), Source: locked.Source + " (transitive)"}
		}
	}

	targetName := ""
	for _, relative := range packageManifests {
		contents, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative))) // #nosec G304 -- path comes from a walk of the inspected checkout
		if readErr != nil {
			return nil, "", readErr
		}
		pkg, requirements, parseErr := parseNpmPackageJSONManifest(root, relative, contents)
		if parseErr != nil {
			return nil, "", fmt.Errorf("parse %s: %w", relative, parseErr)
		}
		if relative == "package.json" && pkg != nil {
			targetName = pkg.Name
		}
		for _, requirement := range requirements {
			evidence := npmSelectedVersion(requirement.Dependency, relative, requirement.Version, lockDirs, lockScopes, time.Time{})
			installed[requirement.Dependency] = peerEvidence{
				Version: evidence.Value, Source: peerEvidenceSource(evidence, relative, requirement.Field), Reason: evidence.Reason,
			}
		}
	}
	return installed, targetName, nil
}

func peerEvidenceSource(evidence VersionEvidence, manifest, field string) string {
	if evidence.Source == "declared_fallback" {
		return manifest + " " + field + " (declared; no lockfile evidence)"
	}
	return evidence.Source
}

// readPublishedPeers asks the registry what the package requires of its host.
func readPublishedPeers(ctx context.Context, pkg string, options PeerOptions) (PublishedPeerSet, error) {
	if options.PublishedPeers != nil {
		return options.PublishedPeers(ctx, pkg)
	}
	arguments := []string{"view", pkg, "version", "peerDependencies", "peerDependenciesMeta", "--json"}
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, "", "pnpm", arguments...)
	if err != nil {
		return PublishedPeerSet{}, err
	}
	set, err := parsePublishedPeers(output)
	if err != nil {
		return PublishedPeerSet{}, fmt.Errorf("decode published peer requirements for %s: %w", pkg, err)
	}
	set.Source = "pnpm " + strings.Join(arguments, " ")
	return set, nil
}

// parsePublishedPeers decodes the multi-field `pnpm view … --json` object,
// the same shape latestPublishedNpmRelease already relies on. A package with
// no peers at all decodes to an empty set rather than an error: "requires
// nothing of its host" is a legitimate — and very reusable — answer.
func parsePublishedPeers(output string) (PublishedPeerSet, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || trimmed == "undefined" {
		return PublishedPeerSet{Peers: map[string]string{}, Optional: map[string]bool{}}, nil
	}
	var fields struct {
		Version          string            `json:"version"`
		PeerDependencies map[string]string `json:"peerDependencies"`
		Meta             map[string]struct {
			Optional bool `json:"optional"`
		} `json:"peerDependenciesMeta"`
	}
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return PublishedPeerSet{}, err
	}
	set := PublishedPeerSet{Version: fields.Version, Peers: map[string]string{}, Optional: map[string]bool{}}
	for name, requirement := range fields.PeerDependencies {
		set.Peers[name] = requirement
	}
	for name, meta := range fields.Meta {
		if meta.Optional {
			set.Optional[name] = true
		}
	}
	return set, nil
}

// peerPackageName strips a pinned version from "@scope/name@1.2.3" without
// mistaking a scope's leading "@" for a version separator.
func peerPackageName(reference string) string {
	trimmed := strings.TrimSpace(reference)
	if index := strings.LastIndex(trimmed, "@"); index > 0 {
		return trimmed[:index]
	}
	return trimmed
}

func peerNow(options PeerOptions) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}
