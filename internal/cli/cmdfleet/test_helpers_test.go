package cmdfleet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/spf13/cobra"
)

type codedError struct {
	code    int
	message string
}

func (err *codedError) Error() string { return err.message }
func testRuntime(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func fakeDependencies() Dependencies {
	return Dependencies{
		Budget: func() int { return 8 }, MkdirAll: func(string, os.FileMode) error { return nil }, WriteFile: func(string, []byte, os.FileMode) error { return nil },
		Overview: func(context.Context, fleetinspect.Request) (fleetinspect.OverviewReport, error) {
			return fleetinspect.OverviewReport{SchemaVersion: 1}, nil
		}, Stats: func(context.Context, fleetinspect.Request) (fleetinspect.StatsReport, error) {
			return fleetinspect.StatsReport{SchemaVersion: 1}, nil
		},
		InventoryOwners: func([]string) ([]prinventory.Owner, []prinventory.Diagnostic) { return nil, nil }, Inventory: func(context.Context, prinventory.Options) prinventory.Report {
			return prinventory.Report{Complete: true}
		},
		DefaultBranch: func(context.Context, defaultbranch.Request, io.Writer) (defaultbranch.Report, error) {
			return defaultbranch.Report{}, nil
		}, MergePolicy: func(context.Context, mergepolicy.Request, io.Writer) (mergepolicy.Report, error) {
			return mergepolicy.Report{}, nil
		},
		Status: statusview.Dependencies{Collect: func(reposelection.Request, repostatus.Observer) (repostatus.Index, error) {
			return repostatus.Index{}, nil
		}, WriteReports: func(repostatus.Index, string, bool, string) error { return nil }, Interactive: func(io.Writer, bool) bool { return false }}}
}
func execute(t *testing.T, command *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(&out)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.ExecuteContext(t.Context())
	return out.String(), stderr.String(), err
}

type defaultBranchFailWriter struct {
	writes int
	failAt int
}

func (writer *defaultBranchFailWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errors.New("output unavailable")
	}
	return len(value), nil
}

func fakeRoot(flags *shared.Flags, deps Dependencies) *cobra.Command {
	root := &cobra.Command{Use: "wb", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&flags.ProjectsRoot, "projects-root", "", "fixture projects")
	root.PersistentFlags().StringVar(&flags.Filter, "filter", "", "fixture filter")
	root.PersistentFlags().StringArrayVar(&flags.ExtraOrgs, "org", nil, "fixture org")
	root.AddCommand(New(testRuntime(flags), deps))
	return root
}

func memoryRead(files map[string][]byte, path string) ([]byte, error) {
	data, ok := files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func quietCommand(command *cobra.Command) *cobra.Command {
	command.SilenceUsage = true
	command.SilenceErrors = true
	return command
}
