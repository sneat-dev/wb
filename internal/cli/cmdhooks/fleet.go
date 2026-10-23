package cmdhooks

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
)

func (family commands) applyHooksFleet(out, diagnostic io.Writer, configPath string, repair, force bool) error {
	repos, err := family.git.LocalRepos(family.runtime.Flags().ProjectsRoot, family.runtime.Flags().Filter)
	if err != nil {
		return err
	}
	failed := 0
	for _, repo := range repos {
		result, applyErr := family.git.Apply(hooks.ApplyOptions{
			RepoPath:     repo.Path,
			ConfigPath:   configPath,
			WBExecutable: family.git.Executable(),
			ProjectsRoot: family.runtime.Flags().ProjectsRoot,
			Repair:       repair,
			Force:        force,
		})
		if applyErr != nil {
			failed++
			if err := shared.WriteFormat(diagnostic, "✗ %s: %v\n", repo.Slug(), applyErr); err != nil {
				return err
			}
			continue
		}
		if err := shared.WriteLine(out, repo.Slug()); err != nil {
			return err
		}
		for _, action := range result.Actions {
			if err := shared.WriteLine(out, "  ✓", action); err != nil {
				return err
			}
		}
		if err := shared.WriteLine(out, "  ✓ hooks ready"); err != nil {
			return err
		}
	}
	if err := shared.WriteFormat(out, "Processed %d repositories; %d failed\n", len(repos), failed); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("hooks operation failed in %d of %d repositories", failed, len(repos))
	}
	return nil
}

func (family commands) checkHooksFleet(out io.Writer, configPath string, jsonOut bool) error {
	repos, err := family.git.LocalRepos(family.runtime.Flags().ProjectsRoot, family.runtime.Flags().Filter)
	if err != nil {
		return err
	}
	results := make([]fleetHooksCheck, 0, len(repos))
	problems := 0
	for _, repo := range repos {
		report, checkErr := family.git.Check(repo.Path, configPath, family.git.Executable(), family.runtime.Flags().ProjectsRoot)
		entry := fleetHooksCheck{Repository: repo.Slug()}
		if checkErr != nil {
			entry.Error = checkErr.Error()
			problems++
		} else {
			entry.Report = &report
			problems += len(report.Findings)
		}
		results = append(results, entry)
	}
	if jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			return err
		}
	} else {
		for _, result := range results {
			if err := shared.WriteLine(out, result.Repository); err != nil {
				return err
			}
			if result.Error != "" {
				if err := shared.WriteLine(out, "  ✗", result.Error); err != nil {
					return err
				}
				continue
			}
			if err := printHooksCheckDetails(out, *result.Report); err != nil {
				return err
			}
		}
		if err := shared.WriteFormat(out, "Checked %d repositories; %d problems\n", len(repos), problems); err != nil {
			return err
		}
	}
	if problems > 0 {
		return &CheckError{Count: problems, Fleet: true}
	}
	return nil
}

type fleetHooksCheck struct {
	Repository string             `json:"repository"`
	Report     *hooks.CheckReport `json:"report,omitempty"`
	Error      string             `json:"error,omitempty"`
}
