package repopath

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// flatCanonicalJoinAllowlist names the production lines that deliberately join
// a projects root and a repository coordinate without the canonical resolver.
// Each entry is "<file>|<distinctive snippet>" so a line number can move without
// breaking it, and each carries the reason it is legitimate.
//
// Every other occurrence is a defect: a flat <root>/{owner}/{repository} path is
// invisible to a migrated fleet, so the command that builds it reads the clone
// as missing, clones a duplicate beside the real one, or writes a marker into a
// directory nobody has.
var flatCanonicalJoinAllowlist = map[string]string{
	"internal/streams/paths.go|filepath.FromSlash(repository)":                      "documented fallback for a bare name or an unresolvable/ambiguous coordinate; the resolver is tried first",
	"internal/worktrees/lifecycle.go|filepath.FromSlash(slug)":                      "diagnostic-only fallback after CanonicalRepositoryPath already rejected a malformed slug",
	"internal/worktrees/orphans_residue.go|filepath.Join(projectsRoot, repository)": "diagnostic-only fallback after CanonicalRepositoryPath already rejected a malformed coordinate",
}

// rootishArguments matches the identifiers that mean "the projects root".
var rootishArguments = regexp.MustCompile(`^(root|.*ProjectsRoot|.*GitHubDir|projectsRoot|githubDir|projectsDir|projects)$`)

// coordinateArguments matches the identifiers that name one repository level.
var coordinateArguments = regexp.MustCompile(`^(owner|Owner|org|Org|name|Name|repo|Repo|repository|Repository|parts\[0\]|parts\[1\])$`)

// slugSplittingCalls matches a coordinate being turned into path segments
// directly rather than through the resolver.
var slugSplittingCalls = regexp.MustCompile(`FromSlash\((repository|slug|repo|tracked|claim\.Repository|record\.Repository|receipt\.Repository|entry\.Repository|options\.ExactRepository|options\.Repository)\)`)

// ownerLevelArguments matches an identifier that holds one owner level. Joining
// a projects root onto an owner level is how the missing-clone path used to be
// written (and how the stray <root>/{owner} directory was created); it is
// legitimate only for a walk that deliberately visits owner levels.
var ownerLevelArguments = regexp.MustCompile(`^(owner|Owner|org|Org|owner\.Name\(\)|organization\.Name\(\)|ownerEntry\.Name\(\))$`)

// repositorySlugArguments matches an identifier that holds a whole
// owner/repository coordinate, which a projects root must never be joined onto
// directly.
var repositorySlugArguments = regexp.MustCompile(`^(repository|slug|repo|tracked|.+Repository|.+Slug)$`)

