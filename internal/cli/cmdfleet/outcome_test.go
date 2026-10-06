package cmdfleet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/spf13/cobra"
)

func TestCommandOutcomesRenderBeforeFindingsAndPreserveWriterErrors(t *testing.T) {
	t.Parallel()
	for _, family := range []string{"overview", "stats", "merge-policy", "default-branch", "prs"} {
		for _, finding := range []bool{false, true} {
			for _, jsonOutput := range []bool{false, true} {
				t.Run(family+map[bool]string{true: "finding", false: "clean"}[finding]+map[bool]string{true: "json", false: "text"}[jsonOutput], func(t *testing.T) {
					t.Parallel()
					deps := fakeDependencies()
					flags := shared.Flags{}
					count := 0
					deps.Overview = func(context.Context, fleetinspect.Request) (fleetinspect.OverviewReport, error) {
						count++
						r := fleetinspect.OverviewReport{}
						if finding {
							r.Stats.Git.Error = 1
						}
						return r, nil
					}
					deps.Stats = func(context.Context, fleetinspect.Request) (fleetinspect.StatsReport, error) {
						count++
						r := fleetinspect.StatsReport{}
						if finding {
							r.Git.Error = 1
						}
						return r, nil
					}
					deps.MergePolicy = func(context.Context, mergepolicy.Request, io.Writer) (mergepolicy.Report, error) {
						count++
						r := mergepolicy.Report{}
						if finding {
							r.Summary.Blocked = 1
						}
						return r, nil
					}
					deps.DefaultBranch = func(context.Context, defaultbranch.Request, io.Writer) (defaultbranch.Report, error) {
						count++
						r := defaultbranch.Report{}
						if finding {
							r.Summary.Drift = 1
						}
						return r, nil
					}
					deps.Inventory = func(context.Context, prinventory.Options) prinventory.Report {
						count++
						return prinventory.Report{Complete: !finding}
					}
					if finding {
						deps.InventoryOwners = func([]string) ([]prinventory.Owner, []prinventory.Diagnostic) {
							return nil, []prinventory.Diagnostic{{Severity: "error", Message: "owners incomplete"}}
						}
					}
					command := New(testRuntime(&flags), deps)
					args := []string{family}
					if jsonOutput {
						if family == "default-branch" || family == "merge-policy" {
							args = append(args, "--json")
						} else {
							args = append(args, "--format", "json")
						}
					}
					output, _, err := execute(t, command, args...)
					if count != 1 || strings.TrimSpace(output) == "" {
						t.Fatal(count, output, err)
					}
					if finding {
						var coded *codedError
						if !errors.As(err, &coded) || coded.code != shared.ExitFindings {
							t.Fatal(err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					command = quietCommand(New(testRuntime(&flags), deps))
					command.SetOut(&defaultBranchFailWriter{failAt: 1})
					command.SetErr(&bytes.Buffer{})
					command.SetArgs(args)
					if err := command.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "output unavailable") {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
func TestMergePolicyRenderingSurfacesEveryWriterFailure(t *testing.T) {
	t.Parallel()
	report := mergepolicy.Report{Repositories: []mergepolicy.Repository{{Repository: "acme/app", Disposition: "drift", Conflicts: []string{"queue"}}}, Rulesets: []mergepolicy.RulesetChange{{SourceType: "Repository", ID: 7}}, ReportPath: "report"}
	for i := 1; i <= 5; i++ {
		writer := &defaultBranchFailWriter{failAt: i}
		if err := printMergePolicyReport(writer, report); err == nil {
			t.Fatal(i)
		}
	}
}
func TestDefaultBranchHelpDelegatesWithoutAnOperation(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.DefaultBranch = func(context.Context, defaultbranch.Request, io.Writer) (defaultbranch.Report, error) {
		t.Fatal("operation on help")
		return defaultbranch.Report{}, nil
	}
	command := NewDefaultBranch(testRuntime(&shared.Flags{}), deps)
	if _, _, err := execute(t, command, "--help"); err != nil {
		t.Fatal(err)
	}
}

var _ *cobra.Command
