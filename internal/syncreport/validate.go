package syncreport

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ValidateInGitDB builds an isolated database containing the exact records
// and asks the installed InGitDB CLI to validate both definitions and data.
func ValidateInGitDB(ctx context.Context, report Report) error {
	directory, err := os.MkdirTemp("", "wb-sync-report-validate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	if err := Install(directory, report); err != nil {
		return err
	}
	return ValidatePath(ctx, directory)
}

// ValidatePath asks the installed InGitDB CLI to validate an assembled
// workbench repository. Publication calls this after installing records and
// before creating a commit.
func ValidatePath(ctx context.Context, directory string) error {
	command := exec.CommandContext(ctx, "ingitdb", "validate", "--path", directory)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("ingitdb validate: %w: %s", err, output)
	}
	return nil
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