// TestNoProductionCodeDerivesACanonicalClonePathFlat is the recurrence guard.
// Three review rounds found flat canonical joins one at a time because nothing
// stopped a new one from being written; this test fails the moment one is, and
// it fails again when an allowlisted line disappears so the list cannot rot.
func TestNoProductionCodeDerivesACanonicalClonePathFlat(t *testing.T) {
	root := repoRoot(t)
	var scanned int
	type offence struct {
		file   string
		line   int
		reason string
		source string
	}
	var offences []offence
	matchedAllowlist := map[string]bool{}

	for _, top := range []string{"cmd", "internal"} {
		walkErr := filepath.WalkDir(filepath.Join(root, top), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == ".worktrees" || entry.Name() == "testdata" || entry.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			relative = filepath.ToSlash(relative)
			scanned++
			for index, line := range strings.Split(string(contents), "\n") {
				reason := flatCanonicalJoinReason(line)
				if reason == "" {
					continue
				}
				allowKey := allowlistKey(relative, line)
				if _, allowed := flatCanonicalJoinAllowlist[allowKey]; allowed {
					matchedAllowlist[allowKey] = true
					continue
				}
				offences = append(offences, offence{file: relative, line: index + 1, reason: reason, source: strings.TrimSpace(line)})
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("scan %s: %v", top, walkErr)
		}
	}
	if scanned < 200 {
		t.Fatalf("scanned only %d production files; the guard is not looking at the tree", scanned)
	}

	if len(offences) > 0 {
		var report strings.Builder
		fmt.Fprintf(&report, "%d production line(s) derive a canonical clone path without the shared resolver:\n", len(offences))
		for _, item := range offences {
			fmt.Fprintf(&report, "  %s:%d: %s\n      %s\n", item.file, item.line, item.reason, item.source)
		}
		report.WriteString("Resolve through worktrees.CanonicalRepositoryPath, worktrees.CanonicalRepositoryPathForURL,\n")
		report.WriteString("orchestrate.CanonicalClonePath, or repopath.Locate / repopath.ClonePathForURL where\n")
		report.WriteString("internal/worktrees cannot be imported. If a line is genuinely flat, add it to\n")
		report.WriteString("flatCanonicalJoinAllowlist with the reason.\n")
		t.Fatal(report.String())
	}

	var stale []string
	for key := range flatCanonicalJoinAllowlist {
		if !matchedAllowlist[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("flatCanonicalJoinAllowlist entries no longer match any production line; remove them:\n  %s", strings.Join(stale, "\n  "))
	}
}

// flatCanonicalJoinReason reports why one source line looks like a flat
// canonical join, or "" when it does not.
func flatCanonicalJoinReason(line string) string {
	code := strings.TrimSpace(line)
	if strings.HasPrefix(code, "//") {
		return ""
	}
	if !strings.Contains(code, "filepath.Join(") {
		return ""
	}
	arguments := joinArguments(code)
	if len(arguments) < 2 || !rootishArguments.MatchString(arguments[0]) {
		return ""
	}
	if slugSplittingCalls.MatchString(code) {
		return "joins a projects root onto a repository slug"
	}
	if len(arguments) == 2 && repositorySlugArguments.MatchString(arguments[1]) {
		return "joins a projects root onto a whole repository coordinate"
	}
	if len(arguments) == 2 && ownerLevelArguments.MatchString(arguments[1]) {
		return "joins a projects root onto a bare owner level instead of the resolved clone"
	}
	if len(arguments) >= 3 {
		coordinates := 0
		for _, argument := range arguments[1:] {
			if coordinateArguments.MatchString(argument) {
				coordinates++
			}
		}
		if coordinates >= 2 {
			return "joins a projects root onto an owner/name pair"
		}
	}
	return ""
}

// joinArguments returns the trimmed top-level arguments of the first
// filepath.Join call on one line. Anything it cannot parse returns fewer than
// three arguments, which the caller treats as not matching.
func joinArguments(line string) []string {
	start := strings.Index(line, "filepath.Join(")
	if start < 0 {
		return nil
	}
	rest := line[start+len("filepath.Join("):]
	depth := 1
	end := -1
	for index, character := range rest {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = index
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	var arguments []string
	depth = 0
	current := strings.Builder{}
	for _, character := range rest[:end] {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				arguments = append(arguments, strings.TrimSpace(current.String()))
				current.Reset()
				continue
			}
		}
		current.WriteRune(character)
	}
	arguments = append(arguments, strings.TrimSpace(current.String()))
	return arguments
}

// allowlistKey identifies an allowlisted line by file and a distinctive snippet,
// so moving the line does not invalidate the entry.
func allowlistKey(file, line string) string {
	code := strings.TrimSpace(line)
	for key := range flatCanonicalJoinAllowlist {
		snippet := key[strings.Index(key, "|")+1:]
		if strings.HasPrefix(key, file+"|") && strings.Contains(code, snippet) {
			return key
		}
	}
	if index := strings.Index(code, "filepath.FromSlash("); index >= 0 {
		return file + "|" + code[index:]
	}
	if index := strings.Index(code, "filepath.Join("); index >= 0 {
		snippet := code[index:]
		if argumentEnd := strings.Index(snippet, ")"); argumentEnd >= 0 {
			snippet = snippet[:argumentEnd+1]
		}
		return file + "|" + snippet
	}
	return file + "|" + code
}

// repoRoot locates the checkout from this package's working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %s has no go.mod: %v", root, err)
	}
	return root
}
