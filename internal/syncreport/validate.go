package syncreport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/runner"
)

// ValidateInGitDB builds an isolated database containing the exact records
// and asks the installed InGitDB CLI to validate both definitions and data.
func ValidateInGitDB(ctx context.Context, report Report) error {
	return validateInGitDB(ctx, report, nil)
}

// validateInGitDB is ValidateInGitDB's testable core: r is the runner.Runner
// seam a unit test substitutes with runnertest.Fake. A nil r resolves to the
// production runner.Runner (resolveRunner), exactly as the exported
// ValidateInGitDB does.
func validateInGitDB(ctx context.Context, report Report, r runner.Runner) error {
	directory, err := os.MkdirTemp("", "wb-sync-report-validate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	if err := Install(directory, report); err != nil {
		return err
	}
	return validatePath(ctx, directory, r)
}

// ValidatePath asks the installed InGitDB CLI to validate an assembled
// workbench repository. Publication calls this after installing records and
// before creating a commit.
func ValidatePath(ctx context.Context, directory string) error {
	return validatePath(ctx, directory, nil)
}

// validatePath is ValidatePath's testable core; see validateInGitDB's doc on
// r.
func validatePath(ctx context.Context, directory string, r runner.Runner) error {
	result, err := resolveRunner(r).Run(ctx, "", "ingitdb", "validate", "--path", directory)
	if err != nil {
		return fmt.Errorf("ingitdb validate: %w: %s", err, result.Stdout+result.Stderr)
	}
	return nil
}

// resolveRunner defaults r to the production runner.Runner when the caller
// left it unset -- the field-default pattern spec/plans/coverage-to-100
// task-8 asks every migrated package to use, kept as a package-local
// function here rather than a struct field since ValidatePath/ValidateInGitDB
// are called as bare function values (cmd/wb/sync_report_command.go) and gain
// nothing from a wrapping options type.
func resolveRunner(r runner.Runner) runner.Runner {
	if r != nil {
		return r
	}
	return runner.New()
}

// Install writes the owned schema and report records below root.
func Install(root string, report Report) error {
	paths := map[string][]byte{
		SettingsPath:        []byte(SettingsYAML),
		RootCollectionsPath: []byte(RootCollectionsYAML),
		DefinitionPath:      []byte(DefinitionYAML),
	}
	for _, record := range report.Records {
		paths[filepath.ToSlash(filepath.Join(CollectionRecords, RecordName(report.ID, record.Repository)))] = record.Raw
	}
	for relative, contents := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(absolute, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}
