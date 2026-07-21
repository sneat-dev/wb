package migrate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// FileChange describes one planned, atomic file replacement.
type FileChange struct {
	Path           string   `json:"path"`
	Language       string   `json:"language"`
	OriginalSHA256 string   `json:"original_sha256"`
	Updated        []byte   `json:"-"`
	Steps          []string `json:"steps"`
}

// Plan is an immutable preview of the edits a migration would make.
type Plan struct {
	MigrationID string       `json:"migration_id"`
	Changes     []FileChange `json:"changes"`
	Findings    []Finding    `json:"findings"`
}

// Finding is a semantic-review item discovered while building a plan.
type Finding struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	RuleID   string `json:"rule_id"`
	Message  string `json:"message"`
	Lines    []int  `json:"lines"`
}

// BuildPlan evaluates spec against every selected source file below roots.
// It never writes to those roots.
func BuildPlan(spec Spec, roots ...string) (Plan, error) {
	if err := spec.Validate(); err != nil {
		return Plan{}, err
	}
	if len(roots) == 0 {
		return Plan{}, fmt.Errorf("at least one root is required")
	}
	plan := Plan{MigrationID: spec.ID}
	seen := map[string]bool{}
	for _, root := range roots {
		root, err := filepath.Abs(root)
		if err != nil {
			return Plan{}, err
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if ignoredDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if seen[path] {
				return nil
			}
			language := languageForPath(path)
			if language == "" || !spec.Scope.includes(language) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || !spec.Scope.matches(rel) {
				return err
			}
			original, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			plan.Findings = append(plan.Findings, reviewFindings(spec.Review, language, original, path)...)
			updated, steps, err := transform(spec.Steps, language, original, path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if slices.Equal(original, updated) {
				return nil
			}
			hash := sha256.Sum256(original)
			plan.Changes = append(plan.Changes, FileChange{
				Path:           path,
				Language:       language,
				OriginalSHA256: fmt.Sprintf("%x", hash),
				Updated:        updated,
				Steps:          steps,
			})
			seen[path] = true
			return nil
		}); err != nil {
			return Plan{}, fmt.Errorf("scan %s: %w", root, err)
		}
	}
	sortFindings(plan.Findings)
	return plan, nil
}

// Apply writes all planned changes, refusing to overwrite a source file that
// changed after planning. Each individual file write is atomic.
func Apply(plan Plan) error {
	for _, change := range plan.Changes {
		current, err := os.ReadFile(change.Path)
		if err != nil {
			return fmt.Errorf("read %s: %w", change.Path, err)
		}
		hash := sha256.Sum256(current)
		if fmt.Sprintf("%x", hash) != change.OriginalSHA256 {
			return fmt.Errorf("refusing to overwrite %s: file changed after planning", change.Path)
		}
		info, err := os.Stat(change.Path)
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(change.Path), ".wb-migrate-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err = tmp.Write(change.Updated); err == nil {
			err = tmp.Chmod(info.Mode())
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("write %s: %w", change.Path, err)
		}
		if err := os.Rename(tmpName, change.Path); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("replace %s: %w", change.Path, err)
		}
	}
	return nil
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", ".venv", "dist", "build":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}

func languageForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx", ".mts", ".cts":
		return "typescript"
	default:
		return ""
	}
}

func (s Scope) includes(language string) bool {
	return len(s.Languages) == 0 || slices.Contains(s.Languages, language)
}

func (s Scope) matches(path string) bool {
	path = filepath.ToSlash(path)
	for _, pattern := range s.Exclude {
		if matchPath(path, pattern) {
			return false
		}
	}
	if len(s.Include) == 0 {
		return true
	}
	for _, pattern := range s.Include {
		if matchPath(path, pattern) {
			return true
		}
	}
	return false
}

func matchPath(path, pattern string) bool {
	pattern = filepath.ToSlash(pattern)
	pattern = strings.TrimPrefix(pattern, "**/")
	if strings.HasSuffix(pattern, "/**") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "/**")+"/")
	}
	ok, err := filepath.Match(pattern, path)
	return err == nil && ok
}

func reviewFindings(rules []ReviewRule, language string, source []byte, path string) []Finding {
	var findings []Finding
	for _, rule := range rules {
		if rule.Language != "" && rule.Language != language {
			continue
		}
		re := regexp.MustCompile(rule.Pattern) // validated when the spec was loaded
		var exclude *regexp.Regexp
		if rule.ExcludePattern != "" {
			exclude = regexp.MustCompile(rule.ExcludePattern)
		}
		matches := re.FindAllIndex(source, -1)
		if len(matches) == 0 {
			continue
		}
		lines := make([]int, 0, len(matches))
		for _, match := range matches {
			lineStart := bytes.LastIndexByte(source[:match[0]], '\n') + 1
			lineEnd := len(source)
			if offset := bytes.IndexByte(source[match[1]:], '\n'); offset >= 0 {
				lineEnd = match[1] + offset
			}
			if exclude != nil && exclude.Match(source[lineStart:lineEnd]) {
				continue
			}
			lines = append(lines, sourceLine(source, match[0]))
		}
		if len(lines) == 0 {
			continue
		}
		findings = append(findings, Finding{
			Path: path, Language: language, RuleID: rule.ID, Message: rule.Message, Lines: slices.Compact(lines),
		})
	}
	return findings
}

func sourceLine(source []byte, offset int) int {
	return 1 + strings.Count(string(source[:offset]), "\n")
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path == findings[j].Path {
			return findings[i].RuleID < findings[j].RuleID
		}
		return findings[i].Path < findings[j].Path
	})
}
