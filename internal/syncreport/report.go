// Package syncreport defines the agent-authored, InGitDB-backed sync analysis
// records that WB validates and publishes to a user's workbench repository.
package syncreport

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion       = 1
	CollectionPath      = "sync-reports"
	CollectionID        = "sync_reports"
	CollectionRecords   = CollectionPath + "/$records"
	DefinitionPath      = CollectionPath + "/.collection/definition.yaml"
	SettingsPath        = ".ingitdb/settings.yaml"
	RootCollectionsPath = ".ingitdb/root-collections.yaml"
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	shaPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	slugPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// Record is one repository's analysis from one sync run. The Markdown body is
// deliberately separate from the queryable frontmatter fields.
type Record struct {
	SchemaVersion   int    `yaml:"schema_version" json:"schema_version"`
	ReportID        string `yaml:"report_id" json:"report_id"`
	Repository      string `yaml:"repository" json:"repository"`
	Finding         string `yaml:"finding" json:"finding"`
	Severity        string `yaml:"severity" json:"severity"`
	State           string `yaml:"state" json:"state"`
	ObservedAt      string `yaml:"observed_at" json:"observed_at"`
	HeadSHA         string `yaml:"head_sha,omitempty" json:"head_sha,omitempty"`
	Title           string `yaml:"title" json:"title"`
	SuggestedAction string `yaml:"suggested_action" json:"suggested_action"`
	Body            string `yaml:"-" json:"body"`
	SourcePath      string `yaml:"-" json:"source_path"`
	Raw             []byte `yaml:"-" json:"-"`
}

// Report is a set of per-repository records from exactly one sync run.
type Report struct {
	ID      string   `json:"report_id"`
	Records []Record `json:"records"`
}

// LoadDirectory reads every top-level .md record in directory. Symlinks,
// nested directories, duplicate repositories, and mixed report IDs are
// refused so an agent cannot smuggle unrelated paths into publication.
func LoadDirectory(directory string) (Report, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Report{}, fmt.Errorf("read sync report directory: %w", err)
	}
	var report Report
	seen := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return Report{}, fmt.Errorf("inspect %s: %w", entry.Name(), err)
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Report{}, fmt.Errorf("%s must be a regular file, not a link or special file", entry.Name())
		}
		path := filepath.Join(directory, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return Report{}, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		record, err := Parse(raw)
		if err != nil {
			return Report{}, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		record.SourcePath, record.Raw = path, raw
		if report.ID == "" {
			report.ID = record.ReportID
		} else if report.ID != record.ReportID {
			return Report{}, fmt.Errorf("%s has report_id %q; all records must use %q", entry.Name(), record.ReportID, report.ID)
		}
		if first := seen[record.Repository]; first != "" {
			return Report{}, fmt.Errorf("duplicate repository %s in %s and %s", record.Repository, first, entry.Name())
		}
		seen[record.Repository] = entry.Name()
		report.Records = append(report.Records, record)
	}
	if len(report.Records) == 0 {
		return Report{}, errors.New("no top-level Markdown records found")
	}
	sort.Slice(report.Records, func(i, j int) bool { return report.Records[i].Repository < report.Records[j].Repository })
	return report, nil
}

// Parse decodes strict YAML frontmatter and preserves the Markdown body.
func Parse(raw []byte) (Record, error) {
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return Record{}, errors.New("record must start with YAML frontmatter delimiter ---")
	}
	rest := raw[4:]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return Record{}, errors.New("record frontmatter must end with delimiter ---")
	}
	var record Record
	decoder := yaml.NewDecoder(bytes.NewReader(rest[:end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode frontmatter: %w", err)
	}
	record.Body = string(rest[end+5:])
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Validate enforces WB's stable subset in addition to InGitDB schema checks.
func (r Record) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if !idPattern.MatchString(r.ReportID) {
		return errors.New("report_id must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes")
	}
	if err := ValidateRepository(r.Repository); err != nil {
		return err
	}
	if !slugPattern.MatchString(r.Finding) {
		return errors.New("finding must be a lowercase slug")
	}
	if r.Severity != "attention" && r.Severity != "error" && r.Severity != "info" {
		return errors.New("severity must be attention, error, or info")
	}
	if r.State != "open" && r.State != "resolved" {
		return errors.New("state must be open or resolved")
	}
	if _, err := time.Parse(time.RFC3339, r.ObservedAt); err != nil {
		return errors.New("observed_at must be RFC3339")
	}
	if r.HeadSHA != "" && !shaPattern.MatchString(r.HeadSHA) {
		return errors.New("head_sha must be an empty value or a 40-character lowercase Git object ID")
	}
	if strings.TrimSpace(r.Title) == "" {
		return errors.New("title is required")
	}
	if strings.TrimSpace(r.SuggestedAction) == "" {
		return errors.New("suggested_action is required")
	}
	if strings.TrimSpace(r.Body) == "" {
		return errors.New("markdown analysis body is required")
	}
	return nil
}

func ValidateRepository(repository string) error {
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || strings.Contains(name, "/") || !repoPartPattern.MatchString(owner) || !repoPartPattern.MatchString(name) {
		return fmt.Errorf("repository %q must be owner/name", repository)
	}
	return nil
}

// RecordName is deterministic and collision-free within a report.
func RecordName(reportID, repository string) string {
	return reportID + "--" + strings.Replace(repository, "/", "%2F", 1) + ".md"
}

const SettingsYAML = "default_record_format: markdown\n"

const RootCollectionsYAML = "sync_reports: sync-reports\n"

// MergeRootCollections registers this collection without replacing other
// InGitDB collections or comments already owned by the user.
func MergeRootCollections(existing []byte) ([]byte, bool, error) {
	if len(existing) == 0 {
		return []byte(RootCollectionsYAML), true, nil
	}
	var collections map[string]string
	if err := yaml.Unmarshal(existing, &collections); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", RootCollectionsPath, err)
	}
	if path, found := collections[CollectionID]; found {
		if path != CollectionPath {
			return nil, false, fmt.Errorf("%s already maps %s to incompatible path %q", RootCollectionsPath, CollectionID, path)
		}
		return existing, false, nil
	}
	merged := append([]byte(nil), existing...)
	if merged[len(merged)-1] != '\n' {
		merged = append(merged, '\n')
	}
	merged = append(merged, RootCollectionsYAML...)
	return merged, true, nil
}

const DefinitionYAML = `titles:
  en: WB sync repository analyses

record_file:
  name: "{key}.md"
  type: "map[string]any"
  format: markdown
  content_field: analysis

columns:
  schema_version: { type: int, required: true }
  report_id: { type: string, required: true }
  repository: { type: string, required: true }
  finding: { type: string, required: true }
  severity: { type: string, required: true }
  state: { type: string, required: true }
  observed_at: { type: datetime, required: true }
  head_sha: { type: string }
  title: { type: string, required: true }
  suggested_action: { type: string, required: true }
  analysis: { type: string, required: true, format: markdown }

columns_order:
  - schema_version
  - report_id
  - repository
  - finding
  - severity
  - state
  - observed_at
  - head_sha
  - title
  - suggested_action
  - analysis
`
